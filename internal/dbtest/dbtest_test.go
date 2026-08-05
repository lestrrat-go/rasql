//go:build unix

package dbtest

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestDSNDecision pins the pure decision inside resolvePostgreSQLServerConfig
// and resolveMySQLServerConfig: given an environment variable's raw state,
// does resolution parse and use its value, or fall through to the
// Docker/skip path (see part 1's blank-DSN case, "a blank, empty and
// all-whitespace DSN still takes the Docker-or-skip path")? It covers
// unset, a real value, an all-whitespace value (empty, single space, and
// tab, since dsnDecision treats them identically), and a real value padded
// with whitespace -- pinning that padding is trimmed away rather than
// treated as part of the value, that both unset and all-whitespace fall
// through, and that only a present-but-blank value asks for the diagnostic
// log.
//
// This is deliberately a test of dsnDecision alone, not of
// PostgreSQLConfig/MySQLConfig/resolvePostgreSQLServerConfig/resolveMySQLServerConfig
// end-to-end: those reach ensureComposeUp, which runs `docker compose up`
// when Docker is reachable. On a CI runner that already has PostgreSQL and
// MySQL service containers bound to ports 5432/3306, that bring-up fails
// even though resolution itself made the right decision -- a test of the
// decision should not perform the action the decision leads to. dsnDecision
// touches no environment variable and no Docker, so this test needs
// neither, and passes identically with Docker installed, absent, or
// already using the ports compose would want.
func TestDSNDecision(t *testing.T) {
	t.Run("unset falls through without logging", func(t *testing.T) {
		trimmed, useValue, shouldLog := dsnDecision("", false)
		if useValue {
			t.Fatal("dsnDecision(\"\", false) reported useValue; an unset variable must fall through to Docker/skip")
		}
		if shouldLog {
			t.Fatal("dsnDecision(\"\", false) reported shouldLog; an unset variable is not the present-but-blank case and must not log")
		}
		if trimmed != "" {
			t.Fatalf("dsnDecision(\"\", false) trimmed = %q, want \"\" since useValue is false", trimmed)
		}
	})

	for _, blank := range []struct {
		name  string
		value string
	}{
		{"present but empty", ""},
		{"present as a single space", " "},
		{"present as a tab", "\t"},
		{"present as mixed whitespace", " \t\n "},
	} {
		t.Run(blank.name+" falls through and logs", func(t *testing.T) {
			trimmed, useValue, shouldLog := dsnDecision(blank.value, true)
			if useValue {
				t.Fatalf("dsnDecision(%q, true) reported useValue; a blank value must fall through to Docker/skip, never be parsed as a DSN", blank.value)
			}
			if !shouldLog {
				t.Fatalf("dsnDecision(%q, true) did not report shouldLog; the present-but-blank diagnostic would be silently dropped", blank.value)
			}
			if trimmed != "" {
				t.Fatalf("dsnDecision(%q, true) trimmed = %q, want \"\" since useValue is false", blank.value, trimmed)
			}
		})
	}

	t.Run("set to a real value is used directly", func(t *testing.T) {
		const dsn = "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"
		trimmed, useValue, shouldLog := dsnDecision(dsn, true)
		if !useValue {
			t.Fatal("dsnDecision reported !useValue for a non-blank value; a set DSN must be parsed directly without touching Docker")
		}
		if shouldLog {
			t.Fatal("dsnDecision reported shouldLog for a non-blank value; only the present-but-blank case logs")
		}
		if trimmed != dsn {
			t.Fatalf("dsnDecision trimmed = %q, want %q unchanged", trimmed, dsn)
		}
	})

	t.Run("set to a value padded with a trailing newline is trimmed and used", func(t *testing.T) {
		const dsn = "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"
		trimmed, useValue, shouldLog := dsnDecision(dsn+"\n", true)
		if !useValue {
			t.Fatal("dsnDecision reported !useValue for a padded non-blank value; padding must not turn a real value into a fall-through")
		}
		if shouldLog {
			t.Fatal("dsnDecision reported shouldLog for a padded non-blank value; only the present-but-blank case logs")
		}
		if trimmed != dsn {
			t.Fatalf("dsnDecision trimmed = %q, want %q with the trailing newline removed", trimmed, dsn)
		}
	})
}

// TestUniqueNameIsPerCallUnique pins the per-run uniqueness property this
// package's own fresh-database naming depends on (see
// createFreshPostgreSQLDatabase and createFreshMySQLDatabase): two calls,
// even back to back within the same process, must never produce the same
// name, since two live tests running concurrently in different
// `go test ./...` binaries -- sharing a PID only by coincidence -- must
// never collide on the database name each one creates and later drops.
func TestUniqueNameIsPerCallUnique(t *testing.T) {
	first := UniqueName(t, "rasql_test")
	second := UniqueName(t, "rasql_test")
	if first == second {
		t.Fatalf("UniqueName called twice returned %q both times, want two distinct names", first)
	}
	if !strings.HasPrefix(first, "rasql_test_") || !strings.HasPrefix(second, "rasql_test_") {
		t.Fatalf("UniqueName results %q and %q do not carry the requested prefix", first, second)
	}
}

// TestPostgreSQLDatabaseStatementsTargetExactName pins that
// createFreshPostgreSQLDatabase and dropPostgreSQLDatabase build CREATE and
// DROP statements naming the exact same, correctly quoted identifier for a
// given per-run unique name, which is what makes cleanup drop precisely --
// and only -- the database this run created. This needs no live server: it
// drives the same pure statement builders those functions call.
func TestPostgreSQLDatabaseStatementsTargetExactName(t *testing.T) {
	name := UniqueName(t, "rasql_test")
	wantIdent := pgx.Identifier{name}.Sanitize()

	create := pgCreateDatabaseStatement(name)
	if create != "CREATE DATABASE "+wantIdent {
		t.Fatalf("pgCreateDatabaseStatement(%q) = %q, want %q", name, create, "CREATE DATABASE "+wantIdent)
	}
	// The drop is registered before CREATE DATABASE runs (see
	// createFreshPostgreSQLDatabase), so it must use IF EXISTS: a
	// legitimate CREATE failure must not turn cleanup into a second,
	// spurious error against a database that was never created.
	drop := pgDropDatabaseStatement(name)
	if drop != "DROP DATABASE IF EXISTS "+wantIdent {
		t.Fatalf("pgDropDatabaseStatement(%q) = %q, want %q", name, drop, "DROP DATABASE IF EXISTS "+wantIdent)
	}
}

// TestMySQLDatabaseStatementsTargetExactName is
// TestPostgreSQLDatabaseStatementsTargetExactName's MySQL equivalent for
// createFreshMySQLDatabase and dropMySQLDatabase.
func TestMySQLDatabaseStatementsTargetExactName(t *testing.T) {
	name := UniqueName(t, "rasql_test")
	wantIdent := mysqlQuoteIdentifier(name)

	create := mysqlCreateDatabaseStatement(name)
	if create != "CREATE DATABASE "+wantIdent {
		t.Fatalf("mysqlCreateDatabaseStatement(%q) = %q, want %q", name, create, "CREATE DATABASE "+wantIdent)
	}
	// See the identical comment in
	// TestPostgreSQLDatabaseStatementsTargetExactName: the drop is
	// registered before CREATE DATABASE runs, so it must use IF EXISTS.
	drop := mysqlDropDatabaseStatement(name)
	if drop != "DROP DATABASE IF EXISTS "+wantIdent {
		t.Fatalf("mysqlDropDatabaseStatement(%q) = %q, want %q", name, drop, "DROP DATABASE IF EXISTS "+wantIdent)
	}
}

// TestCreateFreshDatabase pins the containment ordering createFreshDatabase
// implements (see its own doc for the three steps) using fake
// probeExists/create/drop functions instead of a live database, so the
// decision is provable without a server -- this package cannot open a
// connection in this environment, and even where it can, a database-backed
// test could only prove the happy path, not the specific error-code
// branches this exists to get right.
//
// Every subtest asserts on the *fake* drop function actually being invoked
// or not, never merely on the returned error: the property under test is
// containment -- whether a drop is attempted -- not whether the call
// failed, and a test that only checked the error would pass even if the
// guard were deleted entirely.
func TestCreateFreshDatabase(t *testing.T) {
	t.Run("pre-existing name fails loudly with no drop ever registered", func(t *testing.T) {
		probeCalls, createCalls, dropCalls := 0, 0, 0
		cleanup, err := createFreshDatabase(
			"exists",
			func() (bool, error) { probeCalls++; return true, nil },
			func() error { createCalls++; return nil },
			func(error) bool {
				t.Fatal("errorProvesNothingCreated must not be called: create must never run for a pre-existing name")
				return false
			},
			func() { dropCalls++ },
		)
		if err == nil {
			t.Fatal("createFreshDatabase returned a nil error for a pre-existing name, want an error")
		}
		if cleanup != nil {
			t.Fatal("createFreshDatabase returned a non-nil cleanup for a pre-existing name; nothing may ever be registered for a name this call refused to touch")
		}
		if probeCalls != 1 {
			t.Fatalf("probeExists called %d times, want exactly 1", probeCalls)
		}
		if createCalls != 0 {
			t.Fatalf("create called %d times, want 0: CREATE DATABASE must never run for a name the probe found pre-existing", createCalls)
		}
		if dropCalls != 0 {
			t.Fatalf("drop called %d times, want 0: cleanup is nil, so nothing could have invoked it", dropCalls)
		}
	})

	t.Run("duplicate-name create error clears the flag so the drop does not run", func(t *testing.T) {
		dropCalls := 0
		cleanup, err := createFreshDatabase(
			"dup",
			func() (bool, error) { return false, nil },
			func() error { return errors.New("duplicate database") },
			func(error) bool { return true }, // stands in for pgCreateDatabaseErrorProvesNothingCreated/mysqlCreateDatabaseErrorProvesNothingCreated reporting a duplicate-name code
			func() { dropCalls++ },
		)
		if err == nil {
			t.Fatal("createFreshDatabase returned a nil error for a failed create, want an error")
		}
		if cleanup == nil {
			t.Fatal("createFreshDatabase returned a nil cleanup for a failed create; a cleanup must still be registered so ambiguous errors are not silently leaked")
		}
		cleanup()
		if dropCalls != 0 {
			t.Fatalf("drop called %d times after cleanup(), want 0: a proven duplicate-name error must never trigger a drop", dropCalls)
		}
	})

	t.Run("insufficient-privilege create error clears the flag so the drop does not run", func(t *testing.T) {
		dropCalls := 0
		cleanup, err := createFreshDatabase(
			"priv",
			func() (bool, error) { return false, nil },
			func() error { return errors.New("insufficient privilege") },
			func(error) bool { return true }, // stands in for the classifier reporting an insufficient-privilege code
			func() { dropCalls++ },
		)
		if err == nil {
			t.Fatal("createFreshDatabase returned a nil error for a failed create, want an error")
		}
		cleanup()
		if dropCalls != 0 {
			t.Fatalf("drop called %d times after cleanup(), want 0: a proven insufficient-privilege error must never trigger a drop", dropCalls)
		}
	})

	t.Run("create error with no proof leaves the flag set so the drop still runs", func(t *testing.T) {
		dropCalls := 0
		cleanup, err := createFreshDatabase(
			"cancel",
			func() (bool, error) { return false, nil },
			func() error { return context.Canceled }, // an error carrying no driver-specific code at all
			func(error) bool { return false },        // stands in for neither classifier recognizing this error
			func() { dropCalls++ },
		)
		if err == nil {
			t.Fatal("createFreshDatabase returned a nil error for a failed create, want an error")
		}
		if cleanup == nil {
			t.Fatal("createFreshDatabase returned a nil cleanup for a failed create, want a cleanup so the leak stays closed")
		}
		cleanup()
		if dropCalls != 1 {
			t.Fatalf("drop called %d times after cleanup(), want exactly 1: an unproven error (e.g. a context cancellation) must still attempt the drop, since the database may have been created despite the client-visible error", dropCalls)
		}
	})

	t.Run("happy path creates once and the returned cleanup drops exactly once", func(t *testing.T) {
		probeCalls, createCalls, dropCalls := 0, 0, 0
		cleanup, err := createFreshDatabase(
			"ok",
			func() (bool, error) { probeCalls++; return false, nil },
			func() error { createCalls++; return nil },
			func(error) bool {
				t.Fatal("errorProvesNothingCreated must not be called: create succeeded")
				return false
			},
			func() { dropCalls++ },
		)
		if err != nil {
			t.Fatalf("createFreshDatabase returned an error for a successful create: %v", err)
		}
		if probeCalls != 1 || createCalls != 1 {
			t.Fatalf("probeExists called %d times and create called %d times, want exactly 1 each", probeCalls, createCalls)
		}
		if cleanup == nil {
			t.Fatal("createFreshDatabase returned a nil cleanup for a successful create, want a cleanup that drops the database it created")
		}
		cleanup()
		if dropCalls != 1 {
			t.Fatalf("drop called %d times after cleanup(), want exactly 1", dropCalls)
		}
	})
}

// TestPerTestCacheResolve pins the memoization PostgreSQLConfig and
// MySQLConfig rely on to avoid creating a second, disconnected fresh
// database when a live test calls them more than once for the same
// *testing.T (see openAsRole in inspect/postgresql_privilege_test.go):
// resolve must call create only once per *testing.T and return that same
// value on every later call for the same t, while a different *testing.T
// gets its own independent value.
func TestPerTestCacheResolve(t *testing.T) {
	var cache perTestCache[int]
	calls := 0
	create := func() int {
		calls++
		return calls
	}

	t.Run("subtest one", func(t *testing.T) {
		first := cache.resolve(t, create)
		second := cache.resolve(t, create)
		if first != second {
			t.Fatalf("resolve returned %d then %d for the same *testing.T, want the same value both times", first, second)
		}
	})

	t.Run("subtest two", func(t *testing.T) {
		third := cache.resolve(t, create)
		if third == 1 {
			t.Fatalf("resolve returned the first subtest's cached value %d for a different *testing.T, want create to run again", third)
		}
	})

	if calls != 2 {
		t.Fatalf("create ran %d times across two distinct *testing.T values sharing one cache, want exactly 2", calls)
	}
}
