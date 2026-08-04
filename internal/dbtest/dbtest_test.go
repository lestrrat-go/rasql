package dbtest_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
)

// TestPresentButEmptyDSNFallsThrough pins the behavior that a present-but-
// empty RASQL_TEST_*_DSN is treated the same as an unset one: resolution
// falls through to the Docker/skip path rather than using the empty value
// directly. This guards against a future edit turning present-but-empty
// into a hard failure or, worse, into "use it and let sql.Open see an
// empty DSN" -- see the comment above the os.LookupEnv check in
// resolveDSN (dbtest.go) for why both of those are unsafe.
//
// It cannot exercise the environment where Docker successfully brings up a
// database, since that depends on the machine running the suite. What it
// does check, on any machine: PostgreSQLDSN/MySQLDSN never return the empty
// string for a present-but-empty variable. If a regression made the empty
// value flow straight through instead of falling back to Docker, this
// would either observe that empty return directly, or -- since resolution
// no longer reaches ensureComposeUp -- fail to see the skip that a
// Docker-less machine like this one otherwise always produces.
func TestPresentButEmptyDSNFallsThrough(t *testing.T) {
	t.Run("postgresql", func(t *testing.T) {
		t.Setenv("RASQL_TEST_POSTGRES_DSN", "")
		dsn := dbtest.PostgreSQLDSN(t)
		if dsn == "" {
			t.Fatal("PostgreSQLDSN returned an empty DSN for a present-but-empty RASQL_TEST_POSTGRES_DSN; a present-but-empty value must fall through to the compose DSN, never flow through as-is")
		}
	})

	t.Run("mysql", func(t *testing.T) {
		t.Setenv("RASQL_TEST_MYSQL_DSN", "")
		dsn := dbtest.MySQLDSN(t)
		if dsn == "" {
			t.Fatal("MySQLDSN returned an empty DSN for a present-but-empty RASQL_TEST_MYSQL_DSN; a present-but-empty value must fall through to the compose DSN, never flow through as-is")
		}
	})
}
