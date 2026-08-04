//go:build unix

package dbtest

import (
	"os"
	"syscall"
)

// fileLock is an OS-level advisory lock held by one process at a time. On
// Unix it serializes compose bring-up with syscall.Flock, so of the several
// `go test ./...` binaries that reach ensureComposeUp at once, only one
// actually runs `docker compose up` while the rest block and then proceed
// against the containers it started. See lock_other.go for platforms with
// no flock.
type fileLock struct {
	file *os.File
}

// acquireLock opens (creating if needed) the file at path and blocks until
// this process holds an exclusive advisory lock on it.
func acquireLock(path string) (*fileLock, error) {
	file, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(file.Fd()), syscall.LOCK_EX); err != nil {
		file.Close()
		return nil, err
	}
	return &fileLock{file: file}, nil
}

// release unlocks and closes the underlying file.
func (l *fileLock) release() error {
	defer l.file.Close()
	return syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
}
