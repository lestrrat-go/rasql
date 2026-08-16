//go:build unix

package catalog_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// mustExecLive runs statement against database and fails the test on error.
func mustExecLive(t *testing.T, ctx context.Context, database *sql.DB, statement string) {
	t.Helper()
	_, err := database.ExecContext(ctx, statement)
	require.NoError(t, err)
}

// TestFromDatabaseSweepsLivePostgreSQL mirrors
// cli/rasqlgen/schema_sweep_integration_test.go: it drives FromDatabase
// against a real PostgreSQL server, not sqlmock or an in-process SQLite
// stand-in, and requires the sweep to describe the ordinary table and skip
// both the migration history table and the view. This also settles, in CI,
// that PostgreSQL's driver accepts the LevelRepeatableRead read-only
// transaction FromDatabase opens -- a question no fixture test can answer
// and nothing on this machine can check, since no PostgreSQL server is
// reachable here.
func TestFromDatabaseSweepsLivePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, email text NOT NULL, active boolean NOT NULL DEFAULT true, created_at timestamp NOT NULL)")
	mustExecLive(t, ctx, database, "CREATE TABLE rasql_schema_migrations (id text PRIMARY KEY)")
	mustExecLive(t, ctx, database, "CREATE VIEW active_users AS SELECT id FROM users WHERE active")

	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.PostgreSQL()})
	require.NoError(t, err)
	require.Len(t, tables, 1)
	require.Equal(t, "users", tables[0].Name)
}

// TestFromDatabaseSweepsLiveMySQL is
// TestFromDatabaseSweepsLivePostgreSQL's MySQL counterpart. Both settle, in
// CI only, whether the driver accepts the read-only LevelRepeatableRead
// transaction FromDatabase opens; nothing here is reported as verified
// against MySQL until that job runs, since MySQL is unreachable from this
// machine too.
func TestFromDatabaseSweepsLiveMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	ctx := t.Context()
	mustExecLive(t, ctx, database, "CREATE TABLE users (id integer PRIMARY KEY, email varchar(255) NOT NULL, active boolean NOT NULL DEFAULT true, created_at timestamp NOT NULL)")
	mustExecLive(t, ctx, database, "CREATE TABLE rasql_schema_migrations (id varchar(255) PRIMARY KEY)")
	mustExecLive(t, ctx, database, "CREATE VIEW active_users AS SELECT id FROM users WHERE active")

	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{Dialect: dialect.MySQL()})
	require.NoError(t, err)
	require.Len(t, tables, 1)
	require.Equal(t, "users", tables[0].Name)
}
