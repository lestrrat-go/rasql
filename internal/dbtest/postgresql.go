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

// postgresComposeService is the compose.yaml service PostgreSQLConfig
// brings up when no DSN is set; see ensureComposeUp in compose.go.
var postgresComposeService = composeService{name: "postgres", port: "5432", envVar: postgresEnvVar}

var postgresConfigCache perTestCache[*pgx.ConnConfig]

// PostgreSQLConfig returns a parsed PostgreSQL connection configuration for
// a fresh, per-run database created on a live server, resolved as described
// in the package doc. It skips the calling test rather than returning an
// error when no DSN or usable Docker fallback is available.
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
// RASQL_TEST_POSTGRES_DSN parsed with pgx's own parser, or the Docker
// compose fallback -- without yet creating this run's own fresh database.
// createFreshPostgreSQLDatabase does that next; see PostgreSQLConfig.
func resolvePostgreSQLServerConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	value, set := os.LookupEnv(postgresEnvVar)
	trimmed, useValue, shouldLog := dsnDecision(value, set)
	if !useValue {
		if shouldLog {
			blankDiagnostic(t, postgresEnvVar)
		}
		ensureComposeUp(t, postgresComposeService)
		// postgresComposeDSN is this package's own constant, not user
		// input, so a parse failure here would be a bug in this package
		// rather than something to validate against.
		config, err := pgx.ParseConfig(postgresComposeDSN)
		if err != nil {
			t.Fatalf("dbtest: parse postgresComposeDSN: %v", err)
		}
		return config
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
// run on the server server describes, registers a t.Cleanup to drop it,
// and returns a copy of server pointed at the new database -- the
// configuration every live PostgreSQL connection for this test should use.
//
// PostgreSQL cannot run CREATE DATABASE inside a transaction, and needs an
// existing database to connect to before it can create another, so this
// opens a connection through server -- whatever database its DSN or the
// compose fallback names -- issues CREATE DATABASE there, then reconnects
// to the new database to confirm it before handing back its configuration.
func createFreshPostgreSQLDatabase(t *testing.T, server *pgx.ConnConfig) *pgx.ConnConfig {
	t.Helper()
	name := UniqueName(t, "rasql_test")

	admin := stdlib.OpenDB(*server)
	defer admin.Close()
	if err := admin.PingContext(t.Context()); err != nil {
		t.Fatalf("dbtest: connect via %s to create a fresh PostgreSQL database: %v", postgresEnvVar, err)
	}
	if _, err := admin.ExecContext(t.Context(), pgCreateDatabaseStatement(name)); err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == pgInsufficientPrivilege {
			t.Fatalf("dbtest: %s's credentials cannot CREATE DATABASE; supply a DSN whose credentials can create and drop databases so the live suite can run inside its own database: %v", postgresEnvVar, err)
		}
		// Any other failure -- including the name already existing, which
		// a per-run unique name colliding would mean something is
		// genuinely wrong worth seeing -- fails loudly rather than
		// retrying under another name.
		t.Fatalf("dbtest: create fresh PostgreSQL database %q: %v", name, err)
	}

	fresh := server.Copy()
	fresh.Database = name

	verify := stdlib.OpenDB(*fresh)
	defer verify.Close()
	if err := verify.PingContext(t.Context()); err != nil {
		t.Fatalf("dbtest: reconnect to fresh PostgreSQL database %q: %v", name, err)
	}

	t.Cleanup(func() { dropPostgreSQLDatabase(t, server, name) })
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
	defer admin.Close()
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
	return "DROP DATABASE " + pgx.Identifier{name}.Sanitize()
}
