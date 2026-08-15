package examples_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
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
// with catalog.FromDatabase exactly the way sample/taskboard/gen/main.go
// will, and checks the result against sample/taskboard/internal/store
// through the same directory comparison requireGeneratedDirectoryMatches
// performs for examples/store and examples/schemasource.
//
// It never writes into sample/taskboard, not even under -update-docs: that
// module regenerates itself with its own `go generate` (documented in
// CONTRIBUTING.md), and this test would otherwise be a second, silent way
// to change checked-in output outside the module that owns it. Calling
// requireGeneratedDirectoryMatches directly, instead of
// requireGeneratedDirectoryIsCurrent, is what keeps that true regardless of
// the -update-docs flag.
func TestTaskboardStoreIsCurrent(t *testing.T) {
	migrationsDir := filepath.Join(repositoryRoot, "sample", "taskboard", "migrations", "sqlite", "001_initial")
	entries, err := os.ReadDir(migrationsDir)
	require.NoError(t, err)

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	require.NotEmpty(t, names, "no migrations found in %s", migrationsDir)

	dbPath := filepath.Join(t.TempDir(), "taskboard.db")
	database, err := sql.Open("sqlite", dbPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	ctx := context.Background()
	for _, name := range names {
		contents, err := os.ReadFile(filepath.Join(migrationsDir, name))
		require.NoError(t, err)
		for _, statement := range strings.Split(string(contents), ";") {
			statement = strings.TrimSpace(statement)
			if statement == "" {
				continue
			}
			_, err := database.ExecContext(ctx, statement)
			require.NoErrorf(t, err, "apply %s: %s", name, statement)
		}
	}

	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)
	require.Len(t, tables, 3, "expected members, projects, and tasks")

	store := generate.Store{
		Package: "store",
		Root:    repositoryRootAbs(t),
		Dir:     filepath.Join("sample", "taskboard", "internal", "store"),
		Tables:  tables,
	}

	requireGeneratedDirectoryMatches(t, store, filepath.Join(repositoryRoot, "sample", "taskboard", "internal", "store"))
}
