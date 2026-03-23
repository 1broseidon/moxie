//go:build windows

package scheduler

import "os"

// TODO: add a real cross-process lock for Windows schedule store mutations.
func acquireFileLock(path string) (*os.File, error) {
	return nil, nil
}

func releaseFileLock(*os.File) error {
	return nil
}
