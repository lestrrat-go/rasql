//go:build unix

package conformance

import (
	"bytes"
	"database/sql"
	"flag"
	"net"
	"net/url"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/stretchr/testify/require"
)

// updateFixtures is the one sanctioned way to refresh a conformance fixture's checked-in store
// and rasql.sum: go test ./internal/conformance/ -run TestConformanceFixturesAreCurrent
// -update-fixtures, mirroring go test ./examples/ -update-docs.
var updateFixtures = flag.Bool("update-fixtures", false, "regenerate internal/conformance fixtures from a database instead of checking them")

// TestConformanceFixturesAreCurrent proves each checked-in conformance fixture -- the generated
// store and rasql.sum under testdata/<engine> -- is exactly what codegen generate produces from
// that fixture's own migrations, applied to a real server, today. SQLite runs unconditionally,
// over a scratch database -scratch builds and drops itself; PostgreSQL and MySQL run over a fresh
// database internal/dbtest creates for this test, with the fixture's migrations replayed onto it
// through migrate.Runner.Apply, and skip by name when the relevant live DSN environment variable
// is unset. With -update-fixtures every subtest instead runs generate and overwrites the fixture.
func TestConformanceFixturesAreCurrent(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		checkOrUpdateFixture(t, "sqlite", "", true)
	})
	t.Run("postgresql", func(t *testing.T) {
		dsn := postgresFixtureDSN(t)
		checkOrUpdateFixture(t, "postgresql", dsn, false)
	})
	t.Run("mysql", func(t *testing.T) {
		dsn := mysqlFixtureDSN(t)
		checkOrUpdateFixture(t, "mysql", dsn, false)
	})
}

// checkOrUpdateFixture runs codegen check (or, with -update-fixtures, codegen generate) in-process
// against the real, checked-in testdata/<engine>/rasql.json -- not a copy -- so that a passing run
// is a claim about the fixture as it actually sits in the repository. SQLite passes scratch true
// and no DSN, building and dropping its own throwaway database. PostgreSQL and MySQL pass scratch
// false with dsn naming the already-fresh, already-migrated database postgresFixtureDSN or
// mysqlFixtureDSN prepared, reaching that database directly rather than through a second, nested
// scratch database -- see postgresFixtureDSN's comment for why -scratch itself is not an option
// here.
func checkOrUpdateFixture(t *testing.T, engine, dsn string, scratch bool) {
	t.Helper()
	configPath := filepath.Join("testdata", engine, "rasql.json")
	command := "check"
	if *updateFixtures {
		command = "generate"
	}
	args := []string{command, "-config", configPath}
	if scratch {
		args = append(args, "-scratch")
	}
	if dsn != "" {
		args = append(args, "-dsn", dsn)
	}
	var output, diagnostics bytes.Buffer
	err := rasqlgen.RunContext(t.Context(), args, &output, &diagnostics)
	require.NoError(t, err, "output=%s diagnostics=%s", output.String(), diagnostics.String())
}

// postgresFixtureDSN gives back a real, parseable connection string naming the fresh database
// internal/dbtest created for this test, with testdata/postgresql/migrations already applied to
// it. It is assembled from pgx.ConnConfig's current fields rather than pgx.ConnConfig.ConnString(),
// which returns the string as originally parsed, before dbtest.PostgreSQLConfig repoints .Database
// at the fresh per-test database, so it would still name dbtest's own bootstrap database.
//
// T10's own live tests reach around that same staleness with stdlib.RegisterConnConfig, which
// hands database/sql an opaque key that only the stdlib driver's own Open resolves back to the
// live config. That key would work here too, now that generation reaches PostgreSQL only through
// database/sql; a literal connection string is kept because it is also what a reader can paste
// into psql when a fixture run fails.
func postgresFixtureDSN(t *testing.T) string {
	t.Helper()
	db := dbtest.PostgreSQLDB(t)
	applyFixtureMigrations(t, db, "postgresql", dialect.PostgreSQL())
	return postgresConnString(dbtest.PostgreSQLConfig(t))
}

func postgresConnString(cfg *pgx.ConnConfig) string {
	sslmode := "require"
	if cfg.TLSConfig == nil {
		sslmode = "disable"
	}
	u := url.URL{
		Scheme: "postgres",
		User:   url.UserPassword(cfg.User, cfg.Password),
		Host:   net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))),
		Path:   "/" + cfg.Database,
	}
	q := url.Values{}
	q.Set("sslmode", sslmode)
	u.RawQuery = q.Encode()
	return u.String()
}

// mysqlFixtureDSN is postgresFixtureDSN's MySQL twin. mysql.Config.FormatDSN always reflects the
// config's current fields, so it carries none of ConnString's staleness and needs no registration.
func mysqlFixtureDSN(t *testing.T) string {
	t.Helper()
	db := dbtest.MySQLDB(t)
	applyFixtureMigrations(t, db, "mysql", dialect.MySQL())
	return dbtest.MySQLConfig(t).FormatDSN()
}

func applyFixtureMigrations(t *testing.T, db *sql.DB, engine string, d dialect.Dialect) {
	t.Helper()
	migrations, err := migrationdir.Load(filepath.Join("testdata", engine, "migrations"))
	require.NoError(t, err)
	runner, err := migrate.New(db, d)
	require.NoError(t, err)
	_, err = runner.Apply(t.Context(), migrate.AllPending(), migrations...)
	require.NoError(t, err)
}
