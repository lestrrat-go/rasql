//go:build unix

package dbtest

import (
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// TestUnusablePostgreSQLTarget proves the DSN chokepoint this rescope
// exists to close: a DSN whose own text does not name a host, a port, and
// a database must never be accepted as usable, because pgconn.ParseConfig
// silently substitutes PG* environment variables, a service file, or its
// own built-in defaults for whatever the text left unspecified. Unlike an
// earlier version of this test, none of these cases sets PG* environment
// variables at all: unusablePostgreSQLTarget no longer looks at the
// environment in any way, only at dsn's own text, so there is nothing here
// for an environment variable to influence.
func TestUnusablePostgreSQLTarget(t *testing.T) {
	unusable := []struct {
		name  string
		dsn   string
		lacks string // substring the reason must name
	}{
		{"empty string", "", "host"},
		{"single space", " ", "host"},
		{"tab", "\t", "host"},
		{"only a parameter with no host/port/dbname", "sslmode=disable", "host"},
		{"only credentials, no host/port/dbname", "user=rasql password=rasql", "host"},
		{"keyword/value omitting host", "port=5432 dbname=rasql user=rasql password=rasql sslmode=disable", "host"},
		{"keyword/value omitting port", "host=127.0.0.1 dbname=rasql user=rasql password=rasql sslmode=disable", "port"},
		{"keyword/value omitting database", "host=127.0.0.1 port=5432 user=rasql password=rasql sslmode=disable", "database"},
		{"URL omitting host", "postgres://rasql:rasql@:5432/rasql?sslmode=disable", "host"},
		{"URL omitting port", "postgres://rasql:rasql@127.0.0.1/rasql?sslmode=disable", "port"},
		{"URL omitting database", "postgres://rasql:rasql@127.0.0.1:5432/?sslmode=disable", "database"},
		// The audit's own regression case: this keyword/value DSN parses
		// via pgx's built-in default to a Unix socket host, not to
		// anything the text itself names, precisely because it omits
		// host -- proving the old baseline-comparison discriminator's
		// premise ("an omitted host/port can only reach the address a
		// correct DSN would name") did not hold for PostgreSQL either.
		{"keyword/value that pgx resolves to a Unix socket host", "port=5432 dbname=rasql user=rasql password=rasql sslmode=disable", "host"},
	}
	for _, tc := range unusable {
		t.Run(tc.name, func(t *testing.T) {
			reason := unusablePostgreSQLTarget(tc.dsn)
			if reason == "" {
				t.Fatalf("unusablePostgreSQLTarget(%q) = \"\", want a reason naming %q", tc.dsn, tc.lacks)
			}
			if !strings.Contains(reason, tc.lacks) {
				t.Fatalf("unusablePostgreSQLTarget(%q) = %q, want it to name %q as missing", tc.dsn, reason, tc.lacks)
			}
		})
	}

	service := []struct {
		name string
		dsn  string
	}{
		{"URL naming a service", "postgres://?service=myservice"},
		{"keyword/value naming a service", "service=myservice"},
		{"keyword/value naming a service and a service file", "service=myservice servicefile=/tmp/pg_service.conf"},
		// This DSN's text names its own host, port, and database, so
		// unusablePostgreSQLTarget alone would call it usable; only
		// ConnStringAllowedKeys catches the service key here, proving
		// that mechanism -- not an incidental host/port/database gap --
		// is what rejects a service reference.
		{"service key alongside a fully specified host, port, and database", "host=127.0.0.1 port=5432 dbname=rasql service=myservice"},
	}
	for _, tc := range service {
		t.Run(tc.name, func(t *testing.T) {
			// service/servicefile rejection happens in
			// resolvePostgreSQLConfig via pgx's own
			// ParseConfigOptions.ConnStringAllowedKeys (see
			// postgresConnStringAllowedKeys), not in
			// unusablePostgreSQLTarget: a service DSN can still name its
			// own host/port/database in text while a service file
			// silently overrides them later, so the text-provenance
			// check alone cannot see that hazard. Prove the rejection at
			// that chokepoint directly, the same call
			// resolvePostgreSQLConfig makes.
			_, err := pgx.ParseConfigWithOptions(tc.dsn, pgx.ParseConfigOptions{
				ParseConfigOptions: pgconn.ParseConfigOptions{ConnStringAllowedKeys: postgresConnStringAllowedKeys},
			})
			if err == nil {
				t.Fatalf("pgx.ParseConfigWithOptions(%q) succeeded, want a rejection: service/servicefile must never be accepted from a DSN string", tc.dsn)
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
		// This is the regression test for the bug that made CI red under
		// the old baseline-comparison discriminator, which rejected this
		// DSN because port 5432 -- named explicitly here -- is
		// indistinguishable from pgx's own default port. See
		// TestCIPostgresDSNIsUsable for this same assertion run in
		// isolation.
		{"CI's own postgres DSN, naming the default port explicitly", "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"},
		// A non-default port proves acceptance is not an artifact of the
		// port happening to equal pgx's built-in default.
		{"valid URL DSN naming a non-default port", "postgres://rasql:rasql@127.0.0.1:5555/rasql?sslmode=disable"},
		{"valid keyword/value DSN naming a non-default port", "host=127.0.0.1 port=5555 dbname=rasql user=rasql password=rasql sslmode=disable"},
	}
	for _, tc := range usable {
		t.Run(tc.name, func(t *testing.T) {
			if reason := unusablePostgreSQLTarget(tc.dsn); reason != "" {
				t.Fatalf("unusablePostgreSQLTarget(%q) = %q, want \"\": this DSN names its own host, port, and database in its text", tc.dsn, reason)
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
// that let the old baseline-comparison discriminator's regression reach CI
// unnoticed: nothing exercised the validator against a real DSN unless a
// live database was also available.
func TestCIPostgresDSNIsUsable(t *testing.T) {
	const dsn = "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"
	if reason := unusablePostgreSQLTarget(dsn); reason != "" {
		t.Fatalf("unusablePostgreSQLTarget(%q) = %q, want \"\": this is CI's own postgres DSN and must be usable", dsn, reason)
	}
}
