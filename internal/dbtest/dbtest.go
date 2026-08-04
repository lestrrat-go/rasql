// Package dbtest resolves live PostgreSQL and MySQL connections for tests in
// any rasql package, so a live test is not limited to the root package the
// way TestDatabaseIntegration once was.
//
// Resolution order, for both databases independently:
//
//  1. If the relevant RASQL_TEST_POSTGRES_DSN or RASQL_TEST_MYSQL_DSN
//     environment variable is set, its value is used directly and Docker is
//     never touched. This is CI's path (see .github/workflows/ci.yml), and
//     it keeps that behavior exactly as it is today.
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
// This package never tears containers down, and has no opt-in to make it
// do so. go test ./... compiles and runs every package as a separate
// binary, and runs those binaries in parallel, so several packages can
// reach step 2 at the same moment in different processes; bring-up is
// serialized with an advisory file lock (see lock_unix.go and
// lock_other.go), but a t.Cleanup that ran `docker compose down` in one
// package would destroy the database another package is still using. A
// developer stops the containers by hand once finished:
//
//	docker compose down -v
package dbtest

import (
	"database/sql"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
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

// PostgreSQLDSN returns a DSN for a live PostgreSQL database, resolved as
// described in the package doc. It skips the calling test rather than
// returning an error when no DSN is available.
func PostgreSQLDSN(t *testing.T) string {
	t.Helper()
	return resolveDSN(t, postgresEnvVar, postgresComposeDSN)
}

// MySQLDSN returns a DSN for a live MySQL database, resolved as described in
// the package doc. It skips the calling test rather than returning an error
// when no DSN is available.
func MySQLDSN(t *testing.T) string {
	t.Helper()
	return resolveDSN(t, mysqlEnvVar, mysqlComposeDSN)
}

// PostgreSQLDB opens a live PostgreSQL connection via the "pgx" driver,
// registers t.Cleanup to close it, and pings it before returning.
func PostgreSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	return openDB(t, "pgx", PostgreSQLDSN(t))
}

// MySQLDB opens a live MySQL connection via the "mysql" driver, registers
// t.Cleanup to close it, and pings it before returning.
func MySQLDB(t *testing.T) *sql.DB {
	t.Helper()
	return openDB(t, "mysql", MySQLDSN(t))
}

// dsnDecision is the pure decision at the heart of resolveDSN: given an
// environment variable's raw state -- its value and whether it was set at
// all -- does resolution use that value directly (useEnv), or fall through
// to the Docker/skip path? When it falls through because the variable was
// present but empty (as opposed to simply unset), shouldLog reports that,
// so the caller can emit the present-but-empty diagnostic.
//
// This function does no environment lookups, runs no Docker commands, and
// logs nothing itself -- it is pure input to output -- so it can be unit
// tested directly without touching Docker or process environment state.
// See dsnDecision's test for why that separation exists.
//
// A present-but-empty value is deliberately treated the same as unset and
// falls through to Docker/skip, rather than failing fast. This is not an
// oversight; two things rule out failing here:
//
//   - Clearing an inherited DSN to opt out of live tests is a real
//     workflow, and this repository's own base CI relied on it: the old
//     matrix set RASQL_TEST_MYSQL_DSN from an undefined matrix value on
//     the PostgreSQL leg, producing an empty value that had to mean "not
//     set". Failing on present-but-empty would break that pattern for
//     anyone who reintroduces it.
//   - Failing late, at connection time, instead of here is not a safe
//     alternative either: sql.Open("pgx", "") succeeds, and an empty pgx
//     DSN silently falls back to libpq's PG* environment variables
//     (PGHOST, PGPORT, PGUSER, PGDATABASE, ...). On a machine with those
//     set, that can connect to an unintended database and pass for the
//     wrong reason instead of erroring.
//
// If a hard failure is ever wanted, it must be an explicit, deliberate
// choice made with the above in mind -- not a drive-by fix.
func dsnDecision(value string, set bool) (useEnv, shouldLog bool) {
	if set && value != "" {
		return true, false
	}
	return false, set
}

func resolveDSN(t *testing.T, envVar, composeDSN string) string {
	t.Helper()
	value, set := os.LookupEnv(envVar)
	useEnv, shouldLog := dsnDecision(value, set)
	if useEnv {
		return value
	}
	// The person who expected their DSN to be used can see why it was
	// not, instead of reading a Docker or skip message that never names
	// their variable.
	if shouldLog {
		t.Logf("%s is set but empty; ignoring it and falling through to Docker/skip resolution", envVar)
	}
	ensureComposeUp(t)
	return composeDSN
}

func openDB(t *testing.T, driverName, dsn string) *sql.DB {
	t.Helper()
	db, err := sql.Open(driverName, dsn)
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, db.Close())
	})
	require.NoError(t, db.PingContext(t.Context()))
	return db
}
