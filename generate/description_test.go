package generate_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/stretchr/testify/require"
)

// TestWriteDescriptionPackageWritesTableSourceBytes pins that
// WriteDescriptionPackage writes the same bytes schemagen.DescriptionSource
// and schemagen.TablesFuncSource produce directly, byte for byte, and that
// files land at the same names WritePackage itself would use.
func TestWriteDescriptionPackageWritesTableSourceBytes(t *testing.T) {
	directory := t.TempDir()
	users, orders := usersTableDef(), ordersTableDef()

	require.NoError(t, generate.WriteDescriptionPackage("schemasource", directory, users, orders))

	wantUsers, err := schemagen.DescriptionSource("schemasource", "UsersDef", users)
	require.NoError(t, err)
	gotUsers, err := os.ReadFile(filepath.Join(directory, "users_gen.go"))
	require.NoError(t, err)
	require.Equal(t, wantUsers, gotUsers)

	wantOrders, err := schemagen.DescriptionSource("schemasource", "OrdersDef", orders)
	require.NoError(t, err)
	gotOrders, err := os.ReadFile(filepath.Join(directory, "orders_gen.go"))
	require.NoError(t, err)
	require.Equal(t, wantOrders, gotOrders)

	wantAggregator, err := schemagen.TablesFuncSource("schemasource", []string{"OrdersDef", "UsersDef"})
	require.NoError(t, err)
	gotAggregator, err := os.ReadFile(filepath.Join(directory, "tables_gen.go"))
	require.NoError(t, err)
	require.Equal(t, wantAggregator, gotAggregator)

	wantHints, err := schemagen.HintsSource("schemasource")
	require.NoError(t, err)
	gotHints, err := os.ReadFile(filepath.Join(directory, "hints.go"))
	require.NoError(t, err)
	require.Equal(t, wantHints, gotHints)
}

func TestWriteDescriptionPackageRejectsNoTables(t *testing.T) {
	directory := t.TempDir()
	err := generate.WriteDescriptionPackage("schemasource", directory)
	require.ErrorContains(t, err, "no tables to describe")
	require.Empty(t, mustReadDescriptionDir(t, directory))
}

func TestWriteDescriptionPackageRejectsNonEmptyDirectory(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "notes.txt"), []byte("hand-written"), 0o600))

	err := generate.WriteDescriptionPackage("schemasource", directory, usersTableDef())
	require.ErrorContains(t, err, "is not empty")
	require.NoFileExists(t, filepath.Join(directory, "users_gen.go"))
}

// TestWriteDescriptionPackageRefusesSecondRun proves a directory
// WriteDescriptionPackage already wrote to refuses a second run too:
// refreshing an existing description package is not supported yet.
func TestWriteDescriptionPackageRefusesSecondRun(t *testing.T) {
	directory := t.TempDir()
	require.NoError(t, generate.WriteDescriptionPackage("schemasource", directory, usersTableDef()))

	err := generate.WriteDescriptionPackage("schemasource", directory, usersTableDef())
	require.ErrorContains(t, err, "is not empty")
}

func TestWriteDescriptionPackageRejectsMissingDirectory(t *testing.T) {
	directory := filepath.Join(t.TempDir(), "missing")
	err := generate.WriteDescriptionPackage("schemasource", directory, usersTableDef())
	require.Error(t, err)
}

func TestWriteDescriptionPackageRejectsFileAsDirectory(t *testing.T) {
	directory := t.TempDir()
	file := filepath.Join(directory, "output")
	require.NoError(t, os.WriteFile(file, []byte("x"), 0o600))

	err := generate.WriteDescriptionPackage("schemasource", file, usersTableDef())
	require.ErrorContains(t, err, "is not a directory")
}

// TestWriteDescriptionPackageRejectsAggregatorNameCollision covers a table
// literally named "tables", which would generate the same filename as the
// aggregator file WriteDescriptionPackage itself writes.
func TestWriteDescriptionPackageRejectsAggregatorNameCollision(t *testing.T) {
	directory := t.TempDir()
	err := generate.WriteDescriptionPackage("schemasource", directory, namedTableDef("tables"))
	require.ErrorContains(t, err, `collides with the aggregator file`)
	require.Empty(t, mustReadDescriptionDir(t, directory))
}

// TestWriteDescriptionPackageRejectsFilenameCollision covers two tables
// whose names differ only in case, which schemaOutputFilename's
// lowercasing would generate the same file for.
func TestWriteDescriptionPackageRejectsFilenameCollision(t *testing.T) {
	directory := t.TempDir()
	err := generate.WriteDescriptionPackage("schemasource", directory, namedTableDef("Users"), namedTableDef("users"))
	require.ErrorContains(t, err, `both generate "users_gen.go"`)
	require.Empty(t, mustReadDescriptionDir(t, directory))
}

// mustReadDescriptionDir is a package-local helper: description_test.go and
// write_test.go both need it, and each is its own compilation unit within
// package generate_test, so it is defined once here.
func mustReadDescriptionDir(t *testing.T, directory string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	return entries
}
