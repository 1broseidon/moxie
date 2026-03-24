package webex

import (
	"context"
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
)

const webexMessageChunkSize = 6500

var sendTagPattern = regexp.MustCompile(`(?s)<send>\s*(.*?)\s*</send>`)

type messenger interface {
	SendMessage(ctx context.Context, roomID, text string) (Message, error)
	SendMessageWithFile(ctx context.Context, roomID, text, filePath string) (Message, error)
	DeleteMessage(ctx context.Context, messageID string) error
	GetRoom(ctx context.Context, roomID string) (Room, error)
	DownloadFile(ctx context.Context, fileURL string) (string, error)
}

type Messenger = messenger

// verifiedDirectRooms caches room IDs that have been confirmed as direct (1:1).
// Webex room types don't change, so this avoids redundant GetRoom API calls.
var verifiedDirectRooms = make(map[string]struct{})

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

func replyConversationForJob(job *store.PendingJob, st jobState) chat.ConversationRef {
	if job != nil {
		target := conversationFromID(job.ReplyConversation)
		if target.Provider == chat.ProviderWebex && target.ChannelID != "" {
			return target
		}
	}
	if st.ReplyConversation.Provider == chat.ProviderWebex && st.ReplyConversation.ChannelID != "" {
		return st.ReplyConversation
	}
	return conversationFromID(job.ConversationID)
}

func ensureDirectConversation(api messenger, conversation chat.ConversationRef) error {
	if conversation.Provider != chat.ProviderWebex || strings.TrimSpace(conversation.ChannelID) == "" {
		return fmt.Errorf("unsupported webex conversation: %+v", conversation)
	}
	roomID := conversation.ChannelID
	if _, ok := verifiedDirectRooms[roomID]; ok {
		return nil
	}
	room, err := api.GetRoom(context.Background(), roomID)
	if err != nil {
		return err
	}
	if strings.TrimSpace(room.Type) != "direct" {
		return fmt.Errorf("webex room %s is type %q; only 1:1 direct rooms are supported", roomID, room.Type)
	}
	verifiedDirectRooms[roomID] = struct{}{}
	return nil
}

func compactText(text string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(text)), " ")
}

func renderActivityText(activity string) string {
	activity = compactText(activity)
	if activity == "" {
		return "Working..."
	}

	words := strings.Fields(activity)
	verb := strings.ToLower(words[0])
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

	detail := activity
	runes := []rune(detail)
	if len(runes) > 140 {
		detail = string(runes[:140]) + "…"
	}
	return summary + "\n" + detail
}

// show sends a one-time "Working..." status message. Unlike Slack, Webex does
// not support message editing, so subsequent calls are no-ops to avoid spam.
func (s *runningStatus) show(activity string) {
	if s.api == nil || s.job == nil {
		return
	}
	if s.st.StatusMessage.MessageID != "" {
		return // already sent; Webex has no edit API
	}
	target := replyConversationForJob(s.job, s.st)
	if err := ensureDirectConversation(s.api, target); err != nil {
		log.Printf("webex status send skipped: %v", err)
		return
	}
	msg, err := s.api.SendMessage(context.Background(), target.ChannelID, renderActivityText(activity))
	if err != nil {
		log.Printf("webex status send error: %v", err)
		return
	}
	s.st.ReplyConversation = target
	s.st.StatusMessage = chat.MessageRef{
		Conversation: target,
		MessageID:    msg.ID,
	}
	writeJobState(s.job.ID, s.st)
}

func (s *runningStatus) clear() {
	if s.api == nil {
		removeJobState(s.job.ID)
		return
	}
	if s.st.StatusMessage.MessageID != "" {
		if err := s.api.DeleteMessage(context.Background(), s.st.StatusMessage.MessageID); err != nil {
			log.Printf("webex status delete error for %s: %v", s.st.StatusMessage.MessageID, err)
		}
	}
	removeJobState(s.job.ID)
}

func SendPlainText(api messenger, conversation chat.ConversationRef, text string) error {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if err := ensureDirectConversation(api, conversation); err != nil {
		return err
	}

	sentAny := false
	var firstErr error
	for _, chunk := range chat.SplitText(text, webexMessageChunkSize) {
		if _, err := api.SendMessage(context.Background(), conversation.ChannelID, chunk); err != nil {
			log.Printf("webex send error: %v", err)
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

func sendTaggedFiles(api messenger, conversation chat.ConversationRef, paths []string) []string {
	var failed []string
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if _, err := api.SendMessageWithFile(context.Background(), conversation.ChannelID, "", path); err != nil {
			log.Printf("webex file send error for %s: %v", filepath.Base(path), err)
			failed = append(failed, filepath.Base(path))
		}
	}
	return failed
}

func emptyResultMessage(backend string) string {
	backend = strings.TrimSpace(backend)
	if backend == "" {
		return "Backend returned an empty response."
	}
	return fmt.Sprintf("Backend %s returned an empty response.", backend)
}

func DeliverJobResult(api messenger, job *store.PendingJob) error {
	paths, text := splitResponseFiles(job.Result)
	if text == "" && len(paths) == 0 {
		text = emptyResultMessage(job.State.Backend)
	}
	target := replyConversationForJob(job, readJobState(job.ID))
	if target.Provider != chat.ProviderWebex || target.ChannelID == "" {
		return fmt.Errorf("unsupported webex conversation: %+v", target)
	}
	if err := SendPlainText(api, target, text); err != nil {
		return err
	}
	if len(paths) > 0 {
		if failed := sendTaggedFiles(api, target, paths); len(failed) > 0 {
			log.Printf("webex file delivery failed for: %s", strings.Join(failed, ", "))
		}
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
		Source:            string(chat.ProviderWebex),
		Status:            "ready",
		Result:            text,
	}
	store.WriteJob(job)
	ProcessJob(job, api, nil, nil)

	delivered := !store.JobExists(job.ID)
	if delivered {
		log.Printf("delivered immediate webex job %s", job.ID)
	} else {
		log.Printf("queued immediate webex job %s for retry", job.ID)
	}
	return job.ID, delivered
}

func ProcessJob(job store.PendingJob, api messenger, client *oneagent.Client, schedules *scheduler.Store) {
	dispatch.ProcessJob(&job, client, schedules, webexDispatchCallbacks(api, &job))
}
