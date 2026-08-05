//go:build unix

package dbtest

import (
	"os"
	"syscall"
)

// fileLock is an OS-level advisory lock held by one process at a time. It
// serializes compose bring-up with syscall.Flock, so of the several
// `go test ./...` binaries that reach ensureComposeUp at once, only one
// actually runs `docker compose up` while the rest block and then proceed
// against the containers it started.
//
// This package builds only on unix (see the //go:build unix constraint on
// every file in this package), specifically because this lock has no
// no-op-safe portable fallback: a platform with no syscall.Flock would need
// either a real alternative locking primitive or an unguarded bring-up,
// and an unguarded bring-up is not safe here. This package used to ship a
// no-op fileLock for non-unix platforms, but a no-op lock lets two
// `go test ./...` binaries race the same `docker compose up`, and a
// container-name conflict or a "network already exists" error from that
// race would fail the test loudly and blame a broken compose file that was
// never broken. Dropping non-unix support outright removes that failure
// mode instead of papering over it with a lock that does not lock.
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
