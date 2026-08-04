package dbtest

import "testing"

// TestDSNDecision pins the pure decision inside resolveDSN: given an
// environment variable's raw state, does resolution use its value
// directly, or fall through to the Docker/skip path? It covers the three
// states resolveDSN cares about -- unset, set to a real value, and present
// but empty -- and pins that a present-but-empty value is never used
// directly, that both unset and present-but-empty fall through, and that
// only present-but-empty asks for the diagnostic log.
//
// This is deliberately a test of dsnDecision alone, not of
// PostgreSQLDSN/MySQLDSN/resolveDSN end-to-end: those reach
// ensureComposeUp, which runs `docker compose up` when Docker is
// reachable. On a CI runner that already has PostgreSQL and MySQL service
// containers bound to ports 5432/3306, that bring-up fails even though
// resolution itself made the right decision -- a test of the decision
// should not perform the action the decision leads to. dsnDecision takes
// no environment variable name and touches no Docker, so this test needs
// neither, and passes identically with Docker installed, absent, or
// already using the ports compose would want.
func TestDSNDecision(t *testing.T) {
	t.Run("unset falls through without logging", func(t *testing.T) {
		useEnv, shouldLog := dsnDecision("", false)
		if useEnv {
			t.Fatal("dsnDecision(\"\", false) reported useEnv; an unset variable must fall through to Docker/skip")
		}
		if shouldLog {
			t.Fatal("dsnDecision(\"\", false) reported shouldLog; an unset variable is not the present-but-empty case and must not log")
		}
	})

	t.Run("present but empty falls through and logs", func(t *testing.T) {
		useEnv, shouldLog := dsnDecision("", true)
		if useEnv {
			t.Fatal("dsnDecision(\"\", true) reported useEnv; a present-but-empty value must fall through to Docker/skip, never be used as a DSN")
		}
		if !shouldLog {
			t.Fatal("dsnDecision(\"\", true) did not report shouldLog; the present-but-empty diagnostic would be silently dropped")
		}
	})

	t.Run("set to a real value is used directly", func(t *testing.T) {
		const dsn = "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"
		useEnv, shouldLog := dsnDecision(dsn, true)
		if !useEnv {
			t.Fatal("dsnDecision reported !useEnv for a non-empty value; a set DSN must be used directly without touching Docker")
		}
		if shouldLog {
			t.Fatal("dsnDecision reported shouldLog for a non-empty value; only the present-but-empty case logs")
		}
	})
}
