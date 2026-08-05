//go:build unix

package dbtest

import (
	"context"
	"errors"
	"fmt"
	"io"
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

// TestMySQLCreateDatabaseErrorProvesNothingCreated pins step 3 of the
// containment remedy for MySQL: which CREATE DATABASE errors prove this
// statement created nothing, and therefore may clear
// createFreshMySQLDatabase's dropOwned flag, versus which must leave it set
// so the registered cleanup still attempts the drop. This needs no live
// server: mysqlCreateDatabaseErrorProvesNothingCreated is a pure function
// over an error value, built by *mysql.MySQLError -- exactly what
// go-sql-driver hands back from a real server -- rather than by connecting
// to one.
//
// See TestPgCreateDatabaseErrorProvesNothingCreated's identical note on how
// this discriminates the fix from the bug it replaces: this test's "leaves
// the flag set" cases fail if the guard is deleted (made to always report
// true) and pass with the fix's actual bodies, confirmed by hand -- see the
// task report.
func TestMySQLCreateDatabaseErrorProvesNothingCreated(t *testing.T) {
	proven := []struct {
		name string
		err  error
	}{
		{"ER_DB_CREATE_EXISTS (1007)", &mysql.MySQLError{Number: mysqlDbCreateExists}},
		{"ER_DBACCESS_DENIED_ERROR (1044)", &mysql.MySQLError{Number: mysqlAccessDenied}},
		{"ER_SPECIFIC_ACCESS_DENIED_ERROR (1227)", &mysql.MySQLError{Number: mysqlSpecificAccessDenied}},
		{"an ER_DB_CREATE_EXISTS error wrapped by fmt.Errorf(%w)", fmt.Errorf("exec: %w", &mysql.MySQLError{Number: mysqlDbCreateExists})},
	}
	for _, tc := range proven {
		t.Run(tc.name, func(t *testing.T) {
			if !mysqlCreateDatabaseErrorProvesNothingCreated(tc.err) {
				t.Fatalf("mysqlCreateDatabaseErrorProvesNothingCreated(%v) = false, want true: this error proves CREATE DATABASE created nothing, so dropOwned must clear", tc.err)
			}
		})
	}

	unproven := []struct {
		name string
		err  error
	}{
		{"a MySQLError carrying an unrelated number (1205, ER_LOCK_WAIT_TIMEOUT)", &mysql.MySQLError{Number: 1205}},
		{"context.Canceled: no code at all", context.Canceled},
		{"context.DeadlineExceeded: no code at all", context.DeadlineExceeded},
		{"io.EOF: no code at all, e.g. a dropped connection", io.EOF},
		{"a plain wrapped error with no code", fmt.Errorf("connection reset: %w", errors.New("reset"))},
	}
	for _, tc := range unproven {
		t.Run(tc.name, func(t *testing.T) {
			if mysqlCreateDatabaseErrorProvesNothingCreated(tc.err) {
				t.Fatalf("mysqlCreateDatabaseErrorProvesNothingCreated(%v) = true, want false: this error does not prove CREATE DATABASE created nothing, so dropOwned must stay set and the drop must still be attempted", tc.err)
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
	const dsn = "root:root@tcp(127.0.0.1:3306)/rasql?parseTime=true"
	if _, err := mysql.ParseDSN(dsn); err != nil {
		t.Fatalf("mysql.ParseDSN(%q) = %v, want nil: this is CI's own mysql DSN and must still parse", dsn, err)
	}
}
