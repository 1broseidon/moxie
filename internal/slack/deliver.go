package slack

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/scheduler"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
	"github.com/slack-go/slack"
)

const slackMessageChunkSize = 35000

var sendTagPattern = regexp.MustCompile(`(?s)<send>\s*(.*?)\s*</send>`)

type messenger interface {
	PostMessage(channelID string, options ...slack.MsgOption) (string, string, error)
	UpdateMessage(channelID, timestamp string, options ...slack.MsgOption) (string, string, string, error)
	DeleteMessage(channel, messageTimestamp string) (string, string, error)
}

// fileUploader is implemented by *slack.Client and allows uploading files
// via the files.getUploadURLExternal → upload → files.completeUploadExternal flow.
type fileUploader interface {
	GetUploadURLExternalContext(ctx context.Context, params slack.GetUploadURLExternalParameters) (*slack.GetUploadURLExternalResponse, error)
	UploadToURL(ctx context.Context, params slack.UploadToURLParameters) error
	CompleteUploadExternalContext(ctx context.Context, params slack.CompleteUploadExternalParameters) (*slack.CompleteUploadExternalResponse, error)
}

type Messenger = messenger

func SendWebhookResponse(url string, text string, responseType string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	return slack.PostWebhook(url, &slack.WebhookMessage{
		Text:         text,
		ResponseType: responseType,
	})
}

type runningStatus struct {
	api messenger
	job *store.PendingJob
	st  jobState
}

func newRunningStatus(api messenger, job *store.PendingJob) *runningStatus {
	return &runningStatus{
		api: api,
		job: job,
		st:  readJobState(job.ID),
	}
}

func conversationFromID(id string) chat.ConversationRef {
	return chat.ParseConversationID(id)
}

func compactText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func truncateRunes(text string, max int) string {
	runes := []rune(text)
	if len(runes) <= max {
		return text
	}
	return strings.TrimSpace(string(runes[:max-1])) + "…"
}

func renderActivityText(activity string) string {
	activity = compactText(activity)
	if activity == "" {
		return "Working..."
	}

	words := strings.Fields(activity)
	verb := strings.ToLower(words[0])
	detail := activity

	summary := "Working..."
	switch verb {
	case "read":
		summary = "Reading files..."
	case "write":
		summary = "Writing files..."
	case "edit", "patch":
		summary = "Editing files..."
	case "bash", "sh", "zsh":
		summary = "Running command..."
	case "rg", "grep", "find", "ls", "glob":
		summary = "Searching..."
	}

	if detail == "" {
		return summary
	}
	return summary + "\n" + truncateRunes(detail, 140)
}

func (s *runningStatus) show(activity string) {
	text := renderActivityText(activity)
	if text == s.st.StatusText {
		return
	}

	target := replyConversationForJob(s.job, s.st)
	if target.Provider != chat.ProviderSlack || target.ChannelID == "" {
		return
	}

	if s.st.StatusMessage.MessageID == "" {
		_, ts, err := postPlainText(s.api, target, text)
		if err != nil {
			log.Printf("slack status send error: %v", err)
			return
		}
		s.st.StatusMessage = chat.MessageRef{
			Conversation: target,
			MessageID:    ts,
		}
		s.st.StatusText = text
		writeJobState(s.job.ID, s.st)
		return
	}

	_, _, _, err := s.api.UpdateMessage(
		s.st.StatusMessage.Conversation.ChannelID,
		s.st.StatusMessage.MessageID,
		messageOptions(s.st.StatusMessage.Conversation, text)...,
	)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "message_not_modified") {
			return
		}
		log.Printf("slack status edit error for %s: %v", s.st.StatusMessage.MessageID, err)
		return
	}
	s.st.StatusText = text
	writeJobState(s.job.ID, s.st)
}

func (s *runningStatus) clear() {
	if s.st.StatusMessage.MessageID != "" {
		if _, _, err := s.api.DeleteMessage(s.st.StatusMessage.Conversation.ChannelID, s.st.StatusMessage.MessageID); err != nil {
			log.Printf("slack status delete error for %s: %v", s.st.StatusMessage.MessageID, err)
		}
	}
	removeJobState(s.job.ID)
}

func messageOptions(conversation chat.ConversationRef, text string) []slack.MsgOption {
	opts := []slack.MsgOption{slack.MsgOptionText(text, false)}
	if conversation.ThreadID != "" {
		opts = append(opts, slack.MsgOptionTS(conversation.ThreadID))
	}
	return opts
}

func postPlainText(api messenger, conversation chat.ConversationRef, text string) (channelID string, ts string, err error) {
	return api.PostMessage(conversation.ChannelID, messageOptions(conversation, text)...)
}

func SendPlainText(api messenger, conversation chat.ConversationRef, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}

	sentAny := false
	var firstErr error
	for _, chunk := range chat.SplitText(text, slackMessageChunkSize) {
		if _, _, err := postPlainText(api, conversation, chunk); err != nil {
			log.Printf("slack send error: %v", err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		sentAny = true
	}

	if !sentAny && firstErr != nil {
		return firstErr
	}
	return nil
}

func splitResponseFiles(text string) ([]string, string) {
	matches := sendTagPattern.FindAllStringSubmatch(text, -1)
	paths := make([]string, 0, len(matches))
	for _, match := range matches {
		path := strings.TrimSpace(match[1])
		if path != "" {
			paths = append(paths, path)
		}
	}
	cleaned := strings.TrimSpace(sendTagPattern.ReplaceAllString(text, ""))
	return paths, cleaned
}

// uploadFile uploads a local file to a Slack channel using the external upload flow.
// It returns nil on success or an error if any step fails.
func uploadFile(uploader fileUploader, channelID, threadTS, filePath string) error {
	filePath = strings.TrimSpace(filePath)
	if filePath == "" {
		return fmt.Errorf("empty file path")
	}

	info, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("stat %s: %w", filePath, err)
	}
	if info.IsDir() {
		return fmt.Errorf("%s is a directory", filePath)
	}

	ctx := context.Background()
	fileName := filepath.Base(filePath)

	// Step 1: Get upload URL
	urlResp, err := uploader.GetUploadURLExternalContext(ctx, slack.GetUploadURLExternalParameters{
		FileName: fileName,
		FileSize: int(info.Size()),
	})
	if err != nil {
		return fmt.Errorf("get upload URL for %s: %w", fileName, err)
	}

	// Step 2: Upload file content
	f, err := os.Open(filePath)
	if err != nil {
		return fmt.Errorf("open %s: %w", filePath, err)
	}
	defer f.Close()

	if err := uploader.UploadToURL(ctx, slack.UploadToURLParameters{
		UploadURL: urlResp.UploadURL,
		Reader:    f,
		Filename:  fileName,
	}); err != nil {
		return fmt.Errorf("upload %s: %w", fileName, err)
	}

	// Step 3: Complete upload and share to channel
	completeParams := slack.CompleteUploadExternalParameters{
		Files: []slack.FileSummary{
			{ID: urlResp.FileID, Title: fileName},
		},
		Channel: channelID,
	}
	if threadTS != "" {
		completeParams.ThreadTimestamp = threadTS
	}
	if _, err := uploader.CompleteUploadExternalContext(ctx, completeParams); err != nil {
		return fmt.Errorf("complete upload for %s: %w", fileName, err)
	}

	return nil
}

// sendTaggedFiles uploads files extracted from <send> tags to Slack.
// Returns the list of filenames that failed to upload.
func sendTaggedFiles(uploader fileUploader, conversation chat.ConversationRef, paths []string) []string {
	var failed []string
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if err := uploadFile(uploader, conversation.ChannelID, conversation.ThreadID, path); err != nil {
			log.Printf("slack file upload error for %s: %v", filepath.Base(path), err)
			failed = append(failed, filepath.Base(path))
		}
	}
	return failed
}

func replyConversationForJob(job *store.PendingJob, st jobState) chat.ConversationRef {
	if job != nil {
		target := conversationFromID(job.ReplyConversation)
		if target.Provider == chat.ProviderSlack && target.ChannelID != "" {
			return target
		}
	}
	if st.ReplyConversation.Provider == chat.ProviderSlack && st.ReplyConversation.ChannelID != "" {
		return st.ReplyConversation
	}
	return conversationFromID(job.ConversationID)
}

func emptyResultMessage(backend string) string {
	backend = strings.TrimSpace(backend)
	if backend == "" {
		return "Backend returned an empty response."
	}
	return fmt.Sprintf("Backend %s returned an empty response.", backend)
}

func DeliverJobResult(api messenger, job *store.PendingJob) error {
	return deliverJobResult(api, nil, job)
}

// DeliverJobResultWithFiles delivers a job result with file upload support.
func DeliverJobResultWithFiles(api messenger, uploader fileUploader, job *store.PendingJob) error {
	return deliverJobResult(api, uploader, job)
}

func deliverJobResult(api messenger, uploader fileUploader, job *store.PendingJob) error {
	paths, text := splitResponseFiles(job.Result)
	if text == "" && len(paths) == 0 {
		text = emptyResultMessage(job.State.Backend)
	}
	target := replyConversationForJob(job, readJobState(job.ID))
	if target.Provider != chat.ProviderSlack || target.ChannelID == "" {
		return fmt.Errorf("unsupported slack conversation: %+v", target)
	}
	if err := SendPlainText(api, target, text); err != nil {
		return err
	}
	if len(paths) > 0 && uploader != nil {
		if failed := sendTaggedFiles(uploader, target, paths); len(failed) > 0 {
			log.Printf("slack file upload failed for: %s", strings.Join(failed, ", "))
		}
	} else if len(paths) > 0 {
		// No uploader available — log a warning
		names := make([]string, 0, len(paths))
		for _, p := range paths {
			names = append(names, filepath.Base(p))
		}
		log.Printf("slack file upload skipped (no uploader): %s", strings.Join(names, ", "))
	}
	return nil
}

func SendImmediate(api messenger, conversation chat.ConversationRef, text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", true
	}

	job := store.PendingJob{
		ID:                store.NewJobID(),
		ConversationID:    conversation.ID(),
		ReplyConversation: conversation.ID(),
		Source:            string(chat.ProviderSlack),
		Status:            "ready",
		Result:            text,
	}
	if conversation.ThreadID != "" {
		writeJobState(job.ID, jobState{ReplyConversation: conversation})
	}
	store.WriteJob(job)
	ProcessJob(job, api, nil, nil)

	delivered := !store.JobExists(job.ID)
	if delivered {
		log.Printf("delivered immediate slack job %s", job.ID)
	} else {
		log.Printf("queued immediate slack job %s for retry", job.ID)
	}
	return job.ID, delivered
}

func ProcessJob(job store.PendingJob, api messenger, client *oneagent.Client, schedules *scheduler.Store) {
	dispatch.ProcessJob(&job, client, schedules, slackDispatchCallbacks(api, &job))
}

func ProcessJobWithUploader(job store.PendingJob, api messenger, uploader fileUploader, client *oneagent.Client, schedules *scheduler.Store) {
	dispatch.ProcessJob(&job, client, schedules, slackDispatchCallbacksWithUploader(api, uploader, &job))
}
