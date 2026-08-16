package main

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/modroot"
	"github.com/stretchr/testify/require"
)

func TestRunCheck(t *testing.T) {
	require.NoError(t, run(context.Background(), true))
}

func TestRunCheckReportsStaleOutput(t *testing.T) {
	root, err := modroot.FromWorkingDirectory()
	require.NoError(t, err)
	require.NotEmpty(t, root)

	path := filepath.Join(root, storeDirectory, "schema_gen.go")
	original, err := os.ReadFile(path)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, os.WriteFile(path, original, 0o644))
	})
	require.NoError(t, os.WriteFile(path, append(original, '\n', '/', '/', ' ', 's', 't', 'a', 'l', 'e', '\n'), 0o644))

	require.ErrorIs(t, run(context.Background(), true), generate.ErrStale)
}
