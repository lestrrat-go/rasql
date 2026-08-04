//go:build unix

package dbtest

import (
	"database/sql"
	"fmt"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

// PostgreSQLConfig returns a parsed, validated PostgreSQL connection
// configuration for a live database, resolved as described in the package
// doc. It skips the calling test rather than returning an error when no DSN
// or usable Docker fallback is available.
//
// It returns pgx's own *pgx.ConnConfig rather than a DSN string on purpose.
// A caller that needs different credentials against the same server (see
// inspect/postgresql_privilege_test.go's openAsRole) must copy this config
// and change its User/Password fields directly, never rebuild and reparse
// a DSN string: an earlier version of this harness did exactly that, and a
// net/url round-trip of a keyword/value DSN corrupted it into a connection
// string pgx accepted but resolved to an unrelated target (a Unix socket,
// no database, the OS user). Handing back the already-parsed struct makes
// that class of bug impossible to reintroduce here, because there is no
// string left to round-trip.
func PostgreSQLConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	return resolvePostgreSQLConfig(t)
}

// PostgreSQLDB opens a live PostgreSQL connection via PostgreSQLConfig,
// registers t.Cleanup to close it, and pings it before returning.
func PostgreSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	config := PostgreSQLConfig(t)
	return openAndPing(t, stdlib.OpenDB(*config))
}

func resolvePostgreSQLConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	value, set := os.LookupEnv(postgresEnvVar)
	trimmed, useValue, shouldLog := dsnDecision(value, set)
	if !useValue {
		if shouldLog {
			blankDiagnostic(t, postgresEnvVar)
		}
		ensureComposeUp(t)
		// postgresComposeDSN is this package's own constant, not user
		// input, so a parse failure here would be a bug in this package
		// rather than something to validate against; require.NoError
		// -- unlike t.Fatalf below -- makes that distinction obvious to
		// anyone reading a failure.
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
	if reason := unusablePostgreSQLTarget(config); reason != "" {
		// Same reasoning as the parse-error branch above: a DSN that
		// parses but does not itself pin down where it connects is still
		// a wrong answer, not an absent one, once RASQL_TEST_POSTGRES_DSN
		// has been set. Falling through here would recreate exactly the
		// hazard this rescope closes -- pgconn.ParseConfig silently
		// substituting PG* environment variables or its own built-in
		// defaults for a DSN that does not specify its own target -- so
		// this fails loudly instead.
		t.Fatalf("%s is set but %s; a set DSN must specify its own host, port, and database rather than rely on PG* environment variables or pgx's built-in defaults", postgresEnvVar, reason)
	}
	return config
}

// unusablePostgreSQLTarget reports why config -- parsed from a
// caller-supplied, non-blank DSN -- must not be treated as usable, or ""
// when it is usable.
//
// pgconn.ParseConfig always merges the PG* libpq environment variables
// (PGHOST, PGPORT, PGDATABASE, ...) and its own built-in defaults (a Unix
// socket directory or "localhost" for the host, port 5432) underneath
// whatever the DSN's own text supplies -- see pgconn/config.go's
// mergeSettings(defaultSettings(), parseEnvSettings(), connStringSettings).
// This is what let a whitespace-only DSN read a PG*-pointed target and pass
// for a real one, the defect this rescope traces to its root: pgconn has no
// exported way to ask "did the connection string itself supply this field",
// so this function instead compares config's Host, Port, and Database
// against what pgx.ParseConfig("") alone would produce in the exact same
// process environment. A field equal to that baseline could not have come
// from the DSN text -- there is nothing else it could have come from except
// the environment or a hardcoded default -- so it is rejected.
//
// This can flag a small number of legitimate DSNs that happen to spell out
// the same value the baseline would already produce (host "localhost" set
// explicitly on a machine with no Unix socket directory present, for
// instance) as unusable. That false positive is accepted deliberately, the
// same bias classifyPortCollision documents in port_collision.go: when in
// doubt, stay narrow and conservative rather than accept a DSN whose real
// target this function cannot prove came from the DSN itself.
func unusablePostgreSQLTarget(config *pgx.ConnConfig) string {
	baseline, err := pgx.ParseConfig("")
	if err != nil {
		// ParseConfig("") failing is a pgx/environment problem this
		// package cannot repair by itself; say so rather than silently
		// treating config as usable because the baseline could not be
		// computed.
		return fmt.Sprintf("a baseline for comparison could not be established: %v", err)
	}
	switch {
	case config.Host == baseline.Host:
		return fmt.Sprintf("its host %q is indistinguishable from what PG* environment variables or pgx's built-in default would produce without it", config.Host)
	case config.Port == baseline.Port:
		return fmt.Sprintf("its port %d is indistinguishable from what PG* environment variables or pgx's built-in default would produce without it", config.Port)
	case config.Database == baseline.Database:
		if config.Database == "" {
			return "it does not specify a database"
		}
		return fmt.Sprintf("its database %q is indistinguishable from what PG* environment variables would produce without it", config.Database)
	default:
		return ""
	}
}
