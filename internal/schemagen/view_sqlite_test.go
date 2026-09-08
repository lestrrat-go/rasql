package schemagen_test

import (
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestGeneratedSQLiteViewCanBeRead(t *testing.T) {
	databasePath := filepath.Join(t.TempDir(), "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, email TEXT NOT NULL)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE VIEW active_users AS SELECT id, email FROM users")
	require.NoError(t, err)
	inspector, err := inspect.New(database, dialect.SQLite())
	require.NoError(t, err)
	tables, err := catalog.FromQueryer(t.Context(), database, catalog.Options{Dialect: dialect.SQLite(), IncludeViews: true})
	require.NoError(t, err)
	view, err := inspector.Object(t.Context(), "active_users")
	require.NoError(t, err)
	users, err := inspector.Object(t.Context(), "users")
	require.NoError(t, err)
	require.Len(t, tables, 2)
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	directory := t.TempDir()
	require.NoError(t, compactStore(t, filepath.Join(directory, "generated"), users, view).Write())
	usage := []byte("package generated_test\n\n" +
		"import (\n" +
		"\t\"context\"\n\t\"database/sql\"\n\t\"fmt\"\n\t\"testing\"\n\n" +
		"\t\"github.com/lestrrat-go/rasql\"\n\t\"github.com/lestrrat-go/rasql/dialect\"\n\t\"example.com/generated/generated\"\n\t_ \"modernc.org/sqlite\"\n" +
		")\n\n" +
		"func TestRead(t *testing.T) {\n" +
		"\tctx := context.Background()\n" +
		"\tdb, err := sql.Open(\"sqlite\", `" + databasePath + "`)\n" +
		"\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tdefer db.Close()\n" +
		"\traw, err := rasql.New(db, dialect.SQLite())\n" +
		"\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tprofile, err := rasql.DiscoverEngineProfile(ctx, raw, \"sqlite-3.35\")\n" +
		"\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\texecutor, err := rasql.AsExecutor(raw, profile)\n" +
		"\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tcreatePlan, err := generated.NewUsersCreate().ID(1).Email(\"ada@example.com\").Plan()\n" +
		"\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tif _, err := rasql.ExecMutation(ctx, executor, createPlan); err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tviewSource, err := generated.ActiveUsers().Source(\"\")\n" +
		"\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tviewColumns, err := (generated.ActiveUsersColumns{}).Bind(viewSource)\n" +
		"\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tviewProjection, err := generated.ActiveUsersProjection(viewColumns)\n" +
		"\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\trows, err := rasql.All(ctx, executor, rasql.Select(viewSource.Source(), viewProjection))\n" +
		"\tif err != nil {\n\t\tt.Fatal(err)\n\t}\n" +
		"\tif len(rows) != 1 {\n\t\tt.Fatalf(\"got %d rows\", len(rows))\n\t}\n" +
		"\tfmt.Println(rows[0].Email)\n" +
		"}\n")
	require.NoError(t, os.WriteFile(filepath.Join(directory, "usage_test.go"), usage, 0o600))
	module := "module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nrequire modernc.org/sqlite v1.55.0\n\nreplace github.com/lestrrat-go/rasql => " + filepath.ToSlash(filepath.Join(filepath.Dir(filename), "../..")) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(directory, "go.mod"), []byte(module), 0o600))
	command := exec.Command("go", "mod", "tidy")
	command.Dir = directory
	output, err := command.CombinedOutput()
	require.NoError(t, err, "generated SQLite view dependencies failed:\n%s", output)
	command = exec.Command("go", "test", "./...")
	command.Dir = directory
	output, err = command.CombinedOutput()
	require.NoError(t, err, "generated SQLite view consumer failed:\n%s", output)
}
