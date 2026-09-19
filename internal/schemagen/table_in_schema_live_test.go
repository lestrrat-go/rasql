//go:build unix

package schemagen_test

import (
	"bytes"
	"context"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/stretchr/testify/require"
)

// TestGeneratedTableInSchemaAgainstLiveDatabases proves against real
// PostgreSQL and MySQL servers what a fixture test cannot: that a table the
// compact emitter generated can be moved to a second namespace at run time
// through its own InSchema method, and that a real INSERT built through the
// moved wrapper's Create method and a real SELECT built through its Table
// method actually reach that second namespace rather than the one the
// connection is already sitting in.
//
// TestConformanceFixturesAreCurrent, alongside this test, keeps asserting
// that codegen's output matches the checked-in fixtures; this test asserts
// what the generated wrapper does once a caller actually runs it.
func TestGeneratedTableInSchemaAgainstLiveDatabases(t *testing.T) {
	t.Run("postgresql", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)

		// widgets is created directly, through the live connection dbtest
		// gave this test, so codegen generate -dsn reads it back from the
		// server's own catalog rather than from a schema.TableDef this test
		// wrote by hand.
		_, err := database.ExecContext(t.Context(), "CREATE TABLE widgets (id BIGINT PRIMARY KEY, name TEXT NOT NULL)")
		require.NoError(t, err, "create the source table this test generates from")

		namespace := dbtest.UniqueName(t, "rasql_ns")
		_, err = database.ExecContext(t.Context(), `CREATE SCHEMA "`+namespace+`"`)
		require.NoError(t, err, "create the second schema this test moves into")

		dsn := postgresLiveDSN(dbtest.PostgreSQLConfig(t))
		runGeneratedInSchemaConsumer(t, "postgresql", dsn, namespace,
			`_ "github.com/jackc/pgx/v5/stdlib"`, "pgx", "PostgreSQL")
	})

	t.Run("mysql", func(t *testing.T) {
		database := dbtest.MySQLDB(t)

		_, err := database.ExecContext(t.Context(), "CREATE TABLE widgets (id BIGINT PRIMARY KEY, name TEXT NOT NULL)")
		require.NoError(t, err, "create the source table this test generates from")

		namespace := dbtest.UniqueName(t, "rasql_ns")
		_, err = database.ExecContext(t.Context(), "CREATE DATABASE `"+namespace+"`")
		require.NoError(t, err, "create the second database this test moves into")
		t.Cleanup(func() {
			// t.Context is already cancelled by the time cleanup runs.
			_, err := database.ExecContext(context.Background(), "DROP DATABASE `"+namespace+"`")
			require.NoError(t, err, "drop the second database this test created")
		})

		dsn := dbtest.MySQLConfig(t).FormatDSN()
		runGeneratedInSchemaConsumer(t, "mysql", dsn, namespace,
			`_ "github.com/go-sql-driver/mysql"`, "mysql", "MySQL")
	})
}

// runGeneratedInSchemaConsumer generates a store from the live database dsn
// names -- which must already hold the "widgets" table this test created --
// into a scratch module, then builds and runs a consumer proving InSchema,
// Create, Table and Select all reach namespace and leave the table where the
// connection is sitting untouched.
func runGeneratedInSchemaConsumer(t *testing.T, dialectConfig, dsn, namespace, driverImport, driverName, dialectFunc string) {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repository := filepath.Clean(filepath.Join(filepath.Dir(filename), "../.."))
	root := t.TempDir()

	// The module (go.mod, go.sum) is written before codegen generate runs,
	// because generate resolves its module root by walking up from
	// configPath's directory, and a bare t.TempDir() has no go.mod for it
	// to find.
	require.NoError(t, scratchmod.Write(root, repository, "example.com/inschema"))

	configPath := filepath.Join(root, "rasql.json")
	require.NoError(t, os.WriteFile(configPath,
		[]byte(`{"package": "store", "output": "internal/store", "dialect": "`+dialectConfig+`", "emitter": "compact"}`), 0o600))

	var output, diagnostics bytes.Buffer
	err := rasqlgen.RunContext(t.Context(), []string{"generate", "-config", configPath, "-dsn", dsn}, &output, &diagnostics)
	require.NoError(t, err, "output=%s diagnostics=%s", output.String(), diagnostics.String())

	consumer := "package store_test\n\n" +
		"import (\n" +
		"\t\"context\"\n\t\"database/sql\"\n\t\"testing\"\n\n" +
		"\t\"github.com/lestrrat-go/rasql\"\n\t\"github.com/lestrrat-go/rasql/dialect\"\n" +
		"\t\"example.com/inschema/internal/store\"\n" +
		"\t" + driverImport + "\n" +
		")\n\n" +
		"func TestTableInSchemaAgainstLiveDatabase(t *testing.T) {\n" +
		"\tctx := context.Background()\n" +
		"\tdb, err := sql.Open(\"" + driverName + "\", `" + dsn + "`)\n" +
		"\tif err != nil { t.Fatal(err) }\n" +
		"\tdefer db.Close()\n" +
		"\texecutor, err := rasql.Open(ctx, db, dialect." + dialectFunc + "())\n" +
		"\tif err != nil { t.Fatal(err) }\n" +
		"\thome := store.Widgets()\n" +
		"\tmoved, err := home.InSchema(`" + namespace + "`)\n" +
		"\tif err != nil { t.Fatal(err) }\n" +
		"\tif err := rasql.CreateTable(ctx, executor, moved); err != nil { t.Fatal(err) }\n" +
		"\tplan, err := moved.Create().ID(1).Name(\"ada\").Plan()\n" +
		"\tif err != nil { t.Fatal(err) }\n" +
		"\tif _, err := rasql.ExecMutation(ctx, executor, plan); err != nil { t.Fatal(err) }\n" +
		"\tprojection, err := store.WidgetsProjection(moved)\n" +
		"\tif err != nil { t.Fatal(err) }\n" +
		"\trow, err := rasql.One(ctx, executor, rasql.Select(moved, projection).Where(rasql.EqualValue(moved.ID.Expr(), int64(1))))\n" +
		"\tif err != nil { t.Fatal(err) }\n" +
		"\tif row.Name != \"ada\" { t.Fatalf(\"moved row = %#v\", row) }\n" +
		"\thomeProjection, err := store.WidgetsProjection(home)\n" +
		"\tif err != nil { t.Fatal(err) }\n" +
		"\thomeRows, err := rasql.All(ctx, executor, rasql.Select(home, homeProjection))\n" +
		"\tif err != nil { t.Fatal(err) }\n" +
		"\tif len(homeRows) != 0 { t.Fatalf(\"home table unexpectedly holds rows: %#v\", homeRows) }\n" +
		"}\n"
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "store", "in_schema_consumer_test.go"), []byte(consumer), 0o600))

	command := exec.Command("go", "test", "-mod=mod", "./...")
	command.Dir = root
	out, err := command.CombinedOutput()
	require.NoError(t, err, "%s", out)
}

// postgresLiveDSN builds a real, parseable connection string naming the
// fresh database config points at, the same way
// internal/conformance/fixtures_current_test.go's postgresFixtureDSN does:
// pgx.ConnConfig.ConnString reflects the string as originally parsed, before
// dbtest repoints .Database at the fresh per-test database, so the string is
// assembled from the config's current fields instead.
func postgresLiveDSN(cfg *pgx.ConnConfig) string {
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
