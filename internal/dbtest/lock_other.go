//go:build !unix

package dbtest

// fileLock degrades to a no-op on a platform with no syscall.Flock; see
// lock_unix.go. A concurrent `go test ./...` on such a platform can race
// two `docker compose up` invocations, but that race is harmless rather
// than corrupting: compose bring-up is idempotent, so both converge on the
// same containers. It is unguarded rather than unsafe.
type fileLock struct{}

func acquireLock(string) (*fileLock, error) {
	return &fileLock{}, nil
}

func (l *fileLock) release() error {
	return nil
}
