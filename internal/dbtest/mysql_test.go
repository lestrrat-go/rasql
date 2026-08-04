//go:build unix

package dbtest

import (
	"testing"

	"github.com/go-sql-driver/mysql"
)

// TestUnusableMySQLTarget covers the narrower, non-environment hazard
// unusableMySQLTarget guards against: a DSN that parses but leaves the
// database name empty, which the driver accepts silently rather than
// erroring. See unusableMySQLTarget's comment for why an under-specified
// host/port is not checked the same way -- there is no meaningful
// "came from the DSN vs. came from a default" distinction to draw there,
// unlike PostgreSQL's PG*-environment-variable hazard this test's
// PostgreSQL sibling (TestUnusablePostgreSQLTarget) proves closed.
func TestUnusableMySQLTarget(t *testing.T) {
	unusable := []struct {
		name string
		dsn  string
	}{
		{"empty string", ""},
		{"slash only, no database name", "/"},
		{"host and port given but no database name", "tcp(127.0.0.1:3306)/"},
	}
	for _, tc := range unusable {
		t.Run(tc.name, func(t *testing.T) {
			config, err := mysql.ParseDSN(tc.dsn)
			if err != nil {
				t.Fatalf("mysql.ParseDSN(%q): %v", tc.dsn, err)
			}
			if reason := unusableMySQLTarget(config); reason == "" {
				t.Fatalf("unusableMySQLTarget(%q) = \"\", want a reason: this DSN's database name is %q", tc.dsn, config.DBName)
			}
		})
	}

	t.Run("valid keyword/value DSN naming its own database", func(t *testing.T) {
		config, err := mysql.ParseDSN("rasql:rasql@tcp(127.0.0.1:3306)/rasql?parseTime=true")
		if err != nil {
			t.Fatalf("mysql.ParseDSN: %v", err)
		}
		if reason := unusableMySQLTarget(config); reason != "" {
			t.Fatalf("unusableMySQLTarget = %q, want \"\": this DSN names its own database %q", reason, config.DBName)
		}
	})
}
