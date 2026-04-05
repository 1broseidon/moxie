package main

import (
	"bufio"
	"fmt"
	"os"
	"runtime"
	"strconv"

	"github.com/1broseidon/moxie/internal/prompt"
	"github.com/1broseidon/moxie/internal/store"
)

func cmdInit() {
	dir := store.ConfigDir()
	if err := os.MkdirAll(dir, 0700); err != nil {
		fatal("failed to create config dir: %v", err)
	}

	reader := bufio.NewReader(os.Stdin)

	token := promptRequiredLine(reader, "Bot token: ")
	chatIDText := promptRequiredLine(reader, "Chat ID: ")
	chatID, err := strconv.ParseInt(chatIDText, 10, 64)
	if err != nil {
		fatal("invalid chat ID: %s", chatIDText)
	}

	if token == "" {
		fatal("token cannot be empty")
	}
	if chatID == 0 {
		fatal("chat ID cannot be zero")
	}

	defaultWorkspace, err := platformDefaultWorkspaceDir()
	if err != nil {
		fatal("failed to determine default workspace: %v", err)
	}
	workspaceInput := promptLine(reader, fmt.Sprintf("Default workspace [%s]: ", defaultWorkspace), defaultWorkspace)
	defaultCWD, err := resolveOrCreateDir(workspaceInput)
	if err != nil {
		fatal("invalid default workspace: %v", err)
	}

	cfg := store.Config{
		Channels: map[string]store.ChannelConfig{
			"telegram": {
				Provider:  "telegram",
				Token:     token,
				ChannelID: strconv.FormatInt(chatID, 10),
			},
		},
		Workspaces: map[string]string{},
		DefaultCWD: defaultCWD,
	}
	store.SaveConfig(cfg)
	if err := prompt.EnsureVoiceFile(); err != nil {
		fatal("failed to initialize VOICE.md: %v", err)
	}
	path := store.ConfigFile("config.json")
	fmt.Printf("Config saved to %s\n", path)
	fmt.Printf("Default workspace: %s\n", defaultCWD)
	fmt.Printf("VOICE.md: %s\n", prompt.VoicePath())

	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return
	}
	if !promptYesNo(reader, "Install and start as a background service? [y/N]: ", false) {
		return
	}

	path, err = installService(serviceInstallOptions{})
	if err != nil {
		fatal("service install failed: %v", err)
	}
	fmt.Printf("Service definition written to %s\n", path)
	cmdServiceControl("start")
}
