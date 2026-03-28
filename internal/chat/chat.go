package chat

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/1broseidon/moxie/internal/dispatch"
	"github.com/1broseidon/moxie/internal/store"
	"github.com/1broseidon/oneagent"
)

type Provider string

const (
	ProviderTelegram Provider = "telegram"
	ProviderSlack    Provider = "slack"
	ProviderWebex    Provider = "webex"
)

type ConversationRef struct {
	Provider  Provider `json:"provider"`
	ChannelID string   `json:"channel_id"`
	ThreadID  string   `json:"thread_id,omitempty"`
}

type MessageRef struct {
	Conversation ConversationRef `json:"conversation"`
	MessageID    string          `json:"message_id"`
}

func (c ConversationRef) ID() string {
	if c.ThreadID != "" {
		return fmt.Sprintf("%s:%s:%s", c.Provider, c.ChannelID, c.ThreadID)
	}
	return fmt.Sprintf("%s:%s", c.Provider, c.ChannelID)
}

func ParseConversationID(id string) ConversationRef {
	parts := strings.SplitN(id, ":", 3)
	ref := ConversationRef{}
	if len(parts) > 0 {
		ref.Provider = Provider(parts[0])
	}
	if len(parts) > 1 {
		ref.ChannelID = parts[1]
	}
	if len(parts) > 2 {
		ref.ThreadID = parts[2]
	}
	return ref
}

type InboundMessage struct {
	EventID      string
	Source       string
	Conversation ConversationRef
	SenderName   string
	Text         string
	Prompt       string
	TempPath     string
}

type OutboundMessage struct {
	Conversation ConversationRef
	Text         string
	Files        []string
}

type Settings struct {
	Workspaces     map[string]string
	SaveWorkspaces func(map[string]string)
}

type HandleResult struct {
	ImmediateReply string
	Job            *store.PendingJob
}

func HandleInbound(msg InboundMessage, cfg Settings, defaultCWD string, st store.State, client *oneagent.Client) HandleResult {
	text := strings.TrimSpace(msg.Text)
	if strings.HasPrefix(text, "/") {
		base, _ := parseCommand(text)
		if !isSupportedCommand(base) {
			return HandleResult{ImmediateReply: "Unknown command. Try /new, /model, /cwd, /threads, or /compact."}
		}
		return HandleResult{ImmediateReply: HandleCommand(msg.Conversation.ID(), text, client, cfg, defaultCWD)}
	}

	prompt := strings.TrimSpace(msg.Prompt)
	if prompt == "" {
		prompt = text
	}
	if prompt == "" {
		return HandleResult{}
	}

	cwd := st.CWD
	if cwd == "" {
		cwd = defaultCWD
	}

	return HandleResult{
		Job: &store.PendingJob{
			ID:             store.NewJobID(),
			SourceEventID:  strings.TrimSpace(msg.EventID),
			Source:         strings.TrimSpace(msg.Source),
			ConversationID: msg.Conversation.ID(),
			Prompt:         prompt,
			CWD:            cwd,
			TempPath:       msg.TempPath,
			State:          st,
		},
	}
}

func HandleCommand(conversationID, text string, client *oneagent.Client, cfg Settings, defaultCWD string) string {
	base, arg := parseCommand(text)
	st := store.ReadConversationState(conversationID)

	switch base {
	case "new":
		return handleNew(conversationID, arg, st, client, cfg, defaultCWD)
	case "model":
		if arg == "" {
			b := client.Backends[st.Backend]
			model := st.Model
			if model == "" {
				model = b.DefaultModel
			}
			return fmt.Sprintf("Backend: %s\nModel: %s", st.Backend, model)
		}
		return switchModel(conversationID, arg, st, client)
	case "think":
		return handleThinking(conversationID, arg, st)
	case "cwd":
		return handleCWD(conversationID, arg, st, cfg, client, defaultCWD)
	case "threads":
		return handleThreads(conversationID, arg, st, client)
	case "compact":
		if err := client.CompactThread(st.ThreadID, st.Backend); err != nil {
			return "Compact failed: " + err.Error()
		}
		return "Thread compacted."
	}
	return ""
}

func parseCommand(text string) (base, arg string) {
	cmd := strings.TrimPrefix(text, "/")
	parts := strings.SplitN(cmd, " ", 2)
	base = parts[0]
	if idx := strings.Index(base, "@"); idx >= 0 {
		base = base[:idx]
	}
	if len(parts) > 1 {
		arg = strings.TrimSpace(parts[1])
	}
	return
}

func isSupportedCommand(name string) bool {
	switch name {
	case "new", "model", "think", "cwd", "threads", "compact":
		return true
	default:
		return false
	}
}

func handleNew(conversationID, arg string, st store.State, client *oneagent.Client, cfg Settings, defaultCWD string) string {
	for _, word := range strings.Fields(arg) {
		if _, ok := client.Backends[word]; ok {
			st.Backend = word
			st.Model = ""
		} else if path, ok := cfg.Workspaces[word]; ok {
			resolved, err := resolveDir(path)
			if err != nil {
				return fmt.Sprintf("Workspace %s is invalid: %v", word, err)
			}
			if path != resolved {
				cfg.Workspaces[word] = resolved
				if cfg.SaveWorkspaces != nil {
					cfg.SaveWorkspaces(cfg.Workspaces)
				}
			}
			st.CWD = resolved
		} else {
			return fmt.Sprintf("Unknown backend or workspace: %s", word)
		}
	}
	existing := make(map[string]bool)
	if ids, err := client.ListThreads(); err == nil {
		for _, id := range ids {
			existing[id] = true
		}
	}
	st.ThreadID = NewThreadName(existing)
	store.WriteConversationState(conversationID, st)
	cwd := st.CWD
	if cwd == "" {
		cwd = defaultCWD
		if cwd == "" {
			cwd = "(default workspace)"
		}
	}
	return fmt.Sprintf("New %s session in %s.", st.Backend, cwd)
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return path
		}
		return filepath.Join(home, path[2:])
	}
	return path
}

func resolveDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	resolved := expandHome(path)
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	info, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("access path: %w", err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("not a directory: %s", abs)
	}
	return abs, nil
}

func resetNativeSession(client *oneagent.Client, st store.State) {
	if !dispatch.ClearNativeSession(client, st) {
		return
	}
}

func handleCWD(conversationID, arg string, st store.State, cfg Settings, client *oneagent.Client, defaultCWD string) string {
	if arg == "" {
		cwd := st.CWD
		if cwd == "" {
			cwd = defaultCWD
			if cwd == "" {
				cwd = "(default workspace)"
			}
		}
		var buf strings.Builder
		fmt.Fprintf(&buf, "CWD: %s\n\nWorkspaces:\n", cwd)
		for name, path := range cfg.Workspaces {
			fmt.Fprintf(&buf, "  %s → %s\n", name, path)
		}
		if len(cfg.Workspaces) == 0 {
			buf.WriteString("  (none)\n")
		}
		buf.WriteString("\n/cwd <name> to switch\n/cwd <name> <path> to save")
		return buf.String()
	}
	parts := strings.SplitN(arg, " ", 2)
	if len(parts) == 2 {
		name := strings.TrimSpace(parts[0])
		if name == "" {
			return "Workspace name cannot be empty."
		}
		resolved, err := resolveDir(parts[1])
		if err != nil {
			return "Invalid workspace path: " + err.Error()
		}
		cfg.Workspaces[name] = resolved
		if cfg.SaveWorkspaces != nil {
			cfg.SaveWorkspaces(cfg.Workspaces)
		}
		return fmt.Sprintf("Workspace %s → %s", name, resolved)
	}
	name := strings.TrimSpace(parts[0])
	if path, ok := cfg.Workspaces[name]; ok {
		resolved, err := resolveDir(path)
		if err != nil {
			return fmt.Sprintf("Workspace %s is invalid: %v", name, err)
		}
		if path != resolved {
			cfg.Workspaces[name] = resolved
			if cfg.SaveWorkspaces != nil {
				cfg.SaveWorkspaces(cfg.Workspaces)
			}
		}
		resetNativeSession(client, st)
		st.CWD = resolved
		store.WriteConversationState(conversationID, st)
		return fmt.Sprintf("CWD: %s (%s)", name, st.CWD)
	}
	return "Unknown workspace: " + name + "\n/cwd <name> <path> to create"
}

func switchModel(conversationID, arg string, st store.State, client *oneagent.Client) string {
	next := st
	parts := strings.SplitN(arg, " ", 2)
	if _, ok := client.Backends[parts[0]]; ok {
		next.Backend = parts[0]
		next.Model = ""
		if len(parts) > 1 {
			next.Model = parts[1]
		}
		if next != st {
			resetNativeSession(client, next)
		}
		store.WriteConversationState(conversationID, next)
		if next.Model != "" {
			return fmt.Sprintf("Switched to %s (%s)", next.Backend, next.Model)
		}
		return "Switched to " + next.Backend
	}
	next.Model = arg
	if next != st {
		resetNativeSession(client, next)
	}
	store.WriteConversationState(conversationID, next)
	return "Model set to " + arg
}

func handleThinking(conversationID, arg string, st store.State) string {
	if arg == "" {
		level := st.Thinking
		if level == "" {
			level = "off"
		}
		return "Thinking: " + level
	}
	if arg == "off" || arg == "none" {
		st.Thinking = ""
	} else {
		st.Thinking = arg
	}
	store.WriteConversationState(conversationID, st)
	if st.Thinking == "" {
		return "Thinking disabled."
	}
	return "Thinking set to " + st.Thinking
}

func handleThreads(conversationID, arg string, st store.State, client *oneagent.Client) string {
	if arg != "" {
		st.ThreadID = arg
		store.WriteConversationState(conversationID, st)
		return "Switched to thread " + arg
	}
	ids, err := client.ListThreads()
	if err != nil || len(ids) == 0 {
		return "No saved threads."
	}
	var buf strings.Builder
	for _, id := range ids {
		marker := "  "
		if id == st.ThreadID {
			marker = "> "
		}
		fmt.Fprintf(&buf, "%s%s\n", marker, id)
	}
	buf.WriteString("\n/threads <name> to switch")
	return buf.String()
}

// SplitText breaks text into chunks of at most limit bytes, preferring
// paragraph boundaries (\n\n), then line boundaries (\n), then spaces.
func SplitText(text string, limit int) []string {
	if len(text) <= limit {
		return []string{text}
	}
	var chunks []string
	for len(text) > limit {
		cut := findSplitPoint(text, limit)
		chunks = append(chunks, text[:cut])
		text = strings.TrimLeft(text[cut:], "\n ")
	}
	if text != "" {
		chunks = append(chunks, text)
	}
	return chunks
}

func findSplitPoint(text string, limit int) int {
	window := text[:limit]
	if cut := strings.LastIndex(window, "\n\n"); cut > limit/2 {
		return cut
	}
	if cut := strings.LastIndex(window, "\n"); cut > limit/2 {
		return cut
	}
	if cut := strings.LastIndex(window, " "); cut > 0 {
		return cut
	}
	return limit
}

// SubagentFormattingRules returns transport-specific formatting instructions
// for a subagent that will deliver its result directly to the user.
func SubagentFormattingRules(provider Provider) string {
	switch provider {
	case ProviderTelegram:
		return `Format your response using Telegram HTML.
Supported tags: <b>bold</b>, <i>italic</i>, <u>underline</u>, <s>strikethrough</s>, <code>inline code</code>, <pre>code block</pre>, <a href="url">link</a>.
No markdown. No unsupported tags. Keep the reply concise.`
	case ProviderSlack:
		return `Format your response using Slack mrkdwn.
No HTML. Use: *bold*, _italic_, ~strikethrough~, inline code, fenced code blocks, lists, and plain links.
Keep the reply concise and readable in Slack.`
	case ProviderWebex:
		return `Format your response using simple Webex-friendly markdown.
No HTML. Use: **bold**, *italic*, inline code, fenced code blocks, lists, and plain links.
Keep the reply concise and readable in Webex.`
	default:
		return "Keep your response concise and well-formatted."
	}
}
