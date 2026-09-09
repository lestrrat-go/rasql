//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris && !windows

package migrate

import (
	"context"
	"errors"
)

func nativePlanFileIdentity(string) (string, []byte, error) {
	return "", nil, errors.New("native SQLite plan locks are unsupported on this platform")
}

func acquireNativePlanSidecar(context.Context, string) (ChangePlanLock, error) {
	return nil, errors.New("native SQLite plan locks are unsupported on this platform")
}
