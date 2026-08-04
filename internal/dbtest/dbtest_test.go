//go:build unix

package dbtest

import (
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
	drop := pgDropDatabaseStatement(name)
	if drop != "DROP DATABASE "+wantIdent {
		t.Fatalf("pgDropDatabaseStatement(%q) = %q, want %q", name, drop, "DROP DATABASE "+wantIdent)
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
	drop := mysqlDropDatabaseStatement(name)
	if drop != "DROP DATABASE "+wantIdent {
		t.Fatalf("mysqlDropDatabaseStatement(%q) = %q, want %q", name, drop, "DROP DATABASE "+wantIdent)
	}
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
