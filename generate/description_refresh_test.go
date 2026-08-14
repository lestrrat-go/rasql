package generate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestValidateDescriptionPackageOwnershipAcceptsBootstrapOutput proves a
// directory WriteDescriptionPackage just wrote passes ownership validation
// as-is, hints.go included.
func TestValidateDescriptionPackageOwnershipAcceptsBootstrapOutput(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, generate.WriteDescriptionPackage("schemasource", directory, usersTableDef()))

	require.NoError(t, generate.ValidateDescriptionPackageOwnership(directory))
}

// TestValidateDescriptionPackageOwnershipRejectsHandWrittenFile proves a
// hand-written file sitting beside bootstrap's own output is refused, the
// same refusal a first bootstrap run into a non-empty directory already
// makes.
func TestValidateDescriptionPackageOwnershipRejectsHandWrittenFile(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, generate.WriteDescriptionPackage("schemasource", directory, usersTableDef()))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "notes.go"), []byte("package schemasource\n"), 0o600))

	err := generate.ValidateDescriptionPackageOwnership(directory)
	require.ErrorContains(t, err, `holds "notes.go"`)
}

// TestValidateDescriptionPackageOwnershipRejectsSubdirectory proves a
// subdirectory sitting in -output is refused the same way.
func TestValidateDescriptionPackageOwnershipRejectsSubdirectory(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, generate.WriteDescriptionPackage("schemasource", directory, usersTableDef()))
	require.NoError(t, os.Mkdir(filepath.Join(directory, "sub"), 0o700))

	err := generate.ValidateDescriptionPackageOwnership(directory)
	require.ErrorContains(t, err, `holds a subdirectory "sub"`)
}

// TestApplyDescriptionDiffIsNoOpWhenEmpty proves ApplyDescriptionDiff never
// touches the directory when the diff it is given is empty, which is what
// keeps a refresh with nothing to write byte-identical to what was already
// on disk.
func TestApplyDescriptionDiffIsNoOpWhenEmpty(t *testing.T) {
	directory := t.TempDir()
	users := usersTableDef()
	require.NoError(t, generate.WriteDescriptionPackage("schemasource", directory, users))

	before := mustSnapshotDir(t, directory)
	require.NoError(t, generate.ApplyDescriptionDiff("schemasource", directory, []schema.TableDef{users}, generate.DescriptionDiff{}))
	after := mustSnapshotDir(t, directory)
	require.Equal(t, before, after)
}

// TestApplyDescriptionDiffAddsChangesAndRemovesTables exercises the whole
// chain a live -write refresh depends on: a table added, a table dropped
// (its file deleted and dropped from the aggregator), and a table whose
// column set changed (its file rewritten), while an untouched table's file
// is left completely alone.
func TestApplyDescriptionDiffAddsChangesAndRemovesTables(t *testing.T) {
	directory := t.TempDir()
	users, orders := usersTableDef(), ordersTableDef()
	dropped := namedTableDef("legacy_users")
	require.NoError(t, generate.WriteDescriptionPackage("schemasource", directory, users, dropped))

	untouchedBefore, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)

	changedUsers := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.Text("phone"),
		schema.PrimaryKey("id"),
	)
	oldTables := []schema.TableDef{users, dropped}
	newTables := []schema.TableDef{changedUsers, orders}

	diff := generate.DiffDescriptionPackage(oldTables, newTables)
	require.NoError(t, generate.ApplyDescriptionDiff("schemasource", directory, newTables, diff))

	require.FileExists(t, filepath.Join(directory, "orders_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "legacy_users_gen.go"))

	usersAfter, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.NotEqual(t, string(untouchedBefore), string(usersAfter))
	require.Contains(t, string(usersAfter), `{Name: "phone"`)

	aggregator, err := os.ReadFile(filepath.Join(directory, "tables_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(aggregator), "OrdersDef(),")
	require.Contains(t, string(aggregator), "UsersDef(),")
	require.NotContains(t, string(aggregator), "LegacyUsersDef")
}

// TestApplyDescriptionDiffNeverRewritesHints proves the one promise
// hints.go makes under a refresh too: an edit made to it survives
// ApplyDescriptionDiff even when the diff is non-empty and every other file
// in the package is rewritten.
func TestApplyDescriptionDiffNeverRewritesHints(t *testing.T) {
	directory := t.TempDir()
	users := usersTableDef()
	require.NoError(t, generate.WriteDescriptionPackage("schemasource", directory, users))

	hintsPath := filepath.Join(directory, "hints.go")
	edited := []byte("package schemasource\n\nimport \"github.com/lestrrat-go/rasql/schema\"\n\nvar Hints = map[string]schema.TableHint{\"users\": {RowName: \"User\"}}\n")
	require.NoError(t, os.WriteFile(hintsPath, edited, 0o600))

	orders := ordersTableDef()
	newTables := []schema.TableDef{users, orders}
	diff := generate.DiffDescriptionPackage([]schema.TableDef{users}, newTables)
	require.NoError(t, generate.ApplyDescriptionDiff("schemasource", directory, newTables, diff))

	got, err := os.ReadFile(hintsPath)
	require.NoError(t, err)
	require.Equal(t, edited, got)
}

// mustSnapshotDir reads every regular file directly inside directory into a
// name-to-contents map, for a byte-identical comparison across an
// operation that must be a no-op.
func mustSnapshotDir(t *testing.T, directory string) map[string]string {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	snapshot := make(map[string]string, len(entries))
	for _, entry := range entries {
		contents, err := os.ReadFile(filepath.Join(directory, entry.Name()))
		require.NoError(t, err)
		snapshot[entry.Name()] = string(contents)
	}
	return snapshot
}
