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
	"github.com/lestrrat-go/rasql/internal/schemagen"
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
	view.RowName = "ActiveUserRow"
	users.RowName = "UserRow"
	_, filename, _, ok := runtime.Caller(0)
	require.True(t, ok)
	source, err := schemagen.PackageSource("generated", users, view)
	require.NoError(t, err)
	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "schema.go"), source, 0o600))
	usage := []byte("package generated_test\n\nimport (\n\t\"context\"\n\t\"database/sql\"\n\t\"fmt\"\n\t\"testing\"\n\t\"github.com/lestrrat-go/rasql\"\n\t\"github.com/lestrrat-go/rasql/dialect\"\n\t\"example.com/generated\"\n\t_ \"modernc.org/sqlite\"\n)\nfunc TestRead(t *testing.T) {\n db, err := sql.Open(\"sqlite\", `" + databasePath + "`); if err != nil { t.Fatal(err) }; defer db.Close(); rdb, _ := rasql.New(db, dialect.SQLite()); if _, err := rasql.Insert(context.Background(), rdb, generated.Users(), generated.UserRow{ID: 1, Email: \"ada@example.com\"}); err != nil { t.Fatal(err) }; rows, err := rasql.SelectFrom(generated.ActiveUsers()).All(context.Background(), rdb); if err != nil { t.Fatal(err) }; fmt.Println(rows[0].Email)\n}\n")
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
