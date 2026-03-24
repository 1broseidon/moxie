//go:build windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/1broseidon/moxie/internal/store"
)

func acquireServeLock() func() {
	pidPath := servePidPath()

	// Windows fallback: PID file + process existence check.
	// syscall.Flock is not available on Windows.
	if data, err := os.ReadFile(pidPath); err == nil {
		pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err == nil && pid > 0 {
			if proc, err := os.FindProcess(pid); err == nil {
				if err := proc.Signal(os.Signal(nil)); err == nil {
					fatal("moxie serve is already running (pid %d). Kill it first or remove %s", pid, pidPath)
				}
			}
		}
	}
	if err := os.MkdirAll(filepath.Dir(pidPath), 0700); err != nil {
		fatal("cannot create pid directory: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		fatal("cannot write pid file: %v", err)
	}
	return func() { os.Remove(pidPath) }
}

func servePidPath() string {
	return filepath.Join(store.ConfigDir(), "serve.pid")
}
