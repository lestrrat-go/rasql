//go:build unix

package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// mysqlAccessDenied and mysqlSpecificAccessDenied are the go-sql-driver/mysql
// error numbers MySQL returns when the connected user lacks CREATE
// privilege: 1044 (ER_DBACCESS_DENIED_ERROR, "Access denied for user ... to
// database ...", raised for a specific schema) and 1227
// (ER_SPECIFIC_ACCESS_DENIED_ERROR, "Access denied; you need ... privilege
// ... for this operation", raised when no schema-specific privilege applies
// at all). Checked against mysql.MySQLError.Number -- a stable, documented
// value the driver already parses out of the server's response -- rather
// than matched against the error's free-text message.
const (
	mysqlAccessDenied         = 1044
	mysqlSpecificAccessDenied = 1227
)

// mysqlDbCreateExists is go-sql-driver/mysql's error number for
// ER_DB_CREATE_EXISTS, raised by CREATE DATABASE when a database by that
// name already exists.
const mysqlDbCreateExists = 1007

// mysqlCreateDatabaseErrorProvesNothingCreated reports whether err is a
// MySQL error that CREATE DATABASE can only return when this particular
// statement created nothing: the name already existed (mysqlDbCreateExists),
// or the connected user lacks CREATE privilege (mysqlAccessDenied or
// mysqlSpecificAccessDenied). createFreshMySQLDatabase clears its dropOwned
// flag only when this reports true, so the registered cleanup's DROP
// DATABASE never runs for a database this call did not create; see
// pgCreateDatabaseErrorProvesNothingCreated in postgresql.go for the full
// containment reasoning this mirrors.
//
// Unlike PostgreSQL, this needs no accepted-race case for the pre-existence
// probe (see pgUniqueViolation in postgresql.go for why PostgreSQL does):
// MySQL serializes CREATE DATABASE for a given schema name on its own
// metadata lock, so two concurrent creates of the same name never both
// proceed past the existence check -- the loser of that race gets
// mysqlDbCreateExists (1007) directly, the same code a simple pre-existing
// name produces, never a distinct "someone else won the race" code the way
// PostgreSQL's 23505 is distinct from 42P04.
//
// This is sound for the same underlying reason the PostgreSQL version is: a
// duplicate error here can never be this call's own earlier, successful
// CREATE DATABASE being reported back as a failure, because
// go-sql-driver/mysql does not retry a statement whose bytes already
// reached the wire either. go-sql-driver converts to database/sql's retry
// signal, driver.ErrBadConn, only its own internal errBadConnNoWrite --
// produced only when writing the statement to the connection failed after
// writing zero bytes -- and returns any failure discovered while reading
// the server's response (which is where a real ER_DB_CREATE_EXISTS is
// reported) unchanged, never converted to a retry signal. So whatever
// produced mysqlDbCreateExists here was not this call's own statement being
// silently retried and colliding with itself. This reasoning is
// version-dependent, tied to go-sql-driver/mysql's own errBadConnNoWrite
// contract; a later editor changing which codes are checked here must
// preserve the reasoning, not merely the list of codes.
func mysqlCreateDatabaseErrorProvesNothingCreated(err error) bool {
	var mysqlErr *mysql.MySQLError
	if !errors.As(err, &mysqlErr) {
		return false
	}
	switch mysqlErr.Number {
	case mysqlDbCreateExists, mysqlAccessDenied, mysqlSpecificAccessDenied:
		return true
	default:
		return false
	}
}

// mysqlSchemaExists reports whether a schema (database) named name already
// exists on the server admin is connected to. createFreshMySQLDatabase calls
// this before registering any cleanup for name (step 1 of the containment
// remedy), so a name that already exists is refused with nothing ever
// scheduled to be dropped for it.
func mysqlSchemaExists(ctx context.Context, admin *sql.DB, name string) (bool, error) {
	var discard int
	err := admin.QueryRowContext(ctx, "SELECT 1 FROM information_schema.schemata WHERE schema_name = ?", name).Scan(&discard)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

var mysqlConfigCache perTestCache[*mysql.Config]

// MySQLConfig returns a parsed MySQL connection configuration for a fresh,
// per-run database (a schema, in MySQL terms) created on a live server,
// resolved as described in the package doc. It skips the calling test
// rather than returning an error when RASQL_TEST_MYSQL_DSN is not set.
//
// Like PostgreSQLConfig, it returns the driver's own *mysql.Config rather
// than a DSN string, so a caller needing different credentials modifies the
// parsed struct instead of rebuilding and reparsing a string. See
// PostgreSQLConfig for the round-trip bug that guards against; nothing in
// this repository currently rebuilds a MySQL DSN, but returning the parsed
// form keeps that true rather than merely accidental.
//
// Calling this more than once for the same *testing.T (directly, or via
// MySQLDB) returns configuration for the SAME fresh database every time,
// rather than creating a second one; see perTestCache.
func MySQLConfig(t *testing.T) *mysql.Config {
	t.Helper()
	return mysqlConfigCache.resolve(t, func() *mysql.Config {
		server := resolveMySQLServerConfig(t)
		return createFreshMySQLDatabase(t, server)
	})
}

// MySQLDB opens a live MySQL connection via MySQLConfig, registers
// t.Cleanup to close it, and pings it before returning.
func MySQLDB(t *testing.T) *sql.DB {
	t.Helper()
	config := MySQLConfig(t)
	return openAndPing(t, mysqlOpenDB(t, config))
}

// mysqlOpenDB builds a *sql.DB from config through the driver's own
// connector, never through sql.Open(driverName, dsnString), so nothing
// rebuilds or reparses a DSN string.
func mysqlOpenDB(t *testing.T, config *mysql.Config) *sql.DB {
	t.Helper()
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatalf("dbtest: build mysql connector: %v", err)
	}
	return sql.OpenDB(connector)
}

// resolveMySQLServerConfig resolves a MySQL connection configuration the
// way the package doc describes -- a set RASQL_TEST_MYSQL_DSN parsed with
// the mysql driver's own parser -- without yet creating this run's own
// fresh database. createFreshMySQLDatabase does that next; see MySQLConfig.
func resolveMySQLServerConfig(t *testing.T) *mysql.Config {
	t.Helper()
	value, set := os.LookupEnv(mysqlEnvVar)
	trimmed, useValue, blank := dsnDecision(value, set)
	if !useValue {
		skipNoDSN(t, mysqlEnvVar, mysqlComposeDSN, blank)
		return nil
	}

	config, err := mysql.ParseDSN(trimmed)
	if err != nil {
		// Same reasoning as resolvePostgreSQLServerConfig's identical
		// branch: a set-but-unparseable DSN fails rather than falls
		// through.
		t.Fatalf("%s is set to a value the mysql driver could not parse: %v", mysqlEnvVar, err)
	}
	return config
}

// createFreshMySQLDatabase creates a database (a schema, in MySQL terms)
// named uniquely for this run on the server server describes, registers a
// t.Cleanup to drop it, and returns a copy of server pointed at the new
// database -- the configuration every live MySQL connection for this test
// should use.
//
// Unlike PostgreSQL, MySQL has no restriction on running CREATE DATABASE
// while connected (DDL simply causes an implicit commit if a transaction
// happens to be open; see MySQL's own manual, "Statements That Cause an
// Implicit Commit"), and a connection needs no pre-existing database
// selected at all to run one. So this connects through server exactly as
// given -- its DBName may be empty -- probes for name's pre-existence,
// issues CREATE DATABASE, then reconnects with DBName set to the new
// database to confirm it before handing back its configuration.
//
// The probe, the drop registration, and the drop-guard flag are
// createFreshDatabase's job (see its doc for the full containment
// ordering); this function supplies the MySQL-specific probe, create, and
// drop operations createFreshDatabase drives. The returned cleanup is
// registered with t.Cleanup before the returned error is inspected, so a
// Fatalf below still reaches it -- createFreshDatabase builds that cleanup,
// with dropOwned already decided, before it returns.
func createFreshMySQLDatabase(t *testing.T, server *mysql.Config) *mysql.Config {
	t.Helper()
	name := UniqueName(t, "rasql_test")

	admin := mysqlOpenDB(t, server)
	defer func() { _ = admin.Close() }()
	if err := admin.PingContext(t.Context()); err != nil {
		t.Fatalf("dbtest: connect via %s to create a fresh MySQL database: %v", mysqlEnvVar, err)
	}

	cleanup, err := createFreshDatabase(
		name,
		func() (bool, error) { return mysqlSchemaExists(t.Context(), admin, name) },
		func() error {
			_, err := admin.ExecContext(t.Context(), mysqlCreateDatabaseStatement(name))
			return err
		},
		mysqlCreateDatabaseErrorProvesNothingCreated,
		func() { dropMySQLDatabase(t, server, name) },
	)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && (mysqlErr.Number == mysqlAccessDenied || mysqlErr.Number == mysqlSpecificAccessDenied) {
			t.Fatalf("dbtest: %s's credentials cannot CREATE DATABASE; supply a DSN whose credentials can create and drop databases so the live suite can run inside its own database: %v", mysqlEnvVar, err)
		}
		// Any other failure -- including a pre-existing name, which a
		// per-run unique name colliding would mean something is genuinely
		// wrong worth seeing -- fails loudly rather than retrying under
		// another name.
		t.Fatalf("dbtest: %v", err)
	}

	fresh := server.Clone()
	fresh.DBName = name

	verify := mysqlOpenDB(t, fresh)
	defer func() { _ = verify.Close() }()
	if err := verify.PingContext(t.Context()); err != nil {
		t.Fatalf("dbtest: reconnect to fresh MySQL database %q: %v", name, err)
	}

	return fresh
}

// dropMySQLDatabase drops the fresh database name that
// createFreshMySQLDatabase created, connecting through server -- the same
// configuration used to create it, needing no database selected.
//
// This runs as a t.Cleanup, registered before every connection this
// package hands out against name (MySQLDB's own connection, and any copy
// callers make from MySQLConfig's result); Go runs t.Cleanup callbacks in
// last-registered-first-run order, so those connections' own
// Cleanup-registered (or defer-registered, which always runs before
// Cleanup) closes happen before this one runs.
func dropMySQLDatabase(t *testing.T, server *mysql.Config, name string) {
	t.Helper()
	// t.Context() is already canceled by the time a t.Cleanup function
	// runs, so this uses its own bounded context rather than t.Context().
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	admin := mysqlOpenDB(t, server)
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(ctx, mysqlDropDatabaseStatement(name)); err != nil {
		t.Errorf("dbtest: drop fresh MySQL database %q: %v", name, err)
	}
}

// mysqlCreateDatabaseStatement and mysqlDropDatabaseStatement build the
// exact SQL text createFreshMySQLDatabase and dropMySQLDatabase issue for
// name, split out as pure functions so a test can pin that both target
// exactly the same, correctly quoted identifier without needing a live
// server to connect to.
func mysqlCreateDatabaseStatement(name string) string {
	return "CREATE DATABASE " + mysqlQuoteIdentifier(name)
}

func mysqlDropDatabaseStatement(name string) string {
	return "DROP DATABASE IF EXISTS " + mysqlQuoteIdentifier(name)
}

// mysqlQuoteIdentifier backtick-quotes name for use as a MySQL identifier,
// doubling any backtick name itself contains the way MySQL's own identifier
// quoting requires. UniqueName never produces a backtick, but this stays
// correct even if that were not so, rather than relying on it.
func mysqlQuoteIdentifier(name string) string {
	return "`" + strings.ReplaceAll(name, "`", "``") + "`"
}
