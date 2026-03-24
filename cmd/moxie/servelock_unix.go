//go:build !windows

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/1broseidon/moxie/internal/store"
)

func acquireServeLock() func() {
	pidPath := servePidPath()
	lockPath := pidPath + ".lock"

	// Use flock for a race-free mutual exclusion check. The PID file
	// is kept for informational purposes (external tools, debugging).
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		fatal("cannot create lock directory: %v", err)
	}
	lockFile, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		fatal("cannot open lock file: %v", err)
	}
	if err := syscall.Flock(int(lockFile.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		// Read the PID file for a better error message.
		existingPID := "unknown"
		if data, readErr := os.ReadFile(pidPath); readErr == nil {
			existingPID = strings.TrimSpace(string(data))
		}
		_ = lockFile.Close()
		fatal("moxie serve is already running (pid %s). Kill it first or remove %s", existingPID, pidPath)
	}

	// Write PID for informational use.
	_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600)

	return func() {
		_ = syscall.Flock(int(lockFile.Fd()), syscall.LOCK_UN)
		_ = lockFile.Close()
		_ = os.Remove(lockPath)
		_ = os.Remove(pidPath)
	}
}

func servePidPath() string {
	return filepath.Join(store.ConfigDir(), "serve.pid")
}
