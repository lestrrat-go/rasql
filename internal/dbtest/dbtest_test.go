//go:build unix

package dbtest

import "testing"

// TestDSNDecision pins the pure decision inside resolvePostgreSQLConfig and
// resolveMySQLConfig: given an environment variable's raw state, does
// resolution parse and use its value, or fall through to the Docker/skip
// path? It covers unset, a real value, an all-whitespace value (empty,
// single space, and tab, since dsnDecision treats them identically), and a
// real value padded with whitespace -- pinning that padding is trimmed
// away rather than treated as part of the value, that both unset and
// all-whitespace fall through, and that only a present-but-blank value
// asks for the diagnostic log.
//
// This is deliberately a test of dsnDecision alone, not of
// PostgreSQLConfig/MySQLConfig/resolvePostgreSQLConfig/resolveMySQLConfig
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
