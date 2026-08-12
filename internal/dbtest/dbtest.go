//go:build unix

// Package dbtest resolves live PostgreSQL and MySQL connections for tests in
// any rasql package, so a live test is not limited to the root package the
// way TestDatabaseIntegration once was.
//
// Resolution, for both databases independently, has exactly one path: the
// relevant RASQL_TEST_POSTGRES_DSN or RASQL_TEST_MYSQL_DSN environment
// variable must be set to a non-blank value, which is PARSED with the
// driver's own parser (pgx.ParseConfig for PostgreSQL, mysql.ParseDSN for
// MySQL). This is exactly the DSN CI's integration job sets (see
// .github/workflows/ci.yml), which brings up the compose file checked in at
// the repository root (compose.yaml) as a workflow step before running the
// suite. This package never runs Docker itself; when the variable is unset
// or blank, the calling test is skipped with a message naming exactly what
// to run locally: bring up compose.yaml with `docker compose up -d --wait`
// and export the DSN it defines.
//
// Whichever DSN a database resolves to, this package then creates a
// database (a schema, for MySQL) named uniquely for this run, and hands
// back a configuration pointed at THAT database rather than whatever the
// resolved DSN itself named. Safety no longer depends on proving where a
// supplied DSN's text points: earlier revisions of this package tried to
// verify that from the DSN's own text alone, which required reimplementing
// pieces of each driver's own parser this package cannot call, and an audit
// judged that approach intractable. Instead, this package makes WHERE a set
// DSN lands not matter, by ensuring the live suite can only touch objects
// it created itself: its own fresh database, dropped in cleanup, plus the
// per-run-unique tables and roles/users individual tests create directly
// with UniqueName (see inspect/postgresql_privilege_test.go for an
// example). A calling test must keep that property itself -- never operate
// on an object it did not create by a name it does not control -- since a
// per-database boundary cannot contain a cluster-wide PostgreSQL role or a
// global MySQL user the way it contains a table.
//
// A set DSN that turns out to be unparseable fails the test loudly rather
// than falling back to skip: it carries a specific, wrong answer from
// whoever set it, not the absence of one. Failing to create this package's
// own fresh database once connected -- including a set of credentials that
// cannot CREATE DATABASE at all -- fails loudly for the same reason.
//
// This package never tears down whatever brought the database up, and has
// no opt-in to make it do so: several `go test ./...` binaries can be using
// the same containers at once, and an automatic `docker compose down` in
// one package's cleanup would destroy the database another package's tests
// are still using. A developer stops the containers by hand once finished:
//
//	docker compose down -v
//
// This package -- and therefore any test that imports it -- builds only on
// unix (see the //go:build unix constraint at the top of every file in this
// package). Nothing left in this package is itself unix-specific; the
// constraint predates this package's single-DSN-path form and is kept
// unchanged rather than relitigated here. A consumer test file must carry
// the same constraint; otherwise `go test ./...` fails to build that
// package's test binary on a non-unix platform instead of skipping it. See
// database_integration_test.go and inspect/postgresql_privilege_test.go for
// the pattern.
package dbtest

import (
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

const (
	// postgresEnvVar is checked first for a PostgreSQL DSN; see the package
	// doc for the full resolution order.
	postgresEnvVar = "RASQL_TEST_POSTGRES_DSN"
	// mysqlEnvVar is checked first for a MySQL DSN; see the package doc for
	// the full resolution order.
	mysqlEnvVar = "RASQL_TEST_MYSQL_DSN"

	// postgresComposeDSN matches the "postgres" service in compose.yaml and
	// the RASQL_TEST_POSTGRES_DSN CI's integration job sets. skipNoDSN
	// quotes it verbatim in the skip message resolvePostgreSQLServerConfig
	// emits when RASQL_TEST_POSTGRES_DSN is unset, so a developer can export
	// exactly this value after bringing compose.yaml up.
	postgresComposeDSN = "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"
	// mysqlComposeDSN matches the "mysql" service in compose.yaml and the
	// RASQL_TEST_MYSQL_DSN CI's integration job sets, quoted the same way by
	// skipNoDSN via resolveMySQLServerConfig. It authenticates as root, not
	// the rasql/rasql account MYSQL_USER/MYSQL_PASSWORD create: the official
	// mysql image grants that account privileges scoped to MYSQL_DATABASE
	// only, so it cannot CREATE DATABASE for this package's fresh per-run
	// schema. root, whose password both service definitions already set via
	// MYSQL_ROOT_PASSWORD, can.
	mysqlComposeDSN = "root:root@tcp(127.0.0.1:3306)/rasql?parseTime=true"

	// cleanupTimeout bounds a t.Cleanup-registered database drop. It cannot
	// use t.Context(): testing.T.Context is already canceled by the time
	// Cleanup-registered functions run, so a drop that used it would fail
	// immediately with a canceled-context error instead of ever reaching
	// the server.
	cleanupTimeout = 30 * time.Second
)

// dsnDecision is the pure decision at the heart of both
// resolvePostgreSQLServerConfig and resolveMySQLServerConfig: given an
// environment variable's raw state -- its value and whether it was set at
// all -- does resolution parse and use that value, or fall through to skip?
// It returns (trimmed, useValue, shouldLog): trimmed is value with leading
// and trailing whitespace removed, and useValue reports whether the caller
// should parse trimmed at all. Callers parse trimmed, never value, so a DSN
// with a trailing newline -- the same adjacent case an all-whitespace value
// is -- is handled by the value it clearly means rather than failing on
// incidental formatting. When resolution falls through because the
// variable was present but blank after trimming (as opposed to simply
// unset), shouldLog reports that, so the caller can emit the
// present-but-blank diagnostic.
//
// This function does no environment lookups and logs nothing itself -- it
// is pure input to output -- so it can be unit tested directly without
// touching process environment state. See dsnDecision's test for why that
// separation exists.
//
// A present-but-blank value is deliberately treated the same as unset and
// falls through to skip, rather than failing fast. This is not an
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
// A present and non-blank value that turns out to be unparseable is a
// different case, handled by
// resolvePostgreSQLServerConfig/resolveMySQLServerConfig, not by this
// function: that value carries a specific, wrong answer, unlike a blank
// one, which is why it fails rather than falls through. See the package
// doc.
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
// why it was not, instead of reading a skip message that never names their
// variable.
func blankDiagnostic(t *testing.T, envVar string) {
	t.Helper()
	t.Logf("%s is set to a blank value; ignoring it and skipping", envVar)
}

// skipNoDSN skips the calling test because envVar is not set (or is set to
// a blank value; see blankDiagnostic for that diagnostic), naming exactly
// what to run to get a live database: bring up the checked-in compose.yaml
// and export the DSN it defines for envVar.
func skipNoDSN(t *testing.T, envVar, composeDSN string) {
	t.Helper()
	t.Skipf("%s is not set; bring up compose.yaml with `docker compose up -d --wait` and export %s=%s", envVar, envVar, composeDSN)
}

// UniqueName generates a name unlikely to collide with another concurrent
// or prior test run, for any object a live test creates directly: this
// package's own per-run database (see createFreshPostgreSQLDatabase and its
// MySQL equivalent in mysql.go), or a table or role/user a calling test
// creates itself (see inspect/postgresql_privilege_test.go). PostgreSQL
// roles and MySQL users are cluster-wide/global rather than scoped to a
// single database, so a leaked one from a previous run could otherwise
// collide with a fresh one this run creates; a per-database boundary alone
// cannot contain that, which is why every live test using this package
// must still name what it creates through this function rather than a
// fixed literal.
func UniqueName(t *testing.T, prefix string) string {
	t.Helper()
	return fmt.Sprintf("%s_%d_%d", prefix, os.Getpid(), time.Now().UnixNano())
}

// createFreshDatabase implements the containment ordering
// createFreshPostgreSQLDatabase and createFreshMySQLDatabase both build on,
// factored out into driver-agnostic function-parameter form so the ordering
// itself -- not the SQL either driver speaks -- can be exercised by a test
// with fake probeExists/create/drop functions and no live server. See
// TestCreateFreshDatabase in dbtest_test.go.
//
// The three steps, matching the containment remedy in the package doc:
//
//  1. probeExists runs first, before anything else. If it reports the name
//     already exists, this returns immediately with a nil cleanup and an
//     error: nothing is ever registered for the caller to run, so a
//     pre-existing name can never be dropped by this call, not even by
//     accident.
//  2. A local dropOwned flag, and a cleanup closure that reads it, are
//     built before create runs -- so if the caller registers the returned
//     cleanup with t.Cleanup before inspecting the returned error (as both
//     callers do) and then fails loudly, the cleanup is already in place.
//     dropOwned needs no synchronization: the flag is written here and read
//     by cleanup, and both happen only on the calling test's own goroutine
//     -- cleanup runs from t.Cleanup on that same goroutine, whether
//     reached normally or via t.Fatalf's runtime.Goexit unwind.
//  3. If create fails, errorProvesNothingCreated decides whether that
//     failure proves create made nothing: if so, dropOwned is cleared so
//     the returned cleanup becomes a no-op, and the returned error still
//     tells the caller to fail loudly. If create's error does not prove
//     that -- including an error carrying no code at all, such as a
//     context cancellation, a connection reset, a timeout, or an EOF --
//     dropOwned stays true, so cleanup still attempts the drop: the
//     database may have been created despite the client-visible error, and
//     leaking it is the safe failure mode, not dropping something this
//     call did not create.
//
// On success, cleanup drops the database this call created and err is nil.
func createFreshDatabase(name string, probeExists func() (bool, error), create func() error, errorProvesNothingCreated func(error) bool, drop func()) (func(), error) {
	exists, err := probeExists()
	if err != nil {
		return nil, fmt.Errorf("check whether a database named %q already exists: %w", name, err)
	}
	if exists {
		return nil, fmt.Errorf("a database named %q already exists; refusing to touch it (per-run names must be unique -- see UniqueName)", name)
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
		return cleanup, fmt.Errorf("create fresh database %q: %w", name, err)
	}
	return cleanup, nil
}

// perTestCache memoizes a value per *testing.T, so a live test that
// resolves the same live database more than once within itself (see
// openAsRole in inspect/postgresql_privilege_test.go, which calls
// PostgreSQLConfig again after PostgreSQLDB already created the run's fresh
// database) reuses that same fresh database instead of creating a second,
// unrelated one only the first caller's connections can see. The entry is
// removed via t.Cleanup so the map cannot grow across the life of a test
// binary.
type perTestCache[T any] struct {
	mu sync.Mutex
	m  map[*testing.T]T
}

// resolve returns the cached value for t if resolve has already been called
// for t, or calls create, caches its result, and returns that. create runs
// while the cache's lock is held, so two resolve calls for the same t from
// concurrent goroutines cannot both create a value; nothing in this
// package's own callers does that today, but the lock keeps that true
// rather than merely accidental.
func (c *perTestCache[T]) resolve(t *testing.T, create func() T) T {
	t.Helper()
	c.mu.Lock()
	defer c.mu.Unlock()
	if v, ok := c.m[t]; ok {
		return v
	}
	v := create()
	if c.m == nil {
		c.m = make(map[*testing.T]T)
	}
	c.m[t] = v
	t.Cleanup(func() {
		c.mu.Lock()
		delete(c.m, t)
		c.mu.Unlock()
	})
	return v
}
