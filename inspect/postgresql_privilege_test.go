//go:build unix

package inspect_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"

	"github.com/jackc/pgx/v5/pgconn"
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

	tableName := dbtest.UniqueName(t, "rasql_priv_partial")
	roleName := dbtest.UniqueName(t, "rasql_priv_partial_role")

	mustExec(t, ctx, admin, fmt.Sprintf(`CREATE TABLE %s (id integer PRIMARY KEY, email text NOT NULL, ssn text NOT NULL)`, tableName))
	defer mustExec(t, ctx, admin, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tableName))

	createRestrictedRole(t, ctx, admin, roleName)

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

	tableName := dbtest.UniqueName(t, "rasql_priv_pk")
	roleName := dbtest.UniqueName(t, "rasql_priv_pk_role")

	mustExec(t, ctx, admin, fmt.Sprintf(`CREATE TABLE %s (id integer PRIMARY KEY, email text NOT NULL)`, tableName))
	defer mustExec(t, ctx, admin, fmt.Sprintf(`DROP TABLE IF EXISTS %s`, tableName))

	createRestrictedRole(t, ctx, admin, roleName)

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
	require.Equal(t, []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}},
	}, table.Columns)
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

// pgDuplicateObject is PostgreSQL's SQLSTATE for "duplicate_object" (see
// errcodes.txt in PostgreSQL's own source). CREATE ROLE has no role-specific
// duplicate code and no IF NOT EXISTS clause; PostgreSQL's own
// src/backend/commands/user.c raises this generic code directly from an
// explicit get_role_oid pre-check when the name already exists.
//
// createRestrictedRole's own pgRoleExists probe already refuses a
// pre-existing name before any cleanup is registered (mirroring
// dbtest.pgDatabaseExists / dbtest's createFreshDatabase step 1 in
// internal/dbtest/dbtest.go), so this code firing after that probe passed
// can only mean another process created the exact same UniqueName-derived
// name in the narrow window between the probe and this CREATE ROLE.
const pgDuplicateObject = "42710"

// pgUniqueViolation is PostgreSQL's SQLSTATE for "unique_violation".
// PostgreSQL's CreateRole checks for a duplicate name before taking any
// lock on pg_authid (its RowExclusiveLock on pg_authid is acquired only
// afterward, to perform the insert), so two concurrent CREATE ROLE calls
// for the same name can both pass the pre-check and race at the insert's
// unique index instead -- the loser sees this code rather than
// pgDuplicateObject. See dbtest.pgUniqueViolation in
// internal/dbtest/postgresql.go for the identical accepted race this
// mirrors for CREATE DATABASE.
const pgUniqueViolation = "23505"

// pgInsufficientPrivilege is PostgreSQL's SQLSTATE for
// "insufficient_privilege", the code CREATE ROLE returns when the connected
// role lacks CREATEROLE. dbtest.pgCreateDatabaseErrorProvesNothingCreated in
// internal/dbtest/postgresql.go treats the same code the same way for
// CREATE DATABASE, for the same reason: the statement was rejected before
// anything was created.
const pgInsufficientPrivilege = "42501"

// createRoleErrorProvesNothingCreated reports whether err is a PostgreSQL
// error CREATE ROLE can only return when this particular statement created
// nothing: the name already existed (pgDuplicateObject, or pgUniqueViolation
// for the accepted pre-check/insert race -- see their comments above), or
// admin lacks CREATEROLE (pgInsufficientPrivilege). createRestrictedRole
// clears its dropOwned flag only when this reports true, so the registered
// cleanup's DROP OWNED BY/DROP ROLE never runs for a role this call did not
// create -- see dbtest.pgCreateDatabaseErrorProvesNothingCreated in
// internal/dbtest/postgresql.go for the full soundness argument (no driver
// this package uses retries a statement whose bytes already reached the
// wire), which applies here unchanged.
func createRoleErrorProvesNothingCreated(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case pgDuplicateObject, pgUniqueViolation, pgInsufficientPrivilege:
		return true
	default:
		return false
	}
}

// pgRoleExists reports whether a role named name already exists on the
// server admin is connected to. createRestrictedRole calls this before
// registering any cleanup for name, so a name that already exists is
// refused with nothing ever scheduled to be dropped for it -- the same
// step 1 dbtest.pgDatabaseExists performs for the per-run database in
// internal/dbtest/postgresql.go.
func pgRoleExists(ctx context.Context, admin *sql.DB, name string) (bool, error) {
	var discard int
	err := admin.QueryRowContext(ctx, "SELECT 1 FROM pg_roles WHERE rolname = $1", name).Scan(&discard)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

// restrictedRoleCleanupTimeout bounds a t.Cleanup-registered role drop. It
// cannot use t.Context(): testing.T.Context is already canceled by the time
// Cleanup-registered functions run, so a drop that used it would fail
// immediately with a canceled-context error instead of ever reaching the
// server. See dbtest.cleanupTimeout in internal/dbtest/dbtest.go for the
// identical reasoning.
const restrictedRoleCleanupTimeout = 30 * time.Second

// createRestrictedRoleFlow implements the containment ordering
// createRestrictedRole builds on, factored out into driver-agnostic
// function-parameter form -- the same shape dbtest.createFreshDatabase in
// internal/dbtest/dbtest.go uses for the per-run database -- so the ordering
// itself, not the SQL a live server would speak, can be exercised by a test
// with fake probeExists/create/drop functions and no live server. See
// TestCreateRestrictedRoleFlow.
//
// The three steps, matching dbtest.createFreshDatabase's containment
// ordering (a role is cluster-wide, so dropping the per-run database this
// test's admin connection lives in cannot remove it):
//
//  1. probeExists runs first. If it reports the name already exists, this
//     returns immediately with a nil cleanup and an error: nothing is ever
//     registered for the caller to run, so a pre-existing name can never be
//     dropped by this call, not even by accident.
//  2. A local dropOwned flag, and a cleanup closure that reads it, are built
//     before create runs -- so a caller that registers the returned cleanup
//     before inspecting the returned error (as createRestrictedRole does)
//     still has it in place even for a lost response: CREATE ROLE commits
//     on the server but the client never sees the reply.
//  3. If create fails, errorProvesNothingCreated decides whether that
//     failure proves create made nothing: if so, dropOwned is cleared so
//     the returned cleanup becomes a no-op. Otherwise dropOwned stays true,
//     so cleanup still attempts the drop: the role may have been created
//     despite the client-visible error, and leaking it is the safe failure
//     mode, not dropping a role this call did not create.
//
// On success, cleanup drops the role this call created and err is nil.
func createRestrictedRoleFlow(name string, probeExists func() (bool, error), create func() error, errorProvesNothingCreated func(error) bool, drop func()) (func(), error) {
	exists, err := probeExists()
	if err != nil {
		return nil, fmt.Errorf("check whether a role named %q already exists: %w", name, err)
	}
	if exists {
		return nil, fmt.Errorf("a role named %q already exists; refusing to touch it (per-run names must be unique -- see dbtest.UniqueName)", name)
	}

	dropOwned := true
	cleanup := func() {
		if dropOwned {
			drop()
		}
	}

	if err := create(); err != nil {
		if errorProvesNothingCreated(err) {
			dropOwned = false
		}
		return cleanup, fmt.Errorf("create restricted role %q: %w", name, err)
	}
	return cleanup, nil
}

// createRestrictedRole creates a LOGIN role whose password is its own name,
// so tests can connect as it directly instead of only impersonating it
// within the admin session. See createRestrictedRoleFlow for the
// containment ordering this drives.
func createRestrictedRole(t *testing.T, ctx context.Context, admin *sql.DB, roleName string) {
	t.Helper()

	cleanup, err := createRestrictedRoleFlow(
		roleName,
		func() (bool, error) { return pgRoleExists(ctx, admin, roleName) },
		func() error {
			_, err := admin.ExecContext(ctx, fmt.Sprintf(`CREATE ROLE %s LOGIN PASSWORD '%s'`, roleName, roleName))
			return err
		},
		createRoleErrorProvesNothingCreated,
		func() { dropRestrictedRole(t, admin, roleName) },
	)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	require.NoError(t, err)
}

// dropRestrictedRole cleans up a role created by createRestrictedRole. A
// role cannot be dropped while it still owns privileges, so DROP OWNED BY
// strips whatever this test granted it before DROP ROLE runs.
//
// Every step below is attempted regardless of an earlier step's failure,
// and every failure is reported through t.Errorf rather than a FailNow-style
// assertion: a FailNow-style failure on DROP OWNED BY would skip DROP ROLE
// entirely and leak the cluster-wide role, and today's caller (t.Cleanup)
// runs on the test goroutine after the test body has already returned, so a
// FailNow-style call here would report only the first failure and hide the
// second.
func dropRestrictedRole(t *testing.T, admin *sql.DB, roleName string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), restrictedRoleCleanupTimeout)
	defer cancel()

	dropRestrictedRoleSteps(
		func(msg string) { t.Errorf("dbtest: %s", msg) },
		func() error { _, err := admin.ExecContext(ctx, fmt.Sprintf(`DROP OWNED BY %s`, roleName)); return err },
		func() error {
			_, err := admin.ExecContext(ctx, fmt.Sprintf(`DROP ROLE IF EXISTS %s`, roleName))
			return err
		},
	)
}

// dropRestrictedRoleSteps runs dropRestrictedRole's two cleanup statements,
// DROP OWNED BY then DROP ROLE, in function-parameter form so a test can
// exercise the ordering with fake step functions and no live server -- see
// TestDropRestrictedRoleSteps. dropRole always runs, even when dropOwned
// fails, and every failure is reported through report rather than a
// FailNow-style call: a FailNow-style failure on the first step would skip
// the second and leak the cluster-wide role, and would also hide the
// second failure's own message from whoever reads the test output.
func dropRestrictedRoleSteps(report func(msg string), dropOwned func() error, dropRole func() error) {
	if err := dropOwned(); err != nil {
		report(fmt.Sprintf("drop owned by role: %v", err))
	}
	if err := dropRole(); err != nil {
		report(fmt.Sprintf("drop role: %v", err))
	}
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
