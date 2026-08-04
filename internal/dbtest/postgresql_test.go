//go:build unix

package dbtest

import (
	"testing"

	"github.com/jackc/pgx/v5"
)

// TestPostgreSQLDSNMalformedFailsLoudly proves a set-but-unparseable DSN
// still fails loudly rather than falling through to Docker/skip: it is a
// concrete, wrong answer from the person who set it, not an absence of one.
// See resolvePostgreSQLServerConfig and the package doc.
//
// This drives pgx.ParseConfig directly, the same call
// resolvePostgreSQLServerConfig makes, rather than resolvePostgreSQLServerConfig
// itself: that function calls t.Fatalf, which this test cannot observe
// without a live *testing.T failure, so the parse failure it depends on is
// pinned here instead.
func TestPostgreSQLDSNMalformedFailsLoudly(t *testing.T) {
	malformed := []string{
		"postgres://[::1",          // unterminated IPv6 literal
		"host=127.0.0.1 dbname",    // keyword/value missing '='
		"host='unterminated quote", // unterminated quoted value
	}
	for _, dsn := range malformed {
		t.Run(dsn, func(t *testing.T) {
			if _, err := pgx.ParseConfig(dsn); err == nil {
				t.Fatalf("pgx.ParseConfig(%q) succeeded, want an error: this DSN is not valid syntax", dsn)
			}
		})
	}
}

// TestCIPostgresDSNParses exercises pgx.ParseConfig against the exact
// literal DSN string CI sets RASQL_TEST_POSTGRES_DSN to (see
// .github/workflows/ci.yml), asserting it still parses. It needs no
// database connection, only the parser, so unlike a live test it runs --
// and would catch a regression -- on any machine, including one where live
// tests are skipped because Docker is unusable.
func TestCIPostgresDSNParses(t *testing.T) {
	const dsn = "postgres://rasql:rasql@127.0.0.1:5432/rasql?sslmode=disable"
	if _, err := pgx.ParseConfig(dsn); err != nil {
		t.Fatalf("pgx.ParseConfig(%q) = %v, want nil: this is CI's own postgres DSN and must still parse", dsn, err)
	}
}
