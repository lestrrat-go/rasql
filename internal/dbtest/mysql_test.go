//go:build unix

package dbtest

import (
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestUnusableMySQLTarget covers both hazards unusableMySQLTarget guards
// against: a DSN whose own text does not name a database, which the driver
// accepts silently rather than erroring, and one whose own text does not
// name a network address (or a port, for a tcp address), which
// Config.normalize() defaults rather than erroring. See
// unusableMySQLTarget's comment for why mysql.Config's already-normalized
// fields cannot answer the address/port question by themselves, and why an
// omitted address is not a no-op the way an earlier version of this
// package's comment claimed.
func TestUnusableMySQLTarget(t *testing.T) {
	unusable := []struct {
		name  string
		dsn   string
		lacks string // substring the reason must name
	}{
		{"empty string", "", "database"},
		{"slash only, no database name", "/", "database"},
		{"host and port given but no database name", "tcp(127.0.0.1:3306)/", "database"},
		{"omits the address entirely", "rasql:rasql@/rasql", "network address"},
		{"omits the port", "rasql:rasql@tcp(127.0.0.1)/rasql", "port"},
		// The audit's own regression case: this DSN's unix() address is
		// empty in the text, and Config.normalize() resolves that empty
		// address to "/tmp/mysql.sock" -- not to any address this DSN's
		// text names -- disproving the claim that an omitted host/port
		// can only reach the same address a correct DSN would also name.
		{"empty unix() address resolves to a socket the text never names", "rasql:rasql@unix()/rasql", "network address"},
	}
	for _, tc := range unusable {
		t.Run(tc.name, func(t *testing.T) {
			config, err := mysql.ParseDSN(tc.dsn)
			if err != nil {
				t.Fatalf("mysql.ParseDSN(%q): %v", tc.dsn, err)
			}
			reason := unusableMySQLTarget(tc.dsn, config)
			if reason == "" {
				t.Fatalf("unusableMySQLTarget(%q) = \"\", want a reason naming %q", tc.dsn, tc.lacks)
			}
			if !strings.Contains(reason, tc.lacks) {
				t.Fatalf("unusableMySQLTarget(%q) = %q, want it to name %q as missing", tc.dsn, reason, tc.lacks)
			}
		})
	}

	usable := []struct {
		name string
		dsn  string
	}{
		{"valid keyword/value DSN naming its own address and database", "rasql:rasql@tcp(127.0.0.1:3306)/rasql?parseTime=true"},
		// A Unix socket address carries no port, and must not be rejected
		// for lacking one.
		{"unix socket address naming its own path", "rasql:rasql@unix(/var/run/mysqld/mysqld.sock)/rasql"},
		// A naive left-to-right split on '@' or '/' would misparse this
		// DSN, since the password itself contains both characters; the
		// address (host, port) and database must still be recovered
		// correctly.
		{"password containing @ and /", `rasql:p@ss/word@tcp(127.0.0.1:3306)/rasql`},
		// An IPv6 address must not be mistaken for a missing port because
		// of the colons inside the brackets.
		{"IPv6 address", "rasql:rasql@tcp([::1]:3306)/rasql"},
	}
	for _, tc := range usable {
		t.Run(tc.name, func(t *testing.T) {
			config, err := mysql.ParseDSN(tc.dsn)
			if err != nil {
				t.Fatalf("mysql.ParseDSN(%q): %v", tc.dsn, err)
			}
			if reason := unusableMySQLTarget(tc.dsn, config); reason != "" {
				t.Fatalf("unusableMySQLTarget(%q) = %q, want \"\": this DSN names its own address and database in its text", tc.dsn, reason)
			}
		})
	}
}

// TestCIMySQLDSNIsUsable exercises unusableMySQLTarget against the exact
// literal DSN string CI sets RASQL_TEST_MYSQL_DSN to (see
// .github/workflows/ci.yml), asserting it is usable. It needs no database
// connection, only the validator; see TestCIPostgresDSNIsUsable in
// postgresql_test.go for why that gap -- a validator only ever exercised
// when a live DSN happens to be set -- is exactly what let a regression in
// this package reach CI unnoticed.
func TestCIMySQLDSNIsUsable(t *testing.T) {
	const dsn = "rasql:rasql@tcp(127.0.0.1:3306)/rasql?parseTime=true"
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("mysql.ParseDSN(%q): %v", dsn, err)
	}
	if reason := unusableMySQLTarget(dsn, config); reason != "" {
		t.Fatalf("unusableMySQLTarget(%q) = %q, want \"\": this is CI's own mysql DSN and must be usable", dsn, reason)
	}
}
