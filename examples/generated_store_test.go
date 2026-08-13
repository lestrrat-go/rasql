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

// generatedStore, generatedSchema, generatedSchemaTest and generatedQuery are
// the rasqlgen output the documentation shows. All are checked in, and
// compiled by every build, so a page can show real generated source instead
// of a copy someone kept up to date by hand.
const (
	generatedStore      = "examples/store/users_gen.go"
	generatedSchema     = "examples/store/schema_gen.go"
	generatedSchemaTest = "examples/store/schema_gen_test.go"
	generatedQuery      = "examples/store/user_by_email_gen.go"
	queryTemplate       = "examples/store/user_by_email.sql"
)

// TestGeneratedStoreIsCurrent regenerates the checked-in typed surface and
// descriptor files from the table definition they were generated for and
// fails when they differ, which is what stops the documentation from
// showing output the generator stopped producing. The definition is stated
// here in Go rather than read from a snapshot file, so the check needs
// neither a database nor a checked-in copy of the schema.
func TestGeneratedStoreIsCurrent(t *testing.T) {
	definition, err := schema.NewTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
	require.NoError(t, err)

	tableSource, err := generate.TableSource("store", definition, definition)
	require.NoError(t, err)
	requireGeneratedFile(t, generatedStore, string(tableSource))

	descriptorSource, err := generate.DescriptorSource("store", definition)
	require.NoError(t, err)
	requireGeneratedFile(t, generatedSchema, string(descriptorSource))

	descriptorTestSource, err := generate.DescriptorTestSource("store", definition)
	require.NoError(t, err)
	requireGeneratedFile(t, generatedSchemaTest, string(descriptorTestSource))
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
