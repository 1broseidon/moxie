package slack

import (
	"fmt"
	"log"
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
	detail := ""
	if len(words) > 1 {
		detail = strings.Join(words[1:], " ")
	}

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
		detail = activity
	default:
		detail = activity
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
	for len(text) > 0 {
		chunk := text
		if len(chunk) > slackMessageChunkSize {
			cut := strings.LastIndex(chunk[:slackMessageChunkSize], "\n")
			if cut <= 0 {
				cut = slackMessageChunkSize
			}
			chunk = text[:cut]
			text = text[cut:]
		} else {
			text = ""
		}
		text = strings.TrimPrefix(text, "\n")

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

func fileUploadNotice(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	names := make([]string, 0, len(paths))
	for _, path := range paths {
		names = append(names, filepath.Base(path))
	}
	return fmt.Sprintf("File delivery is not supported on Slack yet: %s", strings.Join(names, ", "))
}

func replyConversationForJob(job *store.PendingJob, st jobState) chat.ConversationRef {
	if st.ReplyConversation.Provider == chat.ProviderSlack && st.ReplyConversation.ChannelID != "" {
		return st.ReplyConversation
	}
	return conversationFromID(job.ConversationID)
}

func DeliverJobResult(api messenger, job *store.PendingJob) error {
	paths, text := splitResponseFiles(job.Result)
	notice := fileUploadNotice(paths)
	if notice != "" {
		if text != "" {
			text += "\n\n" + notice
		} else {
			text = notice
		}
	}
	if text == "" && len(paths) == 0 {
		text = "Done - nothing to report."
	}
	target := replyConversationForJob(job, readJobState(job.ID))
	if target.Provider != chat.ProviderSlack || target.ChannelID == "" {
		return fmt.Errorf("unsupported slack conversation: %+v", target)
	}
	return SendPlainText(api, target, text)
}

func SendImmediate(api messenger, conversation chat.ConversationRef, text string) (string, bool) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", true
	}

	job := store.PendingJob{
		ID:             store.NewJobID(),
		ConversationID: conversation.ID(),
		Source:         string(chat.ProviderSlack),
		Status:         "ready",
		Result:         text,
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
