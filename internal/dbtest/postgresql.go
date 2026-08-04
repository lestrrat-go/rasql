//go:build unix

package dbtest

import (
	"database/sql"
	"fmt"
	"os"
	"sync"
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
	if reason := unusablePostgreSQLTarget(trimmed, config); reason != "" {
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

// libpqEnvVars lists every environment variable pgconn's parseEnvSettings
// reads (pgconn/config.go's nameMap). It must stay in sync with that list;
// pgx exposes no way to ask it for the list itself, and no way to make
// ParseConfig ignore it either -- see parseWithoutLibpqEnv.
var libpqEnvVars = []string{
	"PGHOST", "PGPORT", "PGDATABASE", "PGUSER", "PGPASSWORD", "PGPASSFILE",
	"PGAPPNAME", "PGCONNECT_TIMEOUT", "PGSSLMODE", "PGSSLKEY", "PGSSLCERT",
	"PGSSLSNI", "PGSSLROOTCERT", "PGSSLPASSWORD", "PGSSLNEGOTIATION",
	"PGTARGETSESSIONATTRS", "PGSERVICE", "PGSERVICEFILE", "PGTZ", "PGOPTIONS",
	"PGMINPROTOCOLVERSION", "PGMAXPROTOCOLVERSION", "PGCHANNELBINDING",
	"PGREQUIREAUTH",
}

// pgEnvMu serializes parseWithoutLibpqEnv calls against each other, since
// each one briefly mutates process-wide environment variables. See
// parseWithoutLibpqEnv for why that mutation is safe despite being
// process-wide.
var pgEnvMu sync.Mutex

// parseWithoutLibpqEnv parses dsn the same way pgx.ParseConfig does, except
// with every variable in libpqEnvVars hidden from the parser, so the result
// reflects only what dsn's own text supplies (plus pgx's built-in
// defaults) and never anything the ambient environment would have
// contributed.
//
// pgconn's parseEnvSettings reads PG* variables with a direct os.Getenv
// call (see pgconn/config.go), and ParseConfigWithOptions's
// ParseConfigOptions has no hook to substitute a different environment
// lookup, so there is no way to ask pgx to ignore the environment other
// than actually hiding it: this function unsets every name in
// libpqEnvVars for the duration of the call and restores the previous
// values (present or absent) before returning, even on panic.
//
// That is a process-wide mutation, which a concurrent t.Parallel test could
// in principle observe. It is safe here for two reasons: pgEnvMu
// serializes every call this package makes into this function, so no two
// invocations from this package ever overlap, and no test in this
// repository's dbtest package (nor its one caller,
// database_integration_test.go) calls t.Parallel -- grep confirms it -- so
// nothing else running in the same test binary reads PGHOST/PGPORT/etc.
// during the narrow window this function holds them unset.
func parseWithoutLibpqEnv(dsn string) (*pgx.ConnConfig, error) {
	pgEnvMu.Lock()
	defer pgEnvMu.Unlock()

	saved := make(map[string]string, len(libpqEnvVars))
	wasSet := make(map[string]bool, len(libpqEnvVars))
	for _, name := range libpqEnvVars {
		if value, ok := os.LookupEnv(name); ok {
			saved[name] = value
			wasSet[name] = true
			os.Unsetenv(name)
		}
	}
	defer func() {
		for _, name := range libpqEnvVars {
			if wasSet[name] {
				os.Setenv(name, saved[name])
			}
		}
	}()

	return pgx.ParseConfig(dsn)
}

// unusablePostgreSQLTarget reports why config -- parsed from
// caller-supplied, non-blank dsn -- must not be treated as usable, or ""
// when it is usable.
//
// pgconn.ParseConfig always merges the PG* libpq environment variables
// (PGHOST, PGPORT, PGDATABASE, ...) and its own built-in defaults (a Unix
// socket directory or "localhost" for the host, port 5432) underneath
// whatever the DSN's own text supplies -- see pgconn/config.go's
// mergeSettings(defaultSettings(), parseEnvSettings(), connStringSettings).
// An earlier version of this function tried to detect that by comparing
// config's Host, Port, and Database against what pgx.ParseConfig("") alone
// would produce -- but that baseline is unsound: a DSN that explicitly
// names the default port (5432, overwhelmingly the common case) parses
// identically to one that omitted it, so the comparison could not tell
// "explicitly 5432" from "silently defaulted to 5432" and rejected almost
// every real-world DSN, including CI's own.
//
// This function instead parses dsn a second time with libpqEnvVars hidden
// from the parser (see parseWithoutLibpqEnv) and compares that result
// against config, which was parsed in the ambient environment. If the two
// disagree, some field's value depended on what PG* variables happened to
// be set, so dsn does not by itself determine the target and is rejected.
// If they agree, dsn fully determines the target regardless of whether
// that target happens to equal a built-in default -- an explicit port 5432
// is accepted precisely because it parses to 5432 whether or not PG* is
// visible, which is what makes it deterministic rather than
// environment-derived. The hazard being closed is silent environment
// substitution, not the use of a documented default.
//
// An empty database is checked separately, up front: with no PGDATABASE
// set anywhere, both the ambient and the isolated parse agree on "", so the
// two-parse comparison alone would not catch it, yet an empty database is
// never a usable target.
func unusablePostgreSQLTarget(dsn string, config *pgx.ConnConfig) string {
	if config.Database == "" {
		return "it does not specify a database"
	}

	isolated, err := parseWithoutLibpqEnv(dsn)
	if err != nil {
		// dsn already parsed once to produce config, so a second parse of
		// the same text failing points at a pgx/environment problem this
		// package cannot repair by itself; say so rather than silently
		// treating config as usable because the comparison could not be
		// made.
		return fmt.Sprintf("a comparison parse with PG* environment variables hidden failed: %v", err)
	}
	switch {
	case config.Host != isolated.Host:
		return fmt.Sprintf("its host %q is indistinguishable from what PG* environment variables would supply in its place (hiding them parses it as %q instead)", config.Host, isolated.Host)
	case config.Port != isolated.Port:
		return fmt.Sprintf("its port %d is indistinguishable from what PG* environment variables would supply in its place (hiding them parses it as %d instead)", config.Port, isolated.Port)
	case config.Database != isolated.Database:
		return fmt.Sprintf("its database %q is indistinguishable from what PG* environment variables would supply in its place (hiding them parses it as %q instead)", config.Database, isolated.Database)
	default:
		return ""
	}
}
