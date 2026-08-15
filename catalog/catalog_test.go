package catalog_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"os/exec"
	"path/filepath"
	"sync"
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

// TestFromDatabaseReportsACommitFailure pins the commit-error path
// FromDatabase's doc comment describes: the driver's error reaches the caller
// wrapped, and the connection the transaction held is back in the pool even
// though FromDatabase calls no Rollback after a failed Commit. It reads a real
// SQLite database through a driver that refuses only the commit, so every
// metadata read on the way there is the one the SQLite dialect really issues.
func TestFromDatabaseReportsACommitFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "catalog.db")
	setup, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	_, err = setup.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err)
	require.NoError(t, setup.Close())

	database, err := sql.Open(commitFailureDriverName, path)
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })

	tables, err := catalog.FromDatabase(context.Background(), database, catalog.Options{Dialect: dialect.SQLite()})
	require.Nil(t, tables)
	require.ErrorIs(t, err, errCommitRefused)
	require.ErrorContains(t, err, "catalog: commit inspection transaction")
	require.Zero(t, database.Stats().InUse,
		"a failed commit must leave no connection in use: database/sql finishes the transaction and releases its connection before Commit reports the error, which is why FromDatabase needs no Rollback there")
}

// commitFailureDriverName registers a driver that behaves exactly like the
// SQLite driver the rest of these tests use, except that committing any
// transaction fails. Nothing outside TestFromDatabaseReportsACommitFailure
// uses it.
const commitFailureDriverName = "sqlite-commit-failure"

var errCommitRefused = errors.New("commit refused by the test driver")

func init() {
	sql.Register(commitFailureDriverName, &commitFailureDriver{})
}

type commitFailureDriver struct {
	once  sync.Once
	inner driver.Driver
}

// Open borrows the registered SQLite driver rather than constructing one,
// because modernc.org/sqlite exports its driver type but not a usable value of
// it.
func (d *commitFailureDriver) Open(name string) (driver.Conn, error) {
	d.once.Do(func() {
		database, err := sql.Open("sqlite", ":memory:")
		if err != nil {
			return
		}
		d.inner = database.Driver()
		_ = database.Close()
	})
	if d.inner == nil {
		return nil, errors.New("the sqlite driver is unavailable")
	}
	connection, err := d.inner.Open(name)
	if err != nil {
		return nil, err
	}
	beginner, ok := connection.(driver.ConnBeginTx)
	if !ok {
		_ = connection.Close()
		return nil, errors.New("the sqlite driver does not begin transactions with options")
	}
	return &commitFailureConn{Conn: connection, beginner: beginner}, nil
}

type commitFailureConn struct {
	driver.Conn
	beginner driver.ConnBeginTx
}

func (c *commitFailureConn) BeginTx(ctx context.Context, options driver.TxOptions) (driver.Tx, error) {
	tx, err := c.beginner.BeginTx(ctx, options)
	if err != nil {
		return nil, err
	}
	return commitFailureTx{Tx: tx}, nil
}

type commitFailureTx struct {
	driver.Tx
}

// Commit ends the real transaction before reporting the failure, so the driver
// hands database/sql back a connection with nothing open on it. The error is
// not driver.ErrBadConn, so the connection returns to the pool and
// database/sql's own accounting is what the test observes.
func (tx commitFailureTx) Commit() error {
	_ = tx.Tx.Rollback()
	return errCommitRefused
}

var _ driver.Driver = (*commitFailureDriver)(nil)
var _ driver.Conn = (*commitFailureConn)(nil)
var _ driver.ConnBeginTx = (*commitFailureConn)(nil)
var _ driver.Tx = commitFailureTx{}

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
