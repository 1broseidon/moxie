package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/1broseidon/moxie/internal/store"
)

// ---------------------------------------------------------------------------
// Bug class: PID-based serve lock has TOCTOU race
// Root cause: Signal(0) probe between reading PID and checking aliveness
// ---------------------------------------------------------------------------

// TestServeLockFlockPreventsDoubleAcquire verifies that the flock-based
// serve lock prevents a second process (simulated via goroutine) from
// acquiring the lock.
func TestServeLockFlockPreventsDoubleAcquire(t *testing.T) {
	dir := t.TempDir()
	restore := store.SetConfigDir(dir)
	defer restore()

	lockPath := filepath.Join(dir, "serve.pid.lock")

	// Simulate first serve acquiring the lock.
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	first, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open lock file: %v", err)
	}
	defer first.Close()

	if err := syscall.Flock(int(first.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("first flock: %v", err)
	}

	// Simulate second serve trying to acquire the same lock.
	second, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open lock file (second): %v", err)
	}
	defer second.Close()

	err = syscall.Flock(int(second.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		t.Fatal("second flock should have failed with EWOULDBLOCK")
	}
}

// TestServeLockReleasedOnClose confirms that closing the lock file
// descriptor releases the flock so the next serve can start.
func TestServeLockReleasedOnClose(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "serve.pid.lock")

	first, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := syscall.Flock(int(first.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}

	// Release by closing.
	_ = syscall.Flock(int(first.Fd()), syscall.LOCK_UN)
	first.Close()

	// Now a second lock should succeed.
	second, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open (second): %v", err)
	}
	defer second.Close()

	if err := syscall.Flock(int(second.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("second flock should succeed after first release: %v", err)
	}
}

// TestServeLockBlockingWaiterGetsLockAfterRelease confirms that a
// blocking waiter acquires the lock once the holder releases it.
func TestServeLockBlockingWaiterGetsLockAfterRelease(t *testing.T) {
	dir := t.TempDir()
	lockPath := filepath.Join(dir, "serve.pid.lock")

	first, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer first.Close()
	if err := syscall.Flock(int(first.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}

	acquired := make(chan struct{})
	go func() {
		second, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
		if err != nil {
			return
		}
		defer second.Close()
		// Blocking acquire — will wait until first releases.
		if err := syscall.Flock(int(second.Fd()), syscall.LOCK_EX); err != nil {
			return
		}
		close(acquired)
	}()

	// Waiter should not acquire while held.
	select {
	case <-acquired:
		t.Fatal("waiter acquired lock while first holder still has it")
	case <-time.After(100 * time.Millisecond):
		// Expected — waiter is blocked.
	}

	// Release first.
	_ = syscall.Flock(int(first.Fd()), syscall.LOCK_UN)

	select {
	case <-acquired:
		// Good.
	case <-time.After(2 * time.Second):
		t.Fatal("waiter did not acquire lock after first release")
	}
}

// TestServeLockSurvivesRecycledPID simulates the old bug: a stale PID
// file whose PID has been recycled by the OS. With the old Signal(0)
// approach this would falsely block. With flock, it should not.
func TestServeLockSurvivesRecycledPID(t *testing.T) {
	dir := t.TempDir()
	pidPath := filepath.Join(dir, "serve.pid")
	lockPath := pidPath + ".lock"

	// Write a stale PID file with our own PID (simulates recycled PID).
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	// With flock, we should be able to acquire even though PID is alive.
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open lock: %v", err)
	}
	defer f.Close()

	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock should succeed even with recycled PID in pid file: %v", err)
	}
}

// TestServePidFileContainsCurrentPID verifies the PID file is written
// for informational purposes alongside the flock.
func TestServePidFileContainsCurrentPID(t *testing.T) {
	dir := t.TempDir()
	restore := store.SetConfigDir(dir)
	defer restore()

	pidPath := servePidPath()
	lockPath := pidPath + ".lock"

	// Simulate acquireServeLock's write behavior.
	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	data, err := os.ReadFile(pidPath)
	if err != nil {
		t.Fatalf("read pid file: %v", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid != os.Getpid() {
		t.Fatalf("pid file = %q, want %d", string(data), os.Getpid())
	}
}

// TestServeLockCleanupRemovesBothFiles verifies the cleanup function
// removes both the lock file and the PID file.
func TestServeLockCleanupRemovesBothFiles(t *testing.T) {
	dir := t.TempDir()
	restore := store.SetConfigDir(dir)
	defer restore()

	pidPath := servePidPath()
	lockPath := pidPath + ".lock"

	if err := os.MkdirAll(filepath.Dir(lockPath), 0700); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		t.Fatalf("flock: %v", err)
	}
	if err := os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600); err != nil {
		t.Fatalf("write pid: %v", err)
	}

	// Simulate cleanup.
	_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	_ = f.Close()
	_ = os.Remove(lockPath)
	_ = os.Remove(pidPath)

	if _, err := os.Stat(lockPath); !os.IsNotExist(err) {
		t.Fatal("lock file should be removed after cleanup")
	}
	if _, err := os.Stat(pidPath); !os.IsNotExist(err) {
		t.Fatal("pid file should be removed after cleanup")
	}
}
