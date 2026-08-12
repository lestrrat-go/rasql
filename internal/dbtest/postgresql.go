//go:build unix

package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// pgInsufficientPrivilege is PostgreSQL's SQLSTATE for "insufficient
// privilege" (see the errcodes.txt table in PostgreSQL's own source), the
// error CREATE DATABASE returns when the connected role lacks CREATEDB.
// Checked against pgconn.PgError.Code -- a stable, documented value the
// driver already parses out of the server's response -- rather than
// matched against the error's free-text message.
const pgInsufficientPrivilege = "42501"

// pgDuplicateDatabase is PostgreSQL's SQLSTATE for "duplicate_database",
// raised by CREATE DATABASE when a database by that name already exists.
const pgDuplicateDatabase = "42P04"

// pgUniqueViolation is PostgreSQL's SQLSTATE for "unique_violation". CREATE
// DATABASE ordinarily raises duplicate_database (42P04) for a name that
// already exists, but createFreshPostgreSQLDatabase's own pre-existence
// probe (pgDatabaseExists) has an accepted race: it runs, finds the name
// free, and only then does CREATE DATABASE run, so another process could in
// principle create the exact same name in between. PostgreSQL's own unique
// index on pg_database is what actually enforces uniqueness in that narrow
// window, and the loser of that race sees unique_violation rather than
// duplicate_database. UniqueName's name (process ID plus a nanosecond
// timestamp) makes a genuine collision here vanishingly unlikely, but if it
// ever happens, this run's own CREATE still had its statement rejected by
// the server without creating anything -- see the comment on
// pgCreateDatabaseErrorProvesNothingCreated for why that holds regardless of
// which of these two codes the server happens to surface -- so 23505 is
// treated identically to 42P04 here.
const pgUniqueViolation = "23505"

// pgCreateDatabaseErrorProvesNothingCreated reports whether err is a
// PostgreSQL error that CREATE DATABASE can only return when this
// particular statement created nothing: the name already existed
// (pgDuplicateDatabase, or pgUniqueViolation for the accepted probe race --
// see its comment above), or the connected role lacks CREATEDB
// (pgInsufficientPrivilege). createFreshPostgreSQLDatabase clears its
// dropOwned flag only when this reports true, so the registered cleanup's
// DROP DATABASE never runs for a database this call did not create -- see
// the containment property described in the package doc.
//
// This is sound only because a duplicate-name error here can never be
// caused by THIS call's own earlier, successful CREATE DATABASE having its
// result reported back as a failure: neither driver this package uses
// retries a statement whose bytes already reached the wire, which is the
// only way that could happen.
//
//   - pgx (github.com/jackc/pgx/v5) returns database/sql's driver.ErrBadConn
//     -- the signal database/sql uses to retry a statement transparently on
//     a fresh connection -- only when pgconn.SafeToRetry(err) reports true,
//     which pgconn documents as guaranteed to mean the failure happened
//     before any data for the statement was sent. If CREATE DATABASE's
//     bytes reached the server, SafeToRetry is false and database/sql never
//     retries it.
//   - go-sql-driver/mysql has the equivalent property through a different
//     mechanism, for the same reason; see
//     mysqlCreateDatabaseErrorProvesNothingCreated in mysql.go.
//
// So whatever produced a duplicate/unique-violation/insufficient-privilege
// error here, it was not this call's own statement being silently retried
// and colliding with itself; something else -- a pre-existing database (the
// probe's accepted race, or a broken uniqueness assumption in UniqueName)
// or a genuinely unprivileged credential -- is the only explanation left.
// That is what makes it safe to conclude "nothing was created" and clear
// dropOwned. This reasoning is version-dependent (SafeToRetry's contract
// belongs to pgconn, not to database/sql itself); a later editor changing
// which codes are checked here must preserve the reasoning, not merely the
// list of codes.
func pgCreateDatabaseErrorProvesNothingCreated(err error) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	switch pgErr.Code {
	case pgDuplicateDatabase, pgUniqueViolation, pgInsufficientPrivilege:
		return true
	default:
		return false
	}
}

// pgDatabaseExists reports whether a database named name already exists on
// the server admin is connected to. createFreshPostgreSQLDatabase calls this
// before registering any cleanup for name (step 1 of the containment
// remedy), so a name that already exists is refused with nothing ever
// scheduled to be dropped for it.
func pgDatabaseExists(ctx context.Context, admin *sql.DB, name string) (bool, error) {
	var discard int
	err := admin.QueryRowContext(ctx, "SELECT 1 FROM pg_database WHERE datname = $1", name).Scan(&discard)
	switch {
	case err == nil:
		return true, nil
	case errors.Is(err, sql.ErrNoRows):
		return false, nil
	default:
		return false, err
	}
}

var postgresConfigCache perTestCache[*pgx.ConnConfig]

// PostgreSQLConfig returns a parsed PostgreSQL connection configuration for
// a fresh, per-run database created on a live server, resolved as described
// in the package doc. It skips the calling test rather than returning an
// error when RASQL_TEST_POSTGRES_DSN is not set.
//
// It returns pgx's own *pgx.ConnConfig rather than a DSN string on purpose.
// A caller that needs different credentials against the same database (see
// inspect/postgresql_privilege_test.go's openAsRole) must copy this config
// and change its User/Password fields directly, never rebuild and reparse
// a DSN string: an earlier version of this harness did exactly that, and a
// net/url round-trip of a keyword/value DSN corrupted it into a connection
// string pgx accepted but resolved to an unrelated target (a Unix socket,
// no database, the OS user). Handing back the already-parsed struct makes
// that class of bug impossible to reintroduce here, because there is no
// string left to round-trip.
//
// Calling this more than once for the same *testing.T (directly, or via
// PostgreSQLDB) returns configuration for the SAME fresh database every
// time, rather than creating a second one; see perTestCache.
func PostgreSQLConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	return postgresConfigCache.resolve(t, func() *pgx.ConnConfig {
		server := resolvePostgreSQLServerConfig(t)
		return createFreshPostgreSQLDatabase(t, server)
	})
}

// PostgreSQLDB opens a live PostgreSQL connection via PostgreSQLConfig,
// registers t.Cleanup to close it, and pings it before returning.
func PostgreSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	config := PostgreSQLConfig(t)
	return openAndPing(t, stdlib.OpenDB(*config))
}

// resolvePostgreSQLServerConfig resolves a PostgreSQL connection
// configuration the way the package doc describes -- a set
// RASQL_TEST_POSTGRES_DSN parsed with pgx's own parser -- without yet
// creating this run's own fresh database. createFreshPostgreSQLDatabase does
// that next; see PostgreSQLConfig.
func resolvePostgreSQLServerConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	value, set := os.LookupEnv(postgresEnvVar)
	trimmed, useValue, blank := dsnDecision(value, set)
	if !useValue {
		skipNoDSN(t, postgresEnvVar, postgresComposeDSN, blank)
		return nil
	}

	config, err := pgx.ParseConfig(trimmed)
	if err != nil {
		// A set-but-unparseable DSN fails rather than falls through to
		// Docker/skip: it is a concrete, wrong answer from the person who
		// set it, not the absence of one. See the package doc.
		t.Fatalf("%s is set to a value pgx could not parse: %v", postgresEnvVar, err)
	}
	return config
}

// createFreshPostgreSQLDatabase creates a database named uniquely for this
// run on the server server describes, registers a t.Cleanup to drop it, and
// returns a copy of server pointed at the new database -- the configuration
// every live PostgreSQL connection for this test should use.
//
// PostgreSQL cannot run CREATE DATABASE inside a transaction, and needs an
// existing database to connect to before it can create another, so this
// opens a connection through server -- whatever database its DSN or the
// compose fallback names -- probes for name's pre-existence, issues CREATE
// DATABASE there, then reconnects to the new database to confirm it before
// handing back its configuration.
//
// The probe, the drop registration, and the drop-guard flag are
// createFreshDatabase's job (see its doc for the full containment
// ordering); this function supplies the PostgreSQL-specific probe, create,
// and drop operations createFreshDatabase drives. The returned cleanup is
// registered with t.Cleanup before the returned error is inspected, so a
// Fatalf below still reaches it -- createFreshDatabase builds that cleanup,
// with dropOwned already decided, before it returns.
func createFreshPostgreSQLDatabase(t *testing.T, server *pgx.ConnConfig) *pgx.ConnConfig {
	t.Helper()
	name := UniqueName(t, "rasql_test")

	admin := stdlib.OpenDB(*server)
	defer func() { _ = admin.Close() }()
	if err := admin.PingContext(t.Context()); err != nil {
		t.Fatalf("dbtest: connect via %s to create a fresh PostgreSQL database: %v", postgresEnvVar, err)
	}

	cleanup, err := createFreshDatabase(
		name,
		func() (bool, error) { return pgDatabaseExists(t.Context(), admin, name) },
		func() error {
			_, err := admin.ExecContext(t.Context(), pgCreateDatabaseStatement(name))
			return err
		},
		pgCreateDatabaseErrorProvesNothingCreated,
		func() { dropPostgreSQLDatabase(t, server, name) },
	)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgInsufficientPrivilege {
			t.Fatalf("dbtest: %s's credentials cannot CREATE DATABASE; supply a DSN whose credentials can create and drop databases so the live suite can run inside its own database: %v", postgresEnvVar, err)
		}
		// Any other failure -- including a pre-existing name, which a
		// per-run unique name colliding would mean something is genuinely
		// wrong worth seeing -- fails loudly rather than retrying under
		// another name.
		t.Fatalf("dbtest: %v", err)
	}

	fresh := server.Copy()
	fresh.Database = name

	verify := stdlib.OpenDB(*fresh)
	defer func() { _ = verify.Close() }()
	if err := verify.PingContext(t.Context()); err != nil {
		t.Fatalf("dbtest: reconnect to fresh PostgreSQL database %q: %v", name, err)
	}

	return fresh
}

// dropPostgreSQLDatabase drops the fresh database name that
// createFreshPostgreSQLDatabase created, connecting through server -- the
// same pre-existing database used to create it, since PostgreSQL cannot
// drop the database a connection is currently using.
//
// This runs as a t.Cleanup, registered before every connection this
// package hands out against name (PostgreSQLDB's own connection, and any
// copy callers like openAsRole make from PostgreSQLConfig's result); Go
// runs t.Cleanup callbacks in last-registered-first-run order, so those
// connections' own Cleanup-registered (or defer-registered, which always
// runs before Cleanup) closes happen before this one runs. PostgreSQL
// refuses to drop a database that still has connections open, so that
// ordering -- not a forced drop -- is what makes this drop succeed.
func dropPostgreSQLDatabase(t *testing.T, server *pgx.ConnConfig, name string) {
	t.Helper()
	// t.Context() is already canceled by the time a t.Cleanup function
	// runs, so this uses its own bounded context rather than t.Context().
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()

	admin := stdlib.OpenDB(*server)
	defer func() { _ = admin.Close() }()
	if _, err := admin.ExecContext(ctx, pgDropDatabaseStatement(name)); err != nil {
		t.Errorf("dbtest: drop fresh PostgreSQL database %q: %v", name, err)
	}
}

// pgCreateDatabaseStatement and pgDropDatabaseStatement build the exact SQL
// text createFreshPostgreSQLDatabase and dropPostgreSQLDatabase issue for
// name, split out as pure functions so a test can pin that both target
// exactly the same, correctly quoted identifier without needing a live
// server to connect to.
func pgCreateDatabaseStatement(name string) string {
	return "CREATE DATABASE " + pgx.Identifier{name}.Sanitize()
}

func pgDropDatabaseStatement(name string) string {
	return "DROP DATABASE IF EXISTS " + pgx.Identifier{name}.Sanitize()
}
