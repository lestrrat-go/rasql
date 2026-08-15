package catalog_test

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/stretchr/testify/require"

	_ "modernc.org/sqlite"
)

// mustCreateSQLiteDB opens a fresh file-backed SQLite database, applies
// every statement, and registers t.Cleanup to close it. A file rather than
// ":memory:" is deliberate: database/sql's connection pool can hand out more
// than one connection, and every ":memory:" connection is its own separate,
// empty database, which would make a second connection (as
// TestFromQueryerReadsThroughTheCallersTransaction opens) see none of the
// tables the first one created.
func mustCreateSQLiteDB(t *testing.T, statements ...string) *sql.DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "catalog.db")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	for _, statement := range statements {
		_, err = database.ExecContext(t.Context(), statement)
		require.NoError(t, err)
	}
	return database
}

func TestFromDatabaseSweepsEveryBaseTable(t *testing.T) {
	database := mustCreateSQLiteDB(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id))",
		"CREATE TABLE rasql_schema_migrations (id TEXT PRIMARY KEY)",
		"CREATE VIEW user_orders AS SELECT users.id FROM users JOIN orders ON orders.user_id = users.id",
	)

	tables, err := catalog.FromDatabase(context.Background(), database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)
	require.Len(t, tables, 2)
	require.Equal(t, "orders", tables[0].Name)
	require.Equal(t, "users", tables[1].Name)
}

func TestFromDatabaseIncludeDescribesExactlyWhatWasNamed(t *testing.T) {
	database := mustCreateSQLiteDB(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id))",
	)

	t.Run("one table", func(t *testing.T) {
		tables, err := catalog.FromDatabase(context.Background(), database, catalog.Options{
			Dialect: dialect.SQLite(),
			Include: []string{"users"},
		})
		require.NoError(t, err)
		require.Len(t, tables, 1)
		require.Equal(t, "users", tables[0].Name)
	})

	t.Run("unknown table", func(t *testing.T) {
		_, err := catalog.FromDatabase(context.Background(), database, catalog.Options{
			Dialect: dialect.SQLite(),
			Include: []string{"nope"},
		})
		require.Error(t, err)
		require.True(t, errors.Is(err, inspect.ErrTableNotFound), "got %v", err)
	})
}

func TestFromDatabaseExcludeSkipsARenamedHistoryTable(t *testing.T) {
	t.Run("Exclude", func(t *testing.T) {
		database := mustCreateSQLiteDB(t,
			"CREATE TABLE users (id INTEGER PRIMARY KEY)",
			"CREATE TABLE app_migrations (id TEXT PRIMARY KEY)",
		)
		tables, err := catalog.FromDatabase(context.Background(), database, catalog.Options{
			Dialect: dialect.SQLite(),
			Exclude: []string{"app_migrations"},
		})
		require.NoError(t, err)
		require.Len(t, tables, 1)
		require.Equal(t, "users", tables[0].Name)
	})

	t.Run("HistoryTable", func(t *testing.T) {
		database := mustCreateSQLiteDB(t,
			"CREATE TABLE users (id INTEGER PRIMARY KEY)",
			"CREATE TABLE app_migrations (id TEXT PRIMARY KEY)",
		)
		tables, err := catalog.FromDatabase(context.Background(), database, catalog.Options{
			Dialect:      dialect.SQLite(),
			HistoryTable: "app_migrations",
		})
		require.NoError(t, err)
		require.Len(t, tables, 1)
		require.Equal(t, "users", tables[0].Name)
	})
}

func TestFromDatabaseRejectsIncludeWithExclude(t *testing.T) {
	database := mustCreateSQLiteDB(t, "CREATE TABLE users (id INTEGER PRIMARY KEY)")

	t.Run("Include and Exclude together", func(t *testing.T) {
		_, err := catalog.FromDatabase(context.Background(), database, catalog.Options{
			Dialect: dialect.SQLite(),
			Include: []string{"users"},
			Exclude: []string{"orders"},
		})
		require.ErrorContains(t, err, "options.Include and options.Exclude must not both be set")
	})

	t.Run("duplicate name in Include", func(t *testing.T) {
		_, err := catalog.FromDatabase(context.Background(), database, catalog.Options{
			Dialect: dialect.SQLite(),
			Include: []string{"users", "users"},
		})
		require.ErrorContains(t, err, `options.Include has duplicate table name "users"`)
	})

	t.Run("duplicate name in Exclude", func(t *testing.T) {
		_, err := catalog.FromDatabase(context.Background(), database, catalog.Options{
			Dialect: dialect.SQLite(),
			Exclude: []string{"orders", "orders"},
		})
		require.ErrorContains(t, err, `options.Exclude has duplicate table name "orders"`)
	})
}

func TestFromDatabaseReportsErrNoTables(t *testing.T) {
	database := mustCreateSQLiteDB(t, "CREATE TABLE rasql_schema_migrations (id TEXT PRIMARY KEY)")

	_, err := catalog.FromDatabase(context.Background(), database, catalog.Options{Dialect: dialect.SQLite()})
	require.Error(t, err)
	require.True(t, errors.Is(err, catalog.ErrNoTables), "got %v", err)
}

func TestFromDatabaseSortsByName(t *testing.T) {
	database := mustCreateSQLiteDB(t,
		"CREATE TABLE zebra (id INTEGER PRIMARY KEY)",
		"CREATE TABLE apple (id INTEGER PRIMARY KEY)",
	)

	t.Run("sweep", func(t *testing.T) {
		tables, err := catalog.FromDatabase(context.Background(), database, catalog.Options{Dialect: dialect.SQLite()})
		require.NoError(t, err)
		require.Len(t, tables, 2)
		require.Equal(t, "apple", tables[0].Name)
		require.Equal(t, "zebra", tables[1].Name)
	})

	t.Run("Include given in reverse order", func(t *testing.T) {
		tables, err := catalog.FromDatabase(context.Background(), database, catalog.Options{
			Dialect: dialect.SQLite(),
			Include: []string{"zebra", "apple"},
		})
		require.NoError(t, err)
		require.Len(t, tables, 2)
		require.Equal(t, "apple", tables[0].Name)
		require.Equal(t, "zebra", tables[1].Name)
	})
}

func TestFromDatabaseRejectsNilHandleAndNilDialect(t *testing.T) {
	t.Run("nil handle", func(t *testing.T) {
		_, err := catalog.FromDatabase(context.Background(), nil, catalog.Options{Dialect: dialect.SQLite()})
		require.ErrorContains(t, err, "db must not be nil")
	})

	t.Run("nil dialect", func(t *testing.T) {
		database := mustCreateSQLiteDB(t, "CREATE TABLE users (id INTEGER PRIMARY KEY)")
		_, err := catalog.FromDatabase(context.Background(), database, catalog.Options{})
		require.ErrorContains(t, err, "options.Dialect must not be nil")
	})
}

func TestFromQueryerReadsThroughTheCallersTransaction(t *testing.T) {
	database := mustCreateSQLiteDB(t,
		"CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)",
		"CREATE TABLE orders (id INTEGER PRIMARY KEY, user_id INTEGER NOT NULL REFERENCES users (id))",
	)

	expected, err := catalog.FromDatabase(context.Background(), database, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)

	tx, err := database.BeginTx(context.Background(), &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	require.NoError(t, err)
	t.Cleanup(func() { _ = tx.Rollback() })

	got, err := catalog.FromQueryer(context.Background(), tx, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)
	require.Equal(t, expected, got)
}

// TestCatalogImportsNoDriverAndNoGenerator holds the package's central
// promise in place: importing catalog must add no database driver, and no
// dependency on generate, to the importing module's graph. It fails the
// moment someone adds a driver import for convenience anywhere beneath
// catalog, including transitively through inspect.
func TestCatalogImportsNoDriverAndNoGenerator(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", "github.com/lestrrat-go/rasql/catalog").CombinedOutput()
	require.NoError(t, err, "%s", output)
	forbidden := []string{
		"github.com/jackc/pgx",
		"github.com/go-sql-driver/mysql",
		"modernc.org/sqlite",
		"github.com/lestrrat-go/rasql/generate",
	}
	for _, dependency := range forbidden {
		require.NotContains(t, string(output), dependency,
			"catalog must not depend on %s: catalog is imported by a user's own generator program, and any driver or generate dependency here lands in every such module with no way to opt out", dependency)
	}
}

// TestGenerateDoesNotImportCatalog states the other half of the layer
// split: catalog must not import generate, and generate must not import
// catalog.
func TestGenerateDoesNotImportCatalog(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", "github.com/lestrrat-go/rasql/generate").CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.NotContains(t, string(output), "github.com/lestrrat-go/rasql/catalog",
		"generate must not depend on rasql/catalog: catalog is the database-metadata layer and generate is the code-generation layer, and the two must stay independently importable")
}
