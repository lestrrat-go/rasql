//go:build unix

package dbtest

import (
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestMySQLDSNMalformedFailsLoudly proves a set-but-unparseable DSN still
// fails loudly rather than falling through to Docker/skip: it is a
// concrete, wrong answer from the person who set it, not an absence of one.
// See resolveMySQLServerConfig and the package doc.
//
// This drives mysql.ParseDSN directly, the same call
// resolveMySQLServerConfig makes, rather than resolveMySQLServerConfig
// itself: that function calls t.Fatalf, which this test cannot observe
// without a live *testing.T failure, so the parse failure it depends on is
// pinned here instead.
func TestMySQLDSNMalformedFailsLoudly(t *testing.T) {
	malformed := []string{
		"tcp(127.0.0.1:3306/rasql",             // unterminated protocol address
		"rasql:rasql@tcp(127.0.0.1:3306)rasql", // missing dbname-separating slash
	}
	for _, dsn := range malformed {
		t.Run(dsn, func(t *testing.T) {
			if _, err := mysql.ParseDSN(dsn); err == nil {
				t.Fatalf("mysql.ParseDSN(%q) succeeded, want an error: this DSN is not valid syntax", dsn)
			}
		})
	}
}

// TestCIMySQLDSNParses exercises mysql.ParseDSN against the exact literal
// DSN string CI sets RASQL_TEST_MYSQL_DSN to (see
// .github/workflows/ci.yml), asserting it still parses. It needs no
// database connection, only the parser; see TestCIPostgresDSNParses in
// postgresql_test.go for why that gap -- a parser call only ever exercised
// when a live DSN happens to be set -- matters.
func TestCIMySQLDSNParses(t *testing.T) {
	const dsn = "rasql:rasql@tcp(127.0.0.1:3306)/rasql?parseTime=true"
	if _, err := mysql.ParseDSN(dsn); err != nil {
		t.Fatalf("mysql.ParseDSN(%q) = %v, want nil: this is CI's own mysql DSN and must still parse", dsn, err)
	}
}
