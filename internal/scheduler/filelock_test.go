package scheduler

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreMutationLockBlocksAcrossStoreInstances(t *testing.T) {
	path := filepath.Join(t.TempDir(), "schedules.json")
	first := NewStore(path, time.Local)
	second := NewStore(path, time.Local)

	unlock, err := first.lockMutationFile()
	if err != nil {
		t.Fatalf("lockMutationFile() err = %v", err)
	}

	done := make(chan struct{})
	errCh := make(chan error, 1)
	go func() {
		release, err := second.lockMutationFile()
		if err != nil {
			errCh <- err
			return
		}
		release()
		close(done)
	}()

	select {
	case err := <-errCh:
		t.Fatalf("second lock err = %v", err)
	case <-done:
		t.Fatal("second store should block while the first store holds the mutation lock")
	case <-time.After(150 * time.Millisecond):
	}

	unlock()

	select {
	case err := <-errCh:
		t.Fatalf("second lock err = %v", err)
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("second store did not acquire the mutation lock after release")
	}
}
