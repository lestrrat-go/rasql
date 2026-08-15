package rasqlgen

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestSchemaSourcePattern pins which -source values get rewritten into a
// directory pattern and which reach `go list` untouched. It lives in the
// internal test package because schemaSourcePattern is unexported, and it
// runs in a scratch directory because the decision reads the filesystem for
// exactly one of the cases below.
//
// The colliding case is the one that matters: "example.com/pkg" names a
// real directory here and still resolves as an import path, because a
// dotted first path element decides the form before os.Stat is consulted.
func TestSchemaSourcePattern(t *testing.T) {
	workingDir := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workingDir, "internal", "tables"), 0o700))
	require.NoError(t, os.MkdirAll(filepath.Join(workingDir, "example.com", "pkg"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(workingDir, "notes"), []byte("x"), 0o600))
	t.Chdir(workingDir)

	separator := string(filepath.Separator)
	tests := map[string]string{
		// A bare relative directory on disk: the documented form, and
		// the only one that gets the prefix.
		"internal/tables": "." + separator + "internal/tables",
		// Already an explicit directory pattern.
		"./internal/tables": "./internal/tables",
		".":                 ".",
		// A module-qualified import path, whether or not a directory of
		// that name happens to sit under the working directory.
		"example.com/pkg":            "example.com/pkg",
		"example.com/consumer/other": "example.com/consumer/other",
		// The explicit form still reaches a directory whose own name
		// reads as an import path.
		"./example.com/pkg": "./example.com/pkg",
		// No dot in the first element and nothing on disk: left for go
		// list to resolve as the standard-library-shaped path it looks
		// like.
		"net/http": "net/http",
		// On disk, but not a directory.
		"notes": "notes",
	}
	for input, want := range tests {
		require.Equal(t, want, schemaSourcePattern(input), "input %q", input)
	}

	absolute := filepath.Join(workingDir, "internal", "tables")
	require.Equal(t, absolute, schemaSourcePattern(absolute))
}
