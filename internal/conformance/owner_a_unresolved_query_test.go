package conformance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/stretchr/testify/require"
)

// TestUnresolvedGeneratedQuery proves that a query whose result column has no scalar declaration
// refuses generate -scratch with the analyzer's own diagnostic, exits 2, and writes neither
// internal/store nor rasql.sum -- the live-generation counterpart of what schema update used to
// prove against a checked-in lock. testdata/unresolved-query/rasql.lock.json was a 45-byte
// sentinel, not a real lock, so there is nothing left to snapshot and compare byte-for-byte; its
// removal in this task is what makes that sentinel's absence itself part of the proof.
func TestUnresolvedGeneratedQuery(t *testing.T) {
	root := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, copyTree(filepath.Join("testdata", "unresolved-query"), root))

	var output, diagnostics bytes.Buffer
	err := rasqlgen.RunContext(t.Context(), []string{"generate", "-config", filepath.Join(root, "rasql.json"), "-scratch"}, &output, &diagnostics)
	require.Error(t, err)
	require.Equal(t, 2, rasqlgen.ExitCode(err))
	require.Equal(t, `generate: query "unresolved_result" value "opaque" requires a scalar declaration`, err.Error())

	_, statErr := os.Stat(filepath.Join(root, "internal", "store"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	_, sumErr := os.Stat(filepath.Join(root, "internal", "store", "rasql.sum"))
	require.ErrorIs(t, sumErr, os.ErrNotExist)
}

func mustReadFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
