package main_test

import (
	"bytes"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestGoRunRasqlgenReportsUsageFailureWithExitTwo(t *testing.T) {
	repository, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "rasqlgen")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/rasqlgen")
	build.Dir = repository
	require.NoError(t, build.Run())
	command := exec.CommandContext(t.Context(), binary, "generate", "-unknown")
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	require.Error(t, command.Run())
	require.Equal(t, 2, command.ProcessState.ExitCode())
	require.Empty(t, stdout.String())
	require.Contains(t, stderr.String(), "flag provided but not defined: -unknown")
}
