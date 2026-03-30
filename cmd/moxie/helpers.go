package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			log.Printf("error: cannot expand ~: %v", err)
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

func formatAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

func parseTransportFlag(startIdx int) string {
	for i := startIdx; i < len(os.Args); i++ {
		if os.Args[i] == "--transport" && i+1 < len(os.Args) {
			return strings.TrimSpace(os.Args[i+1])
		}
	}
	return ""
}

func joinArgsExcludingTransport(startIdx int) string {
	args := make([]string, 0, len(os.Args)-startIdx)
	for i := startIdx; i < len(os.Args); i++ {
		if os.Args[i] == "--transport" && i+1 < len(os.Args) {
			i++
			continue
		}
		args = append(args, os.Args[i])
	}
	return strings.Join(args, " ")
}
