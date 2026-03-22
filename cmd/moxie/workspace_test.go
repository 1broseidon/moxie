package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/1broseidon/moxie/internal/store"
)

func TestConfiguredOrPlatformDefaultCWDCreatesConfiguredDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "workspace")
	got, err := configuredOrPlatformDefaultCWD(store.Config{DefaultCWD: path})
	if err != nil {
		t.Fatalf("configuredOrPlatformDefaultCWD() err = %v", err)
	}
	if got != path {
		t.Fatalf("configuredOrPlatformDefaultCWD() = %q, want %q", got, path)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("expected created workspace dir, stat err = %v", err)
	}
}

func TestResolveServeDefaultCWDPrefersCurrentWorkingDir(t *testing.T) {
	cwd := t.TempDir()
	prev, err := os.Getwd()
	if err != nil {
		t.Fatalf("Getwd(): %v", err)
	}
	if err := os.Chdir(cwd); err != nil {
		t.Fatalf("Chdir(): %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(prev)
	})

	cfg := store.Config{DefaultCWD: filepath.Join(t.TempDir(), "configured")}
	got, err := resolveServeDefaultCWD(cfg, "")
	if err != nil {
		t.Fatalf("resolveServeDefaultCWD() err = %v", err)
	}
	if got != cwd {
		t.Fatalf("resolveServeDefaultCWD() = %q, want current dir %q", got, cwd)
	}
}

func TestServiceInstallWorkingDirectoryUsesConfiguredDefault(t *testing.T) {
	restore := store.SetConfigDir(t.TempDir())
	t.Cleanup(restore)

	path := filepath.Join(t.TempDir(), "workspace")
	store.SaveConfig(store.Config{
		Channels: map[string]store.ChannelConfig{
			"telegram": {
				Provider:  "telegram",
				Token:     "token",
				ChannelID: "123",
			},
		},
		DefaultCWD: path,
	})

	got, err := serviceInstallWorkingDirectory(serviceInstallOptions{})
	if err != nil {
		t.Fatalf("serviceInstallWorkingDirectory() err = %v", err)
	}
	if got != path {
		t.Fatalf("serviceInstallWorkingDirectory() = %q, want %q", got, path)
	}
	if info, err := os.Stat(path); err != nil || !info.IsDir() {
		t.Fatalf("expected created workspace dir, stat err = %v", err)
	}
}
