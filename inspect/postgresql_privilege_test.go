//go:build unix

package inspect_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"

	"github.com/jackc/pgx/v5/stdlib"
)

// These two tests prove the premise behind inspect.go's pg_catalog
// cross-check against a real PostgreSQL server, rather than only exercising
// the Go code's reaction to a mocked short result set:
//
//   - information_schema.columns filters each row through
//     has_column_privilege, so a role granted SELECT on only some of a
//     table's columns sees only those columns through information_schema.
//   - information_schema.table_constraints omits SELECT from the privilege
//     list it checks, so a plain SELECT-only role sees no primary-key
//     constraint through information_schema at all.
//
// Both tests query information_schema directly as the restricted role
// before asking the inspector anything, so a passing test demonstrates the
// actual privilege gap rather than merely a coincidentally correct result.

// TestPostgreSQLInspectorRejectsPartialColumnPrivilege covers the first
// case: a role with SELECT on only one of three columns.
func TestPostgreSQLInspectorRejectsPartialColumnPrivilege(t *testing.T) {
	ctx := t.Context()
	admin := dbtest.PostgreSQLDB(t)

	tableName := uniquePostgreSQLName(t, "rasql_priv_partial")
	roleName := uniquePostgreSQLName(t, "rasql_priv_partial_role")

	mustExec(t, ctx, admin, fmt.Sprintf(`CREATE TABLE %s (id integer PRIMARY KEY, email text NOT NULL, ssn text NOT NULL)`, tableName))
	defer mustExec(t, ctx, admin, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tableName))

	createRestrictedRole(t, ctx, admin, roleName)
	defer dropRestrictedRole(t, ctx, admin, roleName)

	mustExec(t, ctx, admin, fmt.Sprintf(`GRANT SELECT (id) ON %s TO %s`, tableName, roleName))

	restricted := openAsRole(t, roleName)
	defer func() { require.NoError(t, restricted.Close()) }()

	// Prove the premise at the SQL level: information_schema.columns must
	// actually be short for this role, or the inspector assertions below
	// would pass for the wrong reason.
	visible := countRows(t, ctx, restricted, fmt.Sprintf(`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '%s'`, tableName))
	actual := countRows(t, ctx, restricted, fmt.Sprintf(`SELECT attribute.attname FROM pg_catalog.pg_attribute AS attribute JOIN pg_catalog.pg_class AS table_data ON table_data.oid = attribute.attrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace WHERE table_namespace.nspname = current_schema() AND table_data.relname = '%s' AND attribute.attnum > 0 AND NOT attribute.attisdropped`, tableName))
	require.Equal(t, 1, visible, "the role granted SELECT on one column should see exactly one row through information_schema.columns")
	require.Equal(t, 3, actual, "pg_catalog carries no column-privilege filter and should report all three columns")

	inspector, err := inspect.New(restricted, dialect.PostgreSQL())
	require.NoError(t, err)
	_, err = inspector.Table(ctx, tableName)
	require.ErrorIs(t, err, inspect.ErrIncompleteMetadata)
	var incomplete *inspect.IncompleteMetadataError
	require.ErrorAs(t, err, &incomplete)
	require.Equal(t, tableName, incomplete.Table)
	require.Equal(t, 1, incomplete.Visible)
	require.Equal(t, 3, incomplete.Actual)
}

// TestPostgreSQLInspectorReadsPrimaryKeyThroughTableLevelSelect covers the
// second case: a role with plain table-level SELECT, which is enough to see
// every column but nothing in information_schema.table_constraints.
func TestPostgreSQLInspectorReadsPrimaryKeyThroughTableLevelSelect(t *testing.T) {
	ctx := t.Context()
	admin := dbtest.PostgreSQLDB(t)

	tableName := uniquePostgreSQLName(t, "rasql_priv_pk")
	roleName := uniquePostgreSQLName(t, "rasql_priv_pk_role")

	mustExec(t, ctx, admin, fmt.Sprintf(`CREATE TABLE %s (id integer PRIMARY KEY, email text NOT NULL)`, tableName))
	defer mustExec(t, ctx, admin, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tableName))

	createRestrictedRole(t, ctx, admin, roleName)
	defer dropRestrictedRole(t, ctx, admin, roleName)

	mustExec(t, ctx, admin, fmt.Sprintf(`GRANT SELECT ON %s TO %s`, tableName, roleName))

	restricted := openAsRole(t, roleName)
	defer func() { require.NoError(t, restricted.Close()) }()

	// Prove the premise: information_schema.table_constraints omits SELECT
	// from its privilege list, so this plain read-only role sees zero
	// primary-key rows through information_schema even though it can read
	// every column.
	//
	// The zero-rows assertion alone cannot tell "the privilege filter hid
	// the primary key" apart from "this connection reached the wrong or an
	// empty database" -- both produce zero rows. The column-visibility
	// check below is a positive control: it must see both columns of this
	// specific table, which only happens if the connection landed on the
	// intended database and the table exists there. Zero PRIMARY KEY rows
	// alongside two visible columns is the actual signature of the
	// privilege gap under test.
	visibleColumns := countRows(t, ctx, restricted, fmt.Sprintf(`SELECT column_name FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = '%s'`, tableName))
	require.Equal(t, 2, visibleColumns, "the role should see both columns of the table it was granted table-level SELECT on, confirming the connection reached the intended database and table")

	pkRows := countRows(t, ctx, restricted, fmt.Sprintf(`SELECT constraint_name FROM information_schema.table_constraints WHERE table_schema = current_schema() AND table_name = '%s' AND constraint_type = 'PRIMARY KEY'`, tableName))
	require.Equal(t, 0, pkRows, "information_schema.table_constraints should expose no PRIMARY KEY row to a plain SELECT-only role")

	inspector, err := inspect.New(restricted, dialect.PostgreSQL())
	require.NoError(t, err)
	table, err := inspector.Table(ctx, tableName)
	require.NoError(t, err)
	require.Equal(t, []string{"id"}, table.PrimaryKey)
	require.Equal(t, []schema.Column{
		{Name: "id", Type: schema.TypeInteger},
		{Name: "email", Type: schema.TypeText},
	}, table.Columns)
}

// uniquePostgreSQLName generates a name unlikely to collide with another
// concurrent test run, since roles are cluster-wide rather than per-database
// and would otherwise collide across runs if ever leaked.
func uniquePostgreSQLName(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s_%d_%d", prefix, os.Getpid(), time.Now().UnixNano())
}

func mustExec(t *testing.T, ctx context.Context, db *sql.DB, statement string) {
	t.Helper()
	_, err := db.ExecContext(ctx, statement)
	require.NoError(t, err)
}

func countRows(t *testing.T, ctx context.Context, db *sql.DB, statement string) int {
	t.Helper()
	var count int
	require.NoError(t, db.QueryRowContext(ctx, fmt.Sprintf(`SELECT count(*) FROM (%s) AS probe`, statement)).Scan(&count))
	return count
}

// createRestrictedRole creates a LOGIN role whose password is its own name,
// so tests can connect as it directly instead of only impersonating it
// within the admin session.
func createRestrictedRole(t *testing.T, ctx context.Context, admin *sql.DB, roleName string) {
	t.Helper()
	mustExec(t, ctx, admin, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, roleName, roleName))
}

// dropRestrictedRole cleans up a role created by createRestrictedRole. A
// role cannot be dropped while it still owns privileges, so DROP OWNED BY
// strips whatever this test granted it before DROP ROLE runs.
func dropRestrictedRole(t *testing.T, ctx context.Context, admin *sql.DB, roleName string) {
	t.Helper()
	mustExec(t, ctx, admin, fmt.Sprintf(`DROP OWNED BY %s`, roleName))
	mustExec(t, ctx, admin, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, roleName))
}

// openAsRole connects to the same server dbtest.PostgreSQLDB used, but
// authenticated as roleName instead of the admin credentials.
//
// dbtest.PostgreSQLConfig hands back pgx's own already-parsed *ConnConfig
// rather than a DSN string, specifically so this can copy it and override
// only User/Password, never rebuild and reparse a connection string. An
// earlier version of this test did exactly that: it parsed the raw DSN
// with net/url and re-serialized it with a userinfo set, which turned
// spaces in a keyword/value DSN ("host=... port=... dbname=...") into
// "%20" inside a bare "//user:pass@..." authority -- a string pgx.ParseConfig
// happily accepted and resolved to an unrelated connection (a Unix socket,
// no database, the OS user) instead of erroring out. Copying the parsed
// config sidesteps that whole class of bug: there is no string left to
// round-trip.
func openAsRole(t *testing.T, roleName string) *sql.DB {
	t.Helper()
	config := dbtest.PostgreSQLConfig(t).Copy()
	config.User = roleName
	config.Password = roleName
	database := stdlib.OpenDB(*config)
	require.NoError(t, database.PingContext(t.Context()))
	return database
}
