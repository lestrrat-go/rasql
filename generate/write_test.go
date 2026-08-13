package generate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func usersTableDef() schema.TableDef {
	return schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
}

func ordersTableDef() schema.TableDef {
	return schema.MustTableDef("orders",
		schema.Integer("id"),
		schema.Integer("user_id"),
		schema.PrimaryKey("id"),
	)
}

// TestWritePackageWritesTableSourceBytes pins that WritePackage writes the
// same bytes the CLI writes today, byte for byte against generate.TableSource.
func TestWritePackageWritesTableSourceBytes(t *testing.T) {
	directory := t.TempDir()
	users, orders := usersTableDef(), ordersTableDef()

	require.NoError(t, generate.WritePackage("store", directory, users, orders))

	wantUsers, err := generate.TableSource("store", users, users, orders)
	require.NoError(t, err)
	gotUsers, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Equal(t, wantUsers, gotUsers)

	wantOrders, err := generate.TableSource("store", orders, users, orders)
	require.NoError(t, err)
	gotOrders, err := os.ReadFile(filepath.Join(directory, "orders_gen.go"))
	require.NoError(t, err)
	require.Equal(t, wantOrders, gotOrders)
}

// TestWritePackageInputOrderDoesNotMatter proves the output is sorted
// internally, so callers do not have to sort their own tables slice.
func TestWritePackageInputOrderDoesNotMatter(t *testing.T) {
	users, orders := usersTableDef(), ordersTableDef()

	forward := t.TempDir()
	require.NoError(t, generate.WritePackage("store", forward, orders, users))
	backward := t.TempDir()
	require.NoError(t, generate.WritePackage("store", backward, users, orders))

	for _, filename := range []string{"users_gen.go", "orders_gen.go"} {
		forwardBytes, err := os.ReadFile(filepath.Join(forward, filename))
		require.NoError(t, err)
		backwardBytes, err := os.ReadFile(filepath.Join(backward, filename))
		require.NoError(t, err)
		require.Equal(t, forwardBytes, backwardBytes)
	}
}

// TestWritePackageDoesNotReorderCallersSlice proves WritePackage sorts a
// copy, not the caller's own slice.
func TestWritePackageDoesNotReorderCallersSlice(t *testing.T) {
	directory := t.TempDir()
	users, orders := usersTableDef(), ordersTableDef()
	tables := []schema.TableDef{orders, users}

	require.NoError(t, generate.WritePackage("store", directory, tables...))

	require.Equal(t, "orders", tables[0].Name)
	require.Equal(t, "users", tables[1].Name)
}

func TestWritePackageMissingDirectoryReturnsErrorAndWritesNothing(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "does-not-exist")

	err := generate.WritePackage("store", directory, usersTableDef())
	require.Error(t, err)
	require.NoDirExists(t, directory)
}

func TestWritePackageRejectsFileOutput(t *testing.T) {
	file := filepath.Join(t.TempDir(), "not-a-directory")
	require.NoError(t, os.WriteFile(file, []byte("not a directory"), 0o600))

	err := generate.WritePackage("store", file, usersTableDef())
	require.ErrorContains(t, err, "is not a directory")
}

// TestWritePackageRejectsFilenameCollisionBeforeWriting uses the same
// api_keys / api__keys pair TestRunSchemaRejectsCrossFileNameCollisionsBeforeWriting
// exercises from the CLI side, and checks that a name collision fails before
// either file is written.
func TestWritePackageRejectsFilenameCollisionBeforeWriting(t *testing.T) {
	directory := t.TempDir()
	apiKeys := schema.MustTableDef("api_keys",
		schema.Integer("id"),
		schema.PrimaryKey("id"),
	)
	apiKeysDouble := schema.MustTableDef("api__keys",
		schema.Integer("id"),
		schema.PrimaryKey("id"),
	)

	err := generate.WritePackage("store", directory, apiKeys, apiKeysDouble)
	require.Error(t, err)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// TestWritePackageWritesTheWholePackage pins the exact set of files one call
// leaves behind: one per table, plus the two the package shares.
func TestWritePackageWritesTheWholePackage(t *testing.T) {
	directory := t.TempDir()

	require.NoError(t, generate.WritePackage("store", directory, usersTableDef(), ordersTableDef()))

	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	require.ElementsMatch(t, []string{"users_gen.go", "orders_gen.go", "schema_gen.go", "schema_gen_test.go"}, names)

	descriptor, err := os.ReadFile(filepath.Join(directory, "schema_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(descriptor), "var usersTable = UsersTable{rasql.TableFrom[UsersRow](usersDef)}")
}

// TestWritePackageAcceptsGenTestSuffix confirms genfile.Write's suffix guard
// accepts the one destination WritePackage writes that does not end in plain
// _gen.go: the generated validity test, schema_gen_test.go.
func TestWritePackageAcceptsGenTestSuffix(t *testing.T) {
	directory := t.TempDir()

	require.NoError(t, generate.WritePackage("store", directory, usersTableDef()))

	source, err := os.ReadFile(filepath.Join(directory, "schema_gen_test.go"))
	require.NoError(t, err)
	require.Contains(t, string(source), "func TestGeneratedDefinitionsAreValid(t *testing.T) {")
}

// TestWritePackageRejectsTableNamedSchema confirms that a table literally
// named "schema" is rejected before anything is written, because
// schemaOutputFilename would otherwise produce schema_gen.go, colliding with
// the package-wide descriptor file every run writes.
func TestWritePackageRejectsTableNamedSchema(t *testing.T) {
	directory := t.TempDir()
	table := schema.MustTableDef("schema",
		schema.Integer("id"),
		schema.PrimaryKey("id"),
	)

	err := generate.WritePackage("store", directory, table)
	require.ErrorContains(t, err, `schema table "schema" generates "schema_gen.go"`)
	require.NoFileExists(t, filepath.Join(directory, "schema_gen.go"))
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}

// handWrittenSource is a file somebody wrote by hand that happens to sit
// where generated output would land. Nothing about it says rasqlgen, which
// is the whole point: the destination must survive.
const handWrittenSource = "package store\n\n// Written by hand. Losing this file loses the only copy.\nfunc Keep() bool { return true }\n"

// TestWritePackageRefusesHandWrittenDestination confirms the refusal reaches
// every kind of destination a schema run writes: the per-table file, the
// package-wide descriptor, and the package-wide descriptor test. A run that
// hits one leaves the whole output directory as it found it.
func TestWritePackageRefusesHandWrittenDestination(t *testing.T) {
	for _, victim := range []string{"users_gen.go", "schema_gen.go", "schema_gen_test.go"} {
		t.Run(victim, func(t *testing.T) {
			directory := t.TempDir()
			victimPath := filepath.Join(directory, victim)
			require.NoError(t, os.WriteFile(victimPath, []byte(handWrittenSource), 0o600))

			err := generate.WritePackage("store", directory, usersTableDef())
			require.ErrorContains(t, err, "refusing to overwrite")
			require.ErrorContains(t, err, victimPath)

			source, err := os.ReadFile(victimPath)
			require.NoError(t, err)
			require.Equal(t, handWrittenSource, string(source))
			// Nothing else was written either: the run reported the
			// collision before it replaced anything.
			entries, err := os.ReadDir(directory)
			require.NoError(t, err)
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			require.ElementsMatch(t, []string{victim}, names)
		})
	}
}

// TestWritePackageResolvesEveryDestinationBeforeWriting pins the pre-scan. A
// schema run writes a package rather than a file, so a collision on a file
// it writes late must not leave the files it writes early already replaced.
// The tables sort to orders_gen.go, users_gen.go, schema_gen.go,
// schema_gen_test.go, so each victim below is preceded by at least one
// destination the run would otherwise have written first.
func TestWritePackageResolvesEveryDestinationBeforeWriting(t *testing.T) {
	for _, victim := range []string{"users_gen.go", "schema_gen.go", "schema_gen_test.go"} {
		t.Run(victim, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, victim), []byte(handWrittenSource), 0o600))

			err := generate.WritePackage("store", directory, usersTableDef(), ordersTableDef())
			require.ErrorContains(t, err, "refusing to overwrite")
			require.NoFileExists(t, filepath.Join(directory, "orders_gen.go"))
			for _, name := range []string{"users_gen.go", "schema_gen.go", "schema_gen_test.go"} {
				if name == victim {
					continue
				}
				require.NoFileExists(t, filepath.Join(directory, name))
			}
		})
	}
}

// TestWritePackageRegeneratesItsOwnOutput confirms the guard does not stand
// in the way of the normal case. Running twice over the same directory
// replaces every file the first run wrote, without complaint.
func TestWritePackageRegeneratesItsOwnOutput(t *testing.T) {
	directory := t.TempDir()

	require.NoError(t, generate.WritePackage("store", directory, usersTableDef()))
	first := make(map[string]string, 3)
	for _, name := range []string{"users_gen.go", "schema_gen.go", "schema_gen_test.go"} {
		source, err := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, err)
		first[name] = string(source)
	}

	require.NoError(t, generate.WritePackage("store", directory, usersTableDef()))
	for name, source := range first {
		regenerated, err := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, err)
		require.Equal(t, source, string(regenerated))
	}
}

func TestWritePackageRejectsInvalidPackageNameBeforeWriting(t *testing.T) {
	directory := t.TempDir()

	err := generate.WritePackage("not a valid package name", directory, usersTableDef())
	require.Error(t, err)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}
