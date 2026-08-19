package examples_test

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// TestTaskboardStoreIsCurrent closes the gap CONTRIBUTING.md's "Generated
// files outside the root module" section records: sample/taskboard is a
// separate module, so nothing in the root `go test ./...` regenerates or
// checks its checked-in store, and a generator change can leave it stale
// with a fully green root test run.
//
// It closes that gap from the root module alone, with no subprocess and
// without touching sample/taskboard itself: it builds a throwaway SQLite
// database in t.TempDir() from sample/taskboard's own migrations, sweeps it
// with catalog.FromDatabase exactly the way `rasql codegen generate`
// will, and checks the result against sample/taskboard/internal/store
// through the same directory comparison requireGeneratedDirectoryMatches
// performs for examples/store. Nothing here rewrites the directory being
// checked, so the directory as it stands is the checked-in source.
//
// The migrations are read with internal/migrationdir and applied with
// migrate.Runner, which is exactly what `rasqlmigrate apply -dir
// sample/taskboard/migrations` does in that module's own
// //go:generate directive, so the whole migration tree is applied in the
// same order and under the same rules. Naming one migration directory here
// instead would leave this test pinning a schema the module's generator
// stopped producing the moment a second migration was added, and it would
// still pass. The migration history table migrate.Runner creates is the one
// catalog.FromDatabase skips by default, so it never reaches the store.
//
// It never writes into sample/taskboard, not even under -update-docs: that
// module regenerates itself with its own `go generate` (documented in
// CONTRIBUTING.md), and this test would otherwise be a second, silent way
// to change checked-in output outside the module that owns it. Calling
// requireGeneratedDirectoryMatches directly, instead of
// requireGeneratedDirectoryIsCurrent, is what keeps that true regardless of
// the -update-docs flag.
func TestTaskboardStoreIsCurrent(t *testing.T) {
	migrationsDir := filepath.Join(repositoryRoot, "sample", "taskboard", "migrations")
	migrations, err := migrationdir.Load(migrationsDir)
	require.NoError(t, err, "load migrations from %s", migrationsDir)

	dbPath := filepath.Join(t.TempDir(), "taskboard.db")
	database, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	runner, err := migrate.New(database, dialect.SQLite())
	require.NoError(t, err)
	applied, err := runner.Apply(ctx, migrate.AllPending(), migrations...)
	require.NoError(t, err)
	require.Len(t, applied, len(migrations))

	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)
	require.NotEmpty(t, tables, "%s applied no tables the sweep could see", migrationsDir)

	store := generate.Store{
		Package: "store",
		Root:    repositoryRootAbs(t),
		Dir:     filepath.Join("sample", "taskboard", "internal", "store"),
		Tables:  tables,
		Prune:   true,
	}

	requireGeneratedDirectoryMatches(t, store, filepath.Join(repositoryRoot, "sample", "taskboard", "internal", "store"))
}
