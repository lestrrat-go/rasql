//go:build unix

package conformance

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/stretchr/testify/require"
)

// TestGeneratedQueryProvenanceNativeTypesLive is the live counterpart TestGeneratedQueryProvenance
// cannot be: PostgreSQL's query analyzer upgrades a declared parameter or result's type certainty
// from "declared" to "known" once a live catalog confirms it, attaching the server's own native
// type (int4, bool, date, varchar, timestamp) and, for integer columns, its unsigned/zero-fill
// facts. rasql.sum carries none of this -- it is a checksum format, not a provenance record -- so
// the only place this claim can still be proven is in the generated Go a live PostgreSQL server
// actually produces. schema.IntegerType{Unsigned: false, ZeroFill: false} in place of the bare
// schema.IntegerType{} every other engine emits is that proof: it appears only when the query
// analyzer's live PostgreSQL describer ran and reported those facts back.
func TestGeneratedQueryProvenanceNativeTypesLive(t *testing.T) {
	dsn := postgresFixtureDSN(t)
	root := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, copyTree(filepath.Join("testdata", "postgresql"), root))
	require.NoError(t, os.RemoveAll(filepath.Join(root, "internal", "store")))

	var output, diagnostics bytes.Buffer
	err := rasqlgen.RunContext(t.Context(), []string{"generate", "-config", filepath.Join(root, "rasql.json"), "-dsn", dsn}, &output, &diagnostics)
	require.NoError(t, err, "output=%s diagnostics=%s", output.String(), diagnostics.String())

	for _, file := range []string{"overdue_task_gen.go", "maybe_overdue_task_gen.go", "overdue_tasks_gen.go"} {
		source, err := os.ReadFile(filepath.Join(root, "internal", "store", file))
		require.NoError(t, err, file)
		text := string(source)
		require.Contains(t, text, "schema.IntegerType{Unsigned: false, ZeroFill: false}", file)
		require.NotContains(t, text, "schema.IntegerType{}", file)
	}
}
