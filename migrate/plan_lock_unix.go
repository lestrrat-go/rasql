//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package migrate

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type unixPlanLock struct{ file *os.File }

func nativePlanFileIdentity(path string) (string, []byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return "", nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || stat.Nlink != 1 {
		return "", nil, errors.New("database file must be regular and have one link")
	}
	identity := make([]byte, 16)
	binary.BigEndian.PutUint64(identity[:8], uint64(stat.Dev))
	binary.BigEndian.PutUint64(identity[8:], stat.Ino)
	return "unix", identity, nil
}

func acquireNativePlanSidecar(ctx context.Context, path string) (ChangePlanLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0o600)
	if err != nil {
		return nil, &ChangePlanLockError{stage: ChangePlanLockValidate, cause: err}
	}
	file := os.NewFile(uintptr(fd), path)
	closeFile := func() { _ = file.Close() }
	var opened unix.Stat_t
	if err := unix.Fstat(fd, &opened); err != nil {
		closeFile()
		return nil, &ChangePlanLockError{stage: ChangePlanLockValidate, cause: err}
	}
	var named unix.Stat_t
	if err := unix.Lstat(path, &named); err != nil {
		closeFile()
		return nil, &ChangePlanLockError{stage: ChangePlanLockValidate, cause: err}
	}
	if opened.Dev != named.Dev || opened.Ino != named.Ino || opened.Nlink != 1 || named.Nlink != 1 ||
		opened.Mode&unix.S_IFMT != unix.S_IFREG || named.Mode&unix.S_IFMT != unix.S_IFREG || opened.Mode&0o7777 != 0o600 {
		closeFile()
		return nil, &ChangePlanLockError{stage: ChangePlanLockValidate, cause: errors.New("unsafe plan lock sidecar")}
	}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		if err := unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return &unixPlanLock{file: file}, nil
		} else if !errors.Is(err, unix.EWOULDBLOCK) && !errors.Is(err, unix.EAGAIN) {
			closeFile()
			return nil, err
		}
		select {
		case <-ctx.Done():
			closeFile()
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func (l *unixPlanLock) Release(ctx context.Context) error {
	if l == nil || l.file == nil {
		return errors.New("plan lock is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fd := int(l.file.Fd())
	unlockErr := unix.Flock(fd, unix.LOCK_UN)
	closeErr := l.file.Close()
	l.file = nil
	if unlockErr != nil {
		unlockErr = fmt.Errorf("unlock plan sidecar: %w", unlockErr)
	}
	if closeErr != nil {
		closeErr = fmt.Errorf("close plan sidecar: %w", closeErr)
	}
	return errors.Join(unlockErr, closeErr)
}
