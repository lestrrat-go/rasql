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
	}
	for _, tc := range unusable {
		t.Run(tc.name, func(t *testing.T) {
			config, err := pgx.ParseConfig(tc.dsn)
			if err != nil {
				t.Fatalf("pgx.ParseConfig(%q): %v", tc.dsn, err)
			}
			reason := unusablePostgreSQLTarget(config)
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
	}
	for _, tc := range usable {
		t.Run(tc.name, func(t *testing.T) {
			config, err := pgx.ParseConfig(tc.dsn)
			if err != nil {
				t.Fatalf("pgx.ParseConfig(%q): %v", tc.dsn, err)
			}
			if reason := unusablePostgreSQLTarget(config); reason != "" {
				t.Fatalf("unusablePostgreSQLTarget(%q) = %q, want \"\": this DSN names its own host %q, port %d, and database %q, none of which came from PGHOST/PGPORT/PGDATABASE",
					tc.dsn, reason, config.Host, config.Port, config.Database)
			}
		})
	}
}
