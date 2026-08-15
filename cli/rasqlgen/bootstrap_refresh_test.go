package rasqlgen

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAsDirectoryPattern(t *testing.T) {
	tests := map[string]string{
		"internal/tables":      "./internal/tables",
		"./internal/tables":    "./internal/tables",
		"../internal/tables":   "../internal/tables",
		".":                    ".",
		"..":                   "..",
		"/abs/internal/tables": "/abs/internal/tables",
	}
	for input, want := range tests {
		require.Equal(t, want, asDirectoryPattern(input), "input %q", input)
	}
}

// TestLoadExistingDescriptionTablesErrorNamesTheDirectoryTyped spawns a
// real `go list`. A bootstrap refresh names the -output value the user
// typed, not the "./"-prefixed pattern go list is handed for it, so a bare
// relative -output is reported back exactly as written. The two are only
// distinguishable for a bare relative value: an explicit "./internal/tables"
// and an absolute path are already in directory-pattern form and reach go
// list unchanged.
//
// The temporary working directory sits outside any Go module, which is
// what makes go list fail and the message reachable at all. Every message
// resolveSchemaSourcePackage emits interpolates that same value, including
// "schema source %s is not inside a Go module", so pinning one pins the
// labelling for all of them.
func TestLoadExistingDescriptionTablesErrorNamesTheDirectoryTyped(t *testing.T) {
	t.Chdir(t.TempDir())
	directory := filepath.Join("internal", "tables")
	require.NoError(t, os.MkdirAll(directory, 0o700))

	_, err := loadExistingDescriptionTables(context.Background(), directory)
	require.Error(t, err)
	require.ErrorContains(t, err, "resolve schema source "+directory+":")
	require.NotContains(t, err.Error(), asDirectoryPattern(directory))
}
