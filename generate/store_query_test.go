package generate_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

const maxQueryInputBytes = 64 << 20

// TestStorePlanRejectsOversizedQueryInput keeps Store.Queries bounded by the
// same 64 MiB input limit as rasqlgen query. The plan must reject the source
// before template parsing or any generated file can be committed.
func TestStorePlanRejectsOversizedQueryInput(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "oversized.sql")
	data := bytes.Repeat([]byte("x"), maxQueryInputBytes+1)
	require.NoError(t, os.WriteFile(input, data, 0o600))

	store := generate.Store{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Tables:  []schema.TableDef{usersTableDef()},
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{
			Input:    input,
			Function: "Oversized",
			Output:   "oversized_gen.go",
		}},
	}

	_, err := store.Plan()
	require.ErrorContains(t, err, input)
	require.ErrorContains(t, err, "exceeds maximum size of 67108864 bytes")
}
