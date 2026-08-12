package examples_test

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// generatedStore and generatedQuery are the rasqlgen output the documentation
// shows. Both are checked in, and compiled by every build, so a page can show
// real generated source instead of a copy someone kept up to date by hand.
const (
	generatedStore = "examples/store/users_gen.go"
	generatedQuery = "examples/store/user_by_email_gen.go"
	queryTemplate  = "examples/store/user_by_email.sql"
)

// TestGeneratedStoreIsCurrent regenerates the checked-in descriptor from the
// table definition it was generated for and fails when the two differ, which is
// what stops the documentation from showing output the generator stopped
// producing. The definition is stated here in Go rather than read from a
// snapshot file, so the check needs neither a database nor a checked-in copy of
// the schema.
func TestGeneratedStoreIsCurrent(t *testing.T) {
	definition, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)

	source, err := generate.TableSource("store", definition, definition)
	require.NoError(t, err)
	requireGeneratedFile(t, generatedStore, string(source))
}

// TestGeneratedQueryIsCurrent regenerates the checked-in query function through
// the rasqlgen command itself, since compiling a static template is what that
// command does and there is no separate library entry point for it.
func TestGeneratedQueryIsCurrent(t *testing.T) {
	output := filepath.Join(t.TempDir(), "user_by_email_gen.go")
	require.NoError(t, rasqlgen.Run([]string{
		"query",
		"-input", filepath.Join(repositoryRoot, queryTemplate),
		"-function", "UserByEmail",
		"-dialect", "postgresql",
		"-package", "store",
		"-output", output,
	}, io.Discard))

	source, err := os.ReadFile(output)
	require.NoError(t, err)
	requireGeneratedFile(t, generatedQuery, string(source))
}

// requireGeneratedFile compares one checked-in generated file with the source
// the generator produces now, or rewrites it under -update-docs, the same flag
// that refreshes every included block.
func requireGeneratedFile(t *testing.T, path, source string) {
	t.Helper()

	full := filepath.Join(repositoryRoot, path)
	if *updateDocs {
		require.NoError(t, os.WriteFile(full, []byte(source), 0o644))
		return
	}
	committed, err := os.ReadFile(full)
	require.NoError(t, err)
	require.Equal(t, source, string(committed),
		"%s is stale; run `go test ./examples/ -update-docs`", path)
}
