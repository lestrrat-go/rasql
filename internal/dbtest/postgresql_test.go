//go:build unix

package dbtest

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestUnusablePostgreSQLTarget proves the DSN chokepoint this rescope
// exists to close: pgx.ParseConfig(" ") -- and any other DSN that does not
// itself specify a host, port, and database -- must never be accepted as
// usable, because pgconn.ParseConfig silently substitutes PG* environment
// variables or its own built-in defaults for whatever the DSN left
// unspecified. It sets PGHOST/PGPORT/PGDATABASE to a deliberately
// different, obviously-wrong target for the run, so a false "usable" here
// would be caught rather than accidentally agreeing with the real target.
func TestUnusablePostgreSQLTarget(t *testing.T) {
	t.Setenv("PGHOST", "pg-env-should-never-be-used.invalid")
	t.Setenv("PGPORT", "6543")
	t.Setenv("PGDATABASE", "env_only_database")

	unusable := []struct {
		name string
		dsn  string
	}{
		{"empty string", ""},
		{"single space", " "},
		{"tab", "\t"},
		{"only a parameter with no host/port/dbname", "sslmode=disable"},
		{"only credentials, no host/port/dbname", "user=rasql password=rasql"},
		{"omits port while PGPORT is set", "host=127.0.0.1 dbname=rasql user=rasql password=rasql sslmode=disable"},
		{"omits host while PGHOST is set", "port=5432 dbname=rasql user=rasql password=rasql sslmode=disable"},
	}
	for _, tc := range unusable {
		t.Run(tc.name, func(t *testing.T) {
			config, err := pgx.ParseConfig(tc.dsn)
			if err != nil {
				t.Fatalf("pgx.ParseConfig(%q): %v", tc.dsn, err)
			}
			reason := unusablePostgreSQLTarget(tc.dsn, config)
			if reason == "" {
				t.Fatalf("unusablePostgreSQLTarget(%q) = \"\", want a reason: this DSN's host %q, port %d, database %q came from PGHOST/PGPORT/PGDATABASE, not the DSN itself",
					tc.dsn, config.Host, config.Port, config.Database)
			}
		})
	}

	usable := []struct {
		name string
		dsn  string
	}{
		{"valid URL DSN naming its own host, port, and database", "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"},
		{"valid keyword/value DSN naming its own host, port, and database", "host=127.0.0.1 port=5432 dbname=rasql user=rasql password=rasql sslmode=disable"},
		// The exact DSN CI sets RASQL_TEST_POSTGRES_DSN to (see
		// .github/workflows/ci.yml and dbtest.go's postgresComposeDSN).
		// This is the regression test for the bug that made CI red: the
		// baseline-comparison discriminator this function used to use
		// rejected this DSN because port 5432 -- named explicitly here --
		// is indistinguishable from pgx's own default port. Had this test
		// existed before, CI would never have gone red, since it needs no
		// database, only the validator. See TestCIPostgresDSNIsUsable for
		// this same assertion run in isolation.
		{"CI's own postgres DSN, naming the default port explicitly", "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"},
		// A non-default port proves acceptance is not an artifact of the
		// port happening to equal pgx's built-in default.
		{"valid URL DSN naming a non-default port", "postgres://rasql:rasql@127.0.0.1:5555/rasql?sslmode=disable"},
	}
	for _, tc := range usable {
		t.Run(tc.name, func(t *testing.T) {
			config, err := pgx.ParseConfig(tc.dsn)
			if err != nil {
				t.Fatalf("pgx.ParseConfig(%q): %v", tc.dsn, err)
			}
			if reason := unusablePostgreSQLTarget(tc.dsn, config); reason != "" {
				t.Fatalf("unusablePostgreSQLTarget(%q) = %q, want \"\": this DSN names its own host %q, port %d, and database %q, none of which came from PGHOST/PGPORT/PGDATABASE",
					tc.dsn, reason, config.Host, config.Port, config.Database)
			}
		})
	}
}

// TestCIPostgresDSNIsUsable exercises unusablePostgreSQLTarget against the
// exact literal DSN string CI sets RASQL_TEST_POSTGRES_DSN to (see
// .github/workflows/ci.yml), asserting it is usable. It needs no database
// connection, only the validator, so unlike TestDatabaseIntegration it runs
// -- and would have failed -- on any machine, including one where live
// tests are skipped because Docker is unusable. That is precisely the gap
// that let the baseline-comparison discriminator's regression reach CI
// unnoticed: nothing exercised the validator against a real DSN unless a
// live database was also available.
func TestCIPostgresDSNIsUsable(t *testing.T) {
	const dsn = "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("pgx.ParseConfig(%q): %v", dsn, err)
	}
	if reason := unusablePostgreSQLTarget(dsn, config); reason != "" {
		t.Fatalf("unusablePostgreSQLTarget(%q) = %q, want \"\": this is CI's own postgres DSN and must be usable", dsn, reason)
	}
}
