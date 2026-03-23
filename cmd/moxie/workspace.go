package main

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/1broseidon/moxie/internal/store"
)

func platformDefaultWorkspaceDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home dir: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Moxie", "workspace"), nil
	case "windows":
		base := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
		if base == "" {
			base = filepath.Join(home, "AppData", "Local")
		}
		return filepath.Join(base, "Moxie", "workspace"), nil
	default:
		return filepath.Join(home, ".local", "share", "moxie", "workspace"), nil
	}
}

func resolveOrCreateDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("path cannot be empty")
	}
	resolved := expandHome(path)
	abs, err := filepath.Abs(resolved)
	if err != nil {
		return "", fmt.Errorf("resolve path: %w", err)
	}
	if err := os.MkdirAll(abs, 0o755); err != nil {
		return "", fmt.Errorf("create path: %w", err)
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

func configuredOrPlatformDefaultCWD(cfg store.Config) (string, error) {
	if trimmed := strings.TrimSpace(cfg.DefaultCWD); trimmed != "" {
		return resolveOrCreateDir(trimmed)
	}
	path, err := platformDefaultWorkspaceDir()
	if err != nil {
		return "", err
	}
	return resolveOrCreateDir(path)
}

func isMeaningfulWorkingDir(path string) bool {
	trimmed := strings.TrimSpace(path)
	if trimmed == "" {
		return false
	}
	clean := filepath.Clean(trimmed)
	return filepath.Dir(clean) != clean
}

func resolveServeDefaultCWD(cfg store.Config, explicit string) (string, error) {
	if trimmed := strings.TrimSpace(explicit); trimmed != "" {
		return resolveDir(trimmed)
	}
	if cwd, err := logicalWorkingDir(); err == nil && isMeaningfulWorkingDir(cwd) {
		return cwd, nil
	}
	return configuredOrPlatformDefaultCWD(cfg)
}

func logicalWorkingDir() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	pwd := strings.TrimSpace(os.Getenv("PWD"))
	if pwd == "" {
		return cwd, nil
	}
	logical, err := resolveDir(pwd)
	if err != nil {
		return cwd, nil
	}
	logicalInfo, err := os.Stat(logical)
	if err != nil {
		return cwd, nil
	}
	cwdInfo, err := os.Stat(cwd)
	if err != nil {
		return cwd, nil
	}
	if os.SameFile(logicalInfo, cwdInfo) {
		return logical, nil
	}
	return cwd, nil
}
