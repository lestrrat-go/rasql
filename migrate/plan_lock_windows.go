//go:build windows

package migrate

import (
	"context"
	"encoding/binary"
	"errors"
	"time"

	"golang.org/x/sys/windows"
)

type windowsPlanLock struct {
	handle  windows.Handle
	overlap windows.Overlapped
}

func nativePlanFileIdentity(path string) (string, []byte, error) {
	handle, err := openWindowsPlanFile(path, windows.OPEN_EXISTING)
	if err != nil {
		return "", nil, err
	}
	defer func() { _ = windows.CloseHandle(handle) }()
	info, err := windowsPlanFileInfo(handle)
	if err != nil {
		return "", nil, err
	}
	identity := make([]byte, 16)
	binary.BigEndian.PutUint64(identity[:8], uint64(info.VolumeSerialNumber))
	binary.BigEndian.PutUint64(identity[8:], uint64(info.FileIndexHigh)<<32|uint64(info.FileIndexLow))
	return "windows", identity, nil
}

func acquireNativePlanSidecar(ctx context.Context, path string) (ChangePlanLock, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	handle, err := openWindowsPlanFile(path, windows.OPEN_ALWAYS)
	if err != nil {
		return nil, &ChangePlanLockError{stage: ChangePlanLockValidate, cause: err}
	}
	if _, err := windowsPlanFileInfo(handle); err != nil {
		_ = windows.CloseHandle(handle)
		return nil, &ChangePlanLockError{stage: ChangePlanLockValidate, cause: err}
	}
	lock := &windowsPlanLock{handle: handle}
	ticker := time.NewTicker(25 * time.Millisecond)
	defer ticker.Stop()
	for {
		err := windows.LockFileEx(handle, windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
			0, 1, 0, &lock.overlap)
		if err == nil {
			return lock, nil
		}
		if !errors.Is(err, windows.ERROR_LOCK_VIOLATION) {
			_ = windows.CloseHandle(handle)
			return nil, err
		}
		select {
		case <-ctx.Done():
			_ = windows.CloseHandle(handle)
			return nil, ctx.Err()
		case <-ticker.C:
		}
	}
}

func openWindowsPlanFile(path string, createMode uint32) (windows.Handle, error) {
	name, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return windows.InvalidHandle, err
	}
	return windows.CreateFile(name, windows.GENERIC_READ|windows.GENERIC_WRITE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, createMode,
		windows.FILE_ATTRIBUTE_NORMAL|windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
}

func windowsPlanFileInfo(handle windows.Handle) (windows.ByHandleFileInformation, error) {
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(handle, &info); err != nil {
		return info, err
	}
	if info.NumberOfLinks != 1 || info.FileAttributes&(windows.FILE_ATTRIBUTE_DIRECTORY|windows.FILE_ATTRIBUTE_REPARSE_POINT) != 0 {
		return info, errors.New("plan lock target must be a regular single-link file")
	}
	return info, nil
}

func (l *windowsPlanLock) Release(ctx context.Context) error {
	if l == nil || l.handle == windows.InvalidHandle || l.handle == 0 {
		return errors.New("plan lock is nil")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	unlockErr := windows.UnlockFileEx(l.handle, 0, 1, 0, &l.overlap)
	closeErr := windows.CloseHandle(l.handle)
	l.handle = windows.InvalidHandle
	return errors.Join(unlockErr, closeErr)
}
