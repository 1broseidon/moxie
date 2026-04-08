package prompt

import (
	"strings"
	"testing"

	"github.com/1broseidon/oneagent"
)

func TestCoreSystemPromptWorkflowGuidance(t *testing.T) {
	tests := []struct {
		name string
		want string
	}{
		{name: "capabilities include workflow", want: "moxie schedule, moxie subagent, moxie workflow, moxie result, moxie memory, and moxie service"},
		{name: "voice file is documented", want: "Moxie keeps an editable style memory at ~/.config/moxie/VOICE.md."},
		{name: "voice updates are allowed", want: "When the user asks to change how Moxie should behave in future replies, update VOICE.md."},
		{name: "subagent remains default", want: "Use moxie subagent by default when delegating work to another backend."},
		{name: "fanout is narrow exception", want: "Use moxie workflow run fanout only for bounded parallel work"},
		{name: "workflows are internal detail", want: "Treat workflows as an internal implementation detail unless the user explicitly asks about workflow behavior."},
		{name: "no polling by default", want: "do not poll status, watch logs, or inspect progress unless the user asks or the run fails and needs intervention"},
		{name: "quiet execution", want: "Prefer quiet background execution: acknowledge launch briefly, then wait for the final result."},
		{name: "no interdependent fanout", want: "Do not use fanout for sequential or interdependent subtasks."},
		{name: "no nesting", want: "Do not nest workflows."},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !strings.Contains(CoreSystemPrompt, tt.want) {
				t.Fatalf("CoreSystemPrompt missing %q", tt.want)
			}
		})
	}
}

func TestApplySystemPromptsInjectsWorkflowGuidance(t *testing.T) {
	backends := map[string]oneagent.Backend{
		"claude": {SystemPrompt: "base"},
		"pi":     {},
	}

	ApplySystemPrompts(backends, "transport")

	for name, backend := range backends {
		for _, want := range []string{
			"moxie workflow",
			"Use moxie subagent by default",
			"Use moxie workflow run fanout only for bounded parallel work",
			"Do not nest workflows.",
		} {
			if !strings.Contains(backend.SystemPrompt, want) {
				t.Fatalf("backend %s missing %q in system prompt: %q", name, want, backend.SystemPrompt)
			}
		}
	}
}
