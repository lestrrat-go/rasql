//go:build unix

package rasqlgen_test

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/stretchr/testify/require"
)

func TestConfigFIFOLimit(t *testing.T) {
	const limit = 1 << 20
	path := filepath.Join(t.TempDir(), "config.json")
	require.NoError(t, syscall.Mkfifo(path, 0o600))
	payload := append([]byte("{}"), bytes.Repeat([]byte{' '}, limit+1)...)

	writerDone := make(chan error, 1)
	go func() {
		file, err := os.OpenFile(path, os.O_WRONLY, 0)
		if err != nil {
			writerDone <- err
			return
		}
		_, writeErr := file.Write(payload)
		closeErr := file.Close()
		if writeErr != nil {
			writerDone <- writeErr
			return
		}
		writerDone <- closeErr
	}()

	runDone := make(chan error, 1)
	go func() {
		var output, diagnostics bytes.Buffer
		err := rasqlgen.Run([]string{"generate", "-config", path}, &output, &diagnostics)
		runDone <- err
	}()
	defer func() {
		select {
		case writerErr := <-writerDone:
			if writerErr != nil {
				require.True(t, errors.Is(writerErr, syscall.EPIPE), writerErr)
			}
		case <-time.After(10 * time.Second):
			t.Error("config writer did not finish")
		}
	}()

	var err error
	select {
	case err = <-runDone:
	case <-time.After(10 * time.Second):
		t.Fatal("config loading blocked on the finite FIFO")
	}
	require.Error(t, err)
	require.Contains(t, err.Error(), "1048576-byte limit")
	require.Contains(t, err.Error(), "-config expects a settings file")
	require.NotContains(t, err.Error(), "unsupported -dialect")
}
