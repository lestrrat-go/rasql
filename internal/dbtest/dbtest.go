//go:build unix

// Package dbtest resolves live PostgreSQL and MySQL connections for tests in
// any rasql package, so a live test is not limited to the root package the
// way TestDatabaseIntegration once was.
//
// Resolution order, for both databases independently:
//
//  1. If the relevant RASQL_TEST_POSTGRES_DSN or RASQL_TEST_MYSQL_DSN
//     environment variable is set to a non-blank value, that value is
//     PARSED with the driver's own parser (pgx.ParseConfig for PostgreSQL,
//     mysql.ParseDSN for MySQL) and Docker is never touched. This is CI's
//     path (see .github/workflows/ci.yml), and it keeps that behavior
//     exactly as it is today. The parsed configuration -- never the raw
//     string -- is what callers receive; see PostgreSQLConfig and
//     MySQLConfig for why.
//  2. Otherwise, if Docker is usable, the compose file checked in at the
//     repository root (compose.yaml) is brought up with
//     `docker compose up -d --wait` (or the standalone `docker-compose` if
//     the compose plugin is absent), and the DSN is derived from that
//     file's fixed ports -- 5432 for PostgreSQL, 3306 for MySQL -- the same
//     ports and credentials CI's DSNs already assume.
//  3. Otherwise the calling test is skipped, naming which of the following
//     was detected: the docker binary missing from PATH, the daemon being
//     unreachable (including a permission error talking to its socket), or
//     neither `docker compose` nor `docker-compose` being available.
//
// Unavailability and failure are different outcomes and this package treats
// them differently. Docker being unusable is a fact about the machine, not
// about rasql, so it skips the test rather than failing it. A compose
// bring-up that fails after Docker has already been confirmed reachable
// means the compose file or an image reference is broken -- something the
// person running the suite can fix -- so that path fails the test loudly
// instead of skipping; a skip there would hide real breakage from exactly
// the person able to see it.
//
// One bring-up failure is deliberately carved out of that loud-failure
// rule: a host port compose wants (5432 or 3306) already being in use by
// something else -- commonly a PostgreSQL or MySQL server the developer
// already has running locally, or, as happened in this repository's own
// CI, another job's service containers still holding the port. That is
// neither a broken compose file nor a broken image reference, so it skips
// instead, naming the conflicting port -- determined by probing, not by
// reading Docker's message text -- and the RASQL_TEST_*_DSN variable that
// points at the database already running there. See classifyPortCollision
// in port_collision.go for how a bring-up failure's output is told apart
// from any other kind, and findConflictingPort for how the port itself is
// determined.
//
// A set RASQL_TEST_*_DSN value is validated, not merely trusted: an empty
// pgx DSN, or one missing its host, port, or database in its own text,
// silently falls back to libpq's PG* environment variables (PGHOST,
// PGPORT, PGDATABASE, ...), a PostgreSQL service file, or pgx's own
// built-in defaults instead of erroring, and a machine with those set can
// then connect to an unintended database and pass for the wrong reason.
// The default on a developer machine is a Unix socket reached as the OS
// user, not 127.0.0.1, so this is not a hypothetical: the live suite runs
// CREATE TABLE, CREATE ROLE, and DROP ROLE, and a wrongly resolved target
// is destructive. So a set-but-unusable DSN FAILS the calling test rather
// than falling back to Docker or skipping -- see resolvePostgreSQLConfig
// and resolveMySQLConfig for exactly what "unusable" means for each driver
// and why failing is the right response once a value has been set. Both
// validators judge the DSN's own text directly rather than comparing
// parses made with and without the ambient environment visible; see
// unusablePostgreSQLTarget's comment in postgresql.go for why that
// approach -- this package's own earlier one -- does not work.
//
// This package never tears containers down, and has no opt-in to make it
// do so. go test ./... compiles and runs every package as a separate
// binary, and runs those binaries in parallel, so several packages can
// reach step 2 at the same moment in different processes; bring-up is
// serialized with an advisory file lock (see lock_unix.go), but a
// t.Cleanup that ran `docker compose down` in one package would destroy
// the database another package is still using. A developer stops the
// containers by hand once finished:
//
//	docker compose down -v
//
// This package -- and therefore any test that imports it -- builds only on
// unix (see the //go:build unix constraint at the top of every file in this
// package, including lock_unix.go's advisory file lock, which has no
// portable equivalent this package relies on). A consumer test file must
// carry the same constraint; otherwise `go test ./...` fails to build that
// package's test binary on a non-unix platform instead of skipping it. See
// database_integration_test.go and inspect/postgresql_privilege_test.go for
// the pattern.
package dbtest

import (
	"database/sql"
	"strings"
	"testing"
)

const (
	// postgresEnvVar is checked first for a PostgreSQL DSN; see the package
	// doc for the full resolution order.
	postgresEnvVar = "RASQL_TEST_POSTGRES_DSN"
	// mysqlEnvVar is checked first for a MySQL DSN; see the package doc for
	// the full resolution order.
	mysqlEnvVar = "RASQL_TEST_MYSQL_DSN"

	// postgresComposeDSN matches the "postgres" service in compose.yaml and
	// the postgres_dsn CI sets for its PostgreSQL matrix leg.
	postgresComposeDSN = "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"
	// mysqlComposeDSN matches the "mysql" service in compose.yaml and the
	// mysql_dsn CI sets for its MySQL matrix leg.
	mysqlComposeDSN = "rasql:rasql@tcp(127.0.0.1:3306)/rasql?parseTime=true"
)

// dsnDecision is the pure decision at the heart of both resolvePostgreSQLConfig
// and resolveMySQLConfig: given an environment variable's raw state -- its
// value and whether it was set at all -- does resolution parse and use that
// value, or fall through to the Docker/skip path? It returns (trimmed,
// useValue, shouldLog): trimmed is value with leading and trailing
// whitespace removed, and useValue reports whether the caller should parse
// trimmed at all. Callers parse trimmed, never value, so a DSN with a
// trailing newline -- the same adjacent case an all-whitespace value is --
// is handled by the value it clearly means rather than failing on
// incidental formatting. When resolution falls through because the
// variable was present but blank after trimming (as opposed to simply
// unset), shouldLog reports that, so the caller can emit the
// present-but-blank diagnostic.
//
// This function does no environment lookups, runs no Docker commands, and
// logs nothing itself -- it is pure input to output -- so it can be unit
// tested directly without touching Docker or process environment state.
// See dsnDecision's test for why that separation exists.
//
// A present-but-blank value is deliberately treated the same as unset and
// falls through to Docker/skip, rather than failing fast. This is not an
// oversight; two things rule out failing here:
//
//   - Clearing an inherited DSN to opt out of live tests is a real
//     workflow, and this repository's own base CI relied on it: the old
//     matrix set RASQL_TEST_MYSQL_DSN from an undefined matrix value on
//     the PostgreSQL leg, producing an empty value that had to mean "not
//     set". Failing on present-but-blank would break that pattern for
//     anyone who reintroduces it.
//   - A blank value carries no information to fail loudly about: there is
//     no host, port, or database in it to report as wrong. It reads the
//     same as "not set", so it is treated the same way.
//
// A present and non-blank value that turns out to be unusable -- one that
// fails to parse, or one whose host/port/database would come from
// somewhere other than the value itself -- is a different case, handled by
// resolvePostgreSQLConfig/resolveMySQLConfig, not by this function: that
// value carries a specific, wrong answer, unlike a blank one, which is why
// it fails rather than falls through. See the package doc.
func dsnDecision(value string, set bool) (string, bool, bool) {
	trimmed := strings.TrimSpace(value)
	if set && trimmed != "" {
		return trimmed, true, false
	}
	return "", false, set
}

// openAndPing pings db, registers t.Cleanup to close it, and returns it.
// Both PostgreSQLDB and MySQLDB build their *sql.DB through their own
// driver-specific connector (see stdlib.OpenDB and mysql.NewConnector) --
// never through sql.Open(driverName, dsnString) -- so this helper only
// covers the part identical to both: verifying the connection actually
// works before handing it to the caller.
func openAndPing(t *testing.T, db *sql.DB) *sql.DB {
	t.Helper()
	t.Cleanup(func() {
		if err := db.Close(); err != nil {
			t.Errorf("dbtest: close database: %v", err)
		}
	})
	if err := db.PingContext(t.Context()); err != nil {
		t.Fatalf("dbtest: ping database: %v", err)
	}
	return db
}

// blankDiagnostic logs why a present-but-blank RASQL_TEST_*_DSN value is
// being ignored, so the person who expected their DSN to be used can see
// why it was not, instead of reading a Docker or skip message that never
// names their variable.
func blankDiagnostic(t *testing.T, envVar string) {
	t.Helper()
	t.Logf("%s is set to a blank value; ignoring it and falling through to Docker/skip resolution", envVar)
}
