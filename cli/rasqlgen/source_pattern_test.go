package rasqlgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSchemaSourceDirectoryRetry pins which -source values are retried as a
// directory pattern once `go list` has refused the value itself, and which
// are reported with that refusal instead. It lives in the internal test
// package because schemaSourceDirectoryRetry is unexported.
//
// The decision reads no filesystem at all, which is what the scratch
// directory below proves: every colliding name in it -- "fmt", which go list
// resolves as the standard library, and "example.com/pkg", which it resolves
// as an import path -- is decided the same way whether or not a directory of
// that name sits under the working directory.
func TestSchemaSourceDirectoryRetry(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workingDir, "internal", "tables"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(workingDir, "example.com", "pkg"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(workingDir, "fmt"), 0o700))
	t.Chdir(workingDir)

	separator := string(filepath.Separator)
	retried := []string{
		// A bare relative directory: the documented form, and the one the
		// retry exists for, since go list reads it as a standard-library
		// import path and refuses it.
		"internal/tables",
		// Shaped like a standard-library path, and offered the same retry:
		// go list resolving it first is what settles those, not this.
		"net/http",
		"fmt",
	}
	for _, input := range retried {
		pattern, retry := schemaSourceDirectoryRetry(input)
		require.True(t, retry, "input %q", input)
		require.Equal(t, "."+separator+input, pattern, "input %q", input)
	}

	notRetried := []string{
		// Already an explicit directory pattern: the first resolution
		// already read it as that directory.
		"./internal/tables",
		".",
		"..",
		"../sibling",
		filepath.Join(workingDir, "internal", "tables"),
		// A module-qualified import path, whether or not a directory of
		// that name happens to sit under the working directory. Its own
		// resolution failure is the error worth reporting.
		"example.com/pkg",
		"example.com/consumer/other",
	}
	for _, input := range notRetried {
		pattern, retry := schemaSourceDirectoryRetry(input)
		require.False(t, retry, "input %q", input)
		require.Empty(t, pattern, "input %q", input)
	}
}
