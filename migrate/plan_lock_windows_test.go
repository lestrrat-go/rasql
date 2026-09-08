//go:build windows

package migrate

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestSQLitePlanLockIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "database.sqlite")
	require.NoError(t, os.WriteFile(path, nil, 0o600))
	osKind, identity, err := nativePlanFileIdentity(path)
	require.NoError(t, err)
	require.Equal(t, "windows", osKind)
	require.Len(t, identity, 16)

	alias := filepath.Join(filepath.Dir(path), "database-alias.sqlite")
	require.NoError(t, os.Link(path, alias))
	_, _, err = nativePlanFileIdentity(path)
	require.Error(t, err)
}

func TestSQLitePlanLockUnsafeTargets(t *testing.T) {
	path := filepath.Join(t.TempDir(), "lock-directory")
	require.NoError(t, os.Mkdir(path, 0o700))
	_, err := acquireNativePlanSidecar(t.Context(), path)
	require.Error(t, err)
	var lockErr *ChangePlanLockError
	require.ErrorAs(t, err, &lockErr)
	require.Equal(t, ChangePlanLockValidate, lockErr.Stage())
}

func TestSQLitePlanLockBlocksProcess(t *testing.T) {
	path := filepath.Join(t.TempDir(), "process.lock")
	command := exec.Command(os.Args[0], "-test.run=^TestSQLitePlanLockHelperProcess$")
	command.Env = append(os.Environ(), "RASQL_PLAN_LOCK_HELPER_PATH="+path)
	stdin, err := command.StdinPipe()
	require.NoError(t, err)
	stdout, err := command.StdoutPipe()
	require.NoError(t, err)
	require.NoError(t, command.Start())
	t.Cleanup(func() {
		_ = stdin.Close()
		if command.ProcessState == nil || !command.ProcessState.Exited() {
			_ = command.Process.Kill()
		}
	})

	scanner := bufio.NewScanner(stdout)
	require.True(t, scanner.Scan())
	require.Equal(t, "locked", scanner.Text())

	type lockResult struct {
		lock ChangePlanLock
		err  error
	}
	result := make(chan lockResult, 1)
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	go func() {
		lock, err := acquireNativePlanSidecar(ctx, path)
		result <- lockResult{lock: lock, err: err}
	}()
	select {
	case acquired := <-result:
		if acquired.lock != nil {
			_ = acquired.lock.Release(t.Context())
		}
		t.Fatalf("second process lock completed before release: %v", acquired.err)
	case <-time.After(150 * time.Millisecond):
	}

	require.NoError(t, stdin.Close())
	require.NoError(t, command.Wait())
	select {
	case acquired := <-result:
		require.NoError(t, acquired.err)
		require.NoError(t, acquired.lock.Release(t.Context()))
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	}
}

func TestSQLitePlanLockCancellation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cancel.lock")
	lock, err := acquireNativePlanSidecar(t.Context(), path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = lock.Release(context.Background()) })

	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	_, err = acquireNativePlanSidecar(ctx, path)
	require.ErrorIs(t, err, context.DeadlineExceeded)
}

func TestSQLitePlanLockHelperProcess(t *testing.T) {
	path := os.Getenv("RASQL_PLAN_LOCK_HELPER_PATH")
	if path == "" {
		return
	}
	lock, err := acquireNativePlanSidecar(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := fmt.Fprintln(os.Stdout, "locked"); err != nil {
		t.Fatal(err)
	}
	if _, err := bufio.NewReader(os.Stdin).ReadByte(); err != io.EOF {
		t.Fatalf("wait for release signal: %v", err)
	}
	if err := lock.Release(t.Context()); err != nil {
		t.Fatal(err)
	}
}
