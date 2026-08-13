package generate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/internal/schemagen"
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

// namedTableDef returns a table with the same shape as usersTableDef under
// whatever name the caller gives, which is what the spelling tests vary.
func namedTableDef(name string) schema.TableDef {
	return schema.MustTableDef(name,
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
// same bytes the CLI writes today, byte for byte.
//
// The comparison is against schemagen.TableSurfaceSource, not
// generate.TableSource: the per-table files hold the surface alone, with the
// descriptors declared once in schema_gen.go, while generate.TableSource
// returns both in one file, which is the form a caller of that package gets
// rather than the form this directory holds.
func TestWritePackageWritesTableSourceBytes(t *testing.T) {
	directory := t.TempDir()
	users, orders := usersTableDef(), ordersTableDef()

	require.NoError(t, generate.WritePackage("store", directory, users, orders))

	wantUsers, err := schemagen.TableSurfaceSource("store", users, users, orders)
	require.NoError(t, err)
	gotUsers, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Equal(t, wantUsers, gotUsers)

	wantOrders, err := schemagen.TableSurfaceSource("store", orders, users, orders)
	require.NoError(t, err)
	gotOrders, err := os.ReadFile(filepath.Join(directory, "orders_gen.go"))
	require.NoError(t, err)
	require.Equal(t, wantOrders, gotOrders)

	wantDescriptor, err := generate.DescriptorSource("store", users, orders)
	require.NoError(t, err)
	gotDescriptor, err := os.ReadFile(filepath.Join(directory, "schema_gen.go"))
	require.NoError(t, err)
	require.Equal(t, wantDescriptor, gotDescriptor)
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
	require.Contains(t, string(source), "func TestRasqlgenGeneratedDefinitionsAreValid(t *testing.T) {")
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

// TestWritePackageRejectsTableSpellingTheGeneratedTest confirms that the
// accessor for a table named test_rasqlgen_generated_definitions_are_valid is
// refused before anything is written, because it spells the function
// schema_gen_test.go declares. Both declarations would come from files this
// run writes into an empty directory and marks DO NOT EDIT, so the package
// would compile under go build and fail to build under go test.
func TestWritePackageRejectsTableSpellingTheGeneratedTest(t *testing.T) {
	directory := t.TempDir()
	table := schema.MustTableDef("test_rasqlgen_generated_definitions_are_valid",
		schema.Integer("id"),
		schema.PrimaryKey("id"),
	)

	err := generate.WritePackage("store", directory, table)
	require.ErrorContains(t, err, `duplicates generated name "TestRasqlgenGeneratedDefinitionsAreValid"`)
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

// TestWritePackageRefusesToOrphanAnEarlierTableFile pins what a narrowed
// rerun does. Generating users and orders and then rerunning for users alone
// rewrites schema_gen.go without the orders descriptor, while orders_gen.go
// stays behind reading the ordersTable value that descriptor declared, so
// the package stops compiling. The run refuses instead, before it writes
// anything, and the refusal names the file and what to do about it.
func TestWritePackageRefusesToOrphanAnEarlierTableFile(t *testing.T) {
	directory := t.TempDir()
	users, orders := usersTableDef(), ordersTableDef()
	require.NoError(t, generate.WritePackage("store", directory, users, orders))

	generated := make(map[string]string, 4)
	for _, name := range []string{"users_gen.go", "orders_gen.go", "schema_gen.go", "schema_gen_test.go"} {
		source, err := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, err)
		generated[name] = string(source)
	}

	err := generate.WritePackage("store", directory, users)
	require.ErrorContains(t, err, "refusing to write schema output")
	require.ErrorContains(t, err, `orders_gen.go was generated for table "orders"`)
	require.ErrorContains(t, err, "would no longer find the ordersTable value it reads")
	require.ErrorContains(t, err, "name every table the package needs in one run, or delete the file first")
	for name, source := range generated {
		unchanged, err := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, err)
		require.Equal(t, source, string(unchanged), "%s must be exactly as the refused run found it", name)
	}

	// Deleting the file the refusal names is one of the two ways out, so
	// the same rerun then succeeds and leaves a package holding users
	// alone.
	require.NoError(t, os.Remove(filepath.Join(directory, "orders_gen.go")))
	require.NoError(t, generate.WritePackage("store", directory, users))
	descriptor, err := os.ReadFile(filepath.Join(directory, "schema_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(descriptor), "var usersDef = schema.TableDef{")
	require.NotContains(t, string(descriptor), "var ordersDef = schema.TableDef{")
}

// TestWritePackageAllowsARenameThatKeepsTheSameFile pins the run the guard
// must not refuse. schemaOutputFilename lowercases the whole table name, so
// "Users" and "users" name one users_gen.go and one usersTable value: the
// second run rewrites exactly the file the first wrote and strands nothing.
// What it leaves behind is one file per table, holding the second spelling.
func TestWritePackageAllowsARenameThatKeepsTheSameFile(t *testing.T) {
	for _, rename := range []string{"users", "USERS", "Users"} {
		t.Run(rename, func(t *testing.T) {
			directory := t.TempDir()
			require.NoError(t, generate.WritePackage("store", directory, namedTableDef("Users")))

			renamed := namedTableDef(rename)
			require.NoError(t, generate.WritePackage("store", directory, renamed))

			entries, err := os.ReadDir(directory)
			require.NoError(t, err)
			names := make([]string, 0, len(entries))
			for _, entry := range entries {
				names = append(names, entry.Name())
			}
			require.ElementsMatch(t, []string{"users_gen.go", "schema_gen.go", "schema_gen_test.go"}, names)

			// Nothing of the first spelling survives: the file holds the
			// bytes the second run generates, and the descriptor it reads is
			// declared for the second spelling.
			want, err := schemagen.TableSurfaceSource("store", renamed, renamed)
			require.NoError(t, err)
			got, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
			require.NoError(t, err)
			require.Equal(t, want, got)
			descriptor, err := os.ReadFile(filepath.Join(directory, "schema_gen.go"))
			require.NoError(t, err)
			require.Contains(t, string(descriptor), "var usersTable = ")
			require.Contains(t, string(descriptor), `Name: "`+rename+`"`)
		})
	}
}

// TestWritePackageRefusesARenameThatWritesAnotherFile pins the half of the
// same question that must keep refusing. "APIKeys" and "api_keys" share the
// apiKeysTable value but generate apikeys_gen.go and api_keys_gen.go, so
// letting the second run through would leave both files declaring the same
// types and the package would not compile. The refusal says that, rather
// than blaming a value that is in fact still declared.
func TestWritePackageRefusesARenameThatWritesAnotherFile(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, generate.WritePackage("store", directory, namedTableDef("APIKeys")))

	generated := make(map[string]string, 3)
	for _, name := range []string{"apikeys_gen.go", "schema_gen.go", "schema_gen_test.go"} {
		source, err := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, err)
		generated[name] = string(source)
	}

	err := generate.WritePackage("store", directory, namedTableDef("api_keys"))
	require.ErrorContains(t, err, "refusing to write schema output")
	require.ErrorContains(t, err, `apikeys_gen.go was generated for table "APIKeys"`)
	require.ErrorContains(t, err, `which this run spells "api_keys" and generates into api_keys_gen.go instead`)
	require.ErrorContains(t, err, "name every table the package needs in one run, or delete the file first")
	require.NotContains(t, err.Error(), "would no longer find", "this run declares apiKeysTable, so the refusal must not claim the value goes missing")
	require.NoFileExists(t, filepath.Join(directory, "api_keys_gen.go"))
	for name, source := range generated {
		unchanged, err := os.ReadFile(filepath.Join(directory, name))
		require.NoError(t, err)
		require.Equal(t, source, string(unchanged), "%s must be exactly as the refused run found it", name)
	}

	// Deleting the file the refusal names is the way out here too.
	require.NoError(t, os.Remove(filepath.Join(directory, "apikeys_gen.go")))
	require.NoError(t, generate.WritePackage("store", directory, namedTableDef("api_keys")))
	require.FileExists(t, filepath.Join(directory, "api_keys_gen.go"))
	require.NoFileExists(t, filepath.Join(directory, "apikeys_gen.go"))
}

// TestWritePackageAllowsARerunThatKeepsEveryTable confirms the orphan guard
// stays out of the way of the runs that do not drop a table: the same two
// tables again, and a query file generated beside them, which declares no
// descriptor and so can never be orphaned by a schema run.
func TestWritePackageAllowsARerunThatKeepsEveryTable(t *testing.T) {
	directory := t.TempDir()
	users, orders := usersTableDef(), ordersTableDef()
	require.NoError(t, generate.WritePackage("store", directory, users, orders))

	queryFile := filepath.Join(directory, "user_by_id_gen.go")
	require.NoError(t, os.WriteFile(queryFile, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	require.NoError(t, generate.WritePackage("store", directory, users, orders))
	require.NoError(t, generate.WritePackage("store", directory, orders, users))
	require.FileExists(t, queryFile)
}

// TestWritePackageRejectsAnUnreadableDescriptorFile covers the one case
// where the orphan guard cannot answer its own question. A descriptor file
// that carries the marker but no longer parses has been edited since
// rasqlgen wrote it, so which tables the package holds is unknown, and the
// run stops rather than writing over a package it cannot reason about.
func TestWritePackageRejectsAnUnreadableDescriptorFile(t *testing.T) {
	directory := t.TempDir()
	corrupted := genfile.Marker + "\n\npackage store\n\nvar usersDef = schema.TableDef{\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema_gen.go"), []byte(corrupted), 0o600))

	err := generate.WritePackage("store", directory, usersTableDef())
	require.ErrorContains(t, err, "cannot read the tables")
	require.ErrorContains(t, err, "delete it and rerun to regenerate the package")
	source, err := os.ReadFile(filepath.Join(directory, "schema_gen.go"))
	require.NoError(t, err)
	require.Equal(t, corrupted, string(source))
	require.NoFileExists(t, filepath.Join(directory, "users_gen.go"))
}

func TestWritePackageRejectsInvalidPackageNameBeforeWriting(t *testing.T) {
	directory := t.TempDir()

	err := generate.WritePackage("not a valid package name", directory, usersTableDef())
	require.Error(t, err)
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	require.Empty(t, entries)
}
