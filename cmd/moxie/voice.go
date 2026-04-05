package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/1broseidon/moxie/internal/prompt"
)

func voiceUsage() {
	fmt.Println(`moxie voice — manage Moxie's adjustable style memory

Usage:
  moxie voice path
  moxie voice show
  moxie voice reset

Notes:
  VOICE.md lives at ~/.config/moxie/VOICE.md
  Edits take effect on the next agent run; no service restart required`)
}

func cmdVoice() {
	if len(os.Args) < 3 {
		voiceUsage()
		return
	}

	switch os.Args[2] {
	case "path":
		fmt.Println(prompt.VoicePath())
	case "show":
		voice, err := prompt.LoadVoice()
		if err != nil {
			fatal("load VOICE.md: %v", err)
		}
		fmt.Print(voice)
		if !strings.HasSuffix(voice, "\n") {
			fmt.Println()
		}
	case "reset":
		if err := prompt.EnsureVoiceFile(); err != nil {
			fatal("initialize VOICE.md: %v", err)
		}
		if err := os.WriteFile(prompt.VoicePath(), []byte(prompt.DefaultVoice()), 0o600); err != nil {
			fatal("reset VOICE.md: %v", err)
		}
		fmt.Printf("VOICE.md reset: %s\n", prompt.VoicePath())
	default:
		voiceUsage()
	}
}
