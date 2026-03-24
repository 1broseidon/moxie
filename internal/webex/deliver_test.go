package webex

import (
	"context"
	"strings"
	"testing"

	"github.com/1broseidon/moxie/internal/chat"
	"github.com/1broseidon/moxie/internal/store"
)

type fakeMessenger struct {
	room      Room
	sentRooms []string
	sentTexts []string
	deleted   []string
}

func (f *fakeMessenger) SendMessage(_ context.Context, roomID, text string) (Message, error) {
	f.sentRooms = append(f.sentRooms, roomID)
	f.sentTexts = append(f.sentTexts, text)
	return Message{ID: "msg-1", RoomID: roomID, Text: text}, nil
}

func (f *fakeMessenger) DeleteMessage(_ context.Context, messageID string) error {
	f.deleted = append(f.deleted, messageID)
	return nil
}

func (f *fakeMessenger) GetRoom(_ context.Context, roomID string) (Room, error) {
	room := f.room
	if room.ID == "" {
		room.ID = roomID
	}
	return room, nil
}

func useWebexStoreDir(t *testing.T) {
	t.Helper()
	restore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restore)
}

func TestSendPlainTextUsesDirectRoom(t *testing.T) {
	api := &fakeMessenger{room: Room{Type: "direct"}}
	err := SendPlainText(api, chat.ConversationRef{
		Provider:  chat.ProviderWebex,
		ChannelID: "room-1",
	}, "hello")
	if err != nil {
		t.Fatalf("SendPlainText(): %v", err)
	}
	if len(api.sentTexts) != 1 || api.sentTexts[0] != "hello" {
		t.Fatalf("sentTexts = %#v, want [hello]", api.sentTexts)
	}
	if len(api.sentRooms) != 1 || api.sentRooms[0] != "room-1" {
		t.Fatalf("sentRooms = %#v, want [room-1]", api.sentRooms)
	}
}

func TestSendPlainTextRejectsNonDirectRoom(t *testing.T) {
	api := &fakeMessenger{room: Room{Type: "group"}}
	err := SendPlainText(api, chat.ConversationRef{
		Provider:  chat.ProviderWebex,
		ChannelID: "space-1",
	}, "hello")
	if err == nil || !strings.Contains(err.Error(), "only 1:1 direct rooms are supported") {
		t.Fatalf("SendPlainText() err = %v", err)
	}
	if len(api.sentTexts) != 0 {
		t.Fatalf("sentTexts = %#v, want no sends", api.sentTexts)
	}
}

func TestDeliverJobResultUsesStoredReplyConversationAndStripsSendTags(t *testing.T) {
	useWebexStoreDir(t)

	api := &fakeMessenger{room: Room{Type: "direct"}}
	job := &store.PendingJob{
		ID:             "job-1",
		ConversationID: "webex:room-a",
		Result:         "done\n<send>/tmp/report.txt</send>",
	}
	writeJobState(job.ID, jobState{
		ReplyConversation: chat.ConversationRef{Provider: chat.ProviderWebex, ChannelID: "room-reply"},
	})

	if err := DeliverJobResult(api, job); err != nil {
		t.Fatalf("DeliverJobResult(): %v", err)
	}
	if len(api.sentRooms) != 1 || api.sentRooms[0] != "room-reply" {
		t.Fatalf("sentRooms = %#v, want reply room", api.sentRooms)
	}
	if len(api.sentTexts) != 1 {
		t.Fatalf("sentTexts len = %d, want 1", len(api.sentTexts))
	}
	if strings.Contains(api.sentTexts[0], "<send>") {
		t.Fatalf("send tag leaked: %q", api.sentTexts[0])
	}
	if !strings.Contains(api.sentTexts[0], "File delivery is not supported on Webex yet: report.txt") {
		t.Fatalf("text = %q, missing file notice", api.sentTexts[0])
	}
}

func TestUnseenMessagesReturnsChronologicalMessagesAfterCursor(t *testing.T) {
	messages := []Message{
		{ID: "3", Text: "third"},
		{ID: "2", Text: "second"},
		{ID: "1", Text: "first"},
	}
	got := unseenMessages(messages, "1")
	if len(got) != 2 {
		t.Fatalf("len(unseenMessages) = %d, want 2", len(got))
	}
	if got[0].ID != "2" || got[1].ID != "3" {
		t.Fatalf("unseenMessages() = %#v, want IDs [2 3]", got)
	}
}

func TestSenderAllowedWithoutACLAllowsAnyone(t *testing.T) {
	adapter := &Adapter{}
	if !adapter.senderAllowed(Message{PersonID: "user-1", PersonEmail: "user@example.com"}) {
		t.Fatal("expected sender to be allowed when no ACL is configured")
	}
}

func TestSenderAllowedByUserID(t *testing.T) {
	adapter := &Adapter{allowedUserIDs: map[string]struct{}{"user-1": {}}}
	if !adapter.senderAllowed(Message{PersonID: "user-1", PersonEmail: "other@example.com"}) {
		t.Fatal("expected sender to be allowed by user ID")
	}
	if adapter.senderAllowed(Message{PersonID: "user-2", PersonEmail: "other@example.com"}) {
		t.Fatal("expected sender to be denied when user ID is not on the ACL")
	}
}

func TestSenderAllowedByEmailCaseInsensitive(t *testing.T) {
	adapter := &Adapter{allowedEmails: map[string]struct{}{"user@example.com": {}}}
	if !adapter.senderAllowed(Message{PersonID: "user-2", PersonEmail: "User@Example.com"}) {
		t.Fatal("expected sender to be allowed by email")
	}
}
