//go:build unix

package dbtest

import (
	"context"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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

// TestPgCreateDatabaseErrorProvesNothingCreated pins step 3 of the
// containment remedy for PostgreSQL: which CREATE DATABASE errors prove
// this statement created nothing, and therefore may clear
// createFreshPostgreSQLDatabase's dropOwned flag, versus which must leave it
// set so the registered cleanup still attempts the drop. This needs no live
// server: pgCreateDatabaseErrorProvesNothingCreated is a pure function over
// an error value, built by *pgconn.PgError -- exactly what pgx hands back
// from a real server -- rather than by connecting to one.
//
// This is also the test that discriminates the fix from the bug it
// replaces: with the guard deleted (i.e. this function always returning
// true, clearing dropOwned for every error), the "leaves the flag set"
// cases below fail, since they assert false; with the fix's actual bodies
// in place they pass. That was confirmed by hand -- see the task report --
// since a permanent "break the guard" variant does not belong in the
// checked-in suite.
func TestPgCreateDatabaseErrorProvesNothingCreated(t *testing.T) {
	proven := []struct {
		name string
		err  error
	}{
		{"duplicate_database (42P04)", &pgconn.PgError{Code: pgDuplicateDatabase}},
		{"unique_violation (23505), the accepted probe-race path", &pgconn.PgError{Code: pgUniqueViolation}},
		{"insufficient_privilege (42501)", &pgconn.PgError{Code: pgInsufficientPrivilege}},
		{"a duplicate_database error wrapped by fmt.Errorf(%w)", fmt.Errorf("exec: %w", &pgconn.PgError{Code: pgDuplicateDatabase})},
	}
	for _, tc := range proven {
		t.Run(tc.name, func(t *testing.T) {
			if !pgCreateDatabaseErrorProvesNothingCreated(tc.err) {
				t.Fatalf("pgCreateDatabaseErrorProvesNothingCreated(%v) = false, want true: this error proves CREATE DATABASE created nothing, so dropOwned must clear", tc.err)
			}
		})
	}

	unproven := []struct {
		name string
		err  error
	}{
		{"a PgError carrying an unrelated code (57014, query_canceled)", &pgconn.PgError{Code: "57014"}},
		{"context.Canceled: no code at all", context.Canceled},
		{"context.DeadlineExceeded: no code at all", context.DeadlineExceeded},
		{"io.EOF: no code at all, e.g. a dropped connection", io.EOF},
		{"a plain wrapped error with no code", fmt.Errorf("connection reset: %w", errors.New("reset"))},
	}
	for _, tc := range unproven {
		t.Run(tc.name, func(t *testing.T) {
			if pgCreateDatabaseErrorProvesNothingCreated(tc.err) {
				t.Fatalf("pgCreateDatabaseErrorProvesNothingCreated(%v) = true, want false: this error does not prove CREATE DATABASE created nothing, so dropOwned must stay set and the drop must still be attempted", tc.err)
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
