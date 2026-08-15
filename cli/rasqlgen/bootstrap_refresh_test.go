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

// TestLoadExistingDescriptionTablesErrorNamesTheDirectoryPattern spawns a
// real `go list`. A bootstrap refresh names the "./"-prefixed pattern go
// list is handed, not the bare -output value, which is what this path has
// always reported. The two are only distinguishable for a bare relative
// value: an explicit "./internal/tables" and an absolute path are already
// in directory-pattern form and reach go list unchanged.
//
// The temporary working directory sits outside any Go module, which is
// what makes go list fail and the message reachable at all. Every message
// resolveSchemaSourcePackage emits interpolates that same value, including
// "schema source %s is not inside a Go module", so pinning one pins the
// labelling for all of them.
func TestLoadExistingDescriptionTablesErrorNamesTheDirectoryPattern(t *testing.T) {
	t.Chdir(t.TempDir())
	directory := filepath.Join("internal", "tables")
	require.NoError(t, os.MkdirAll(directory, 0o700))

	_, err := loadExistingDescriptionTables(context.Background(), directory)
	require.Error(t, err)
	require.ErrorContains(t, err, "resolve schema source "+asDirectoryPattern(directory)+":")
	// The prefixed pattern contains the bare value, so only the label plus
	// the bare value distinguishes the two forms.
	require.NotContains(t, err.Error(), "resolve schema source "+directory+":")
}
