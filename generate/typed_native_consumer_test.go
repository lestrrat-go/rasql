package generate_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/stretchr/testify/require"
)

func TestTypedNativeSQLiteTwoColumnConsumer(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/typedconsumer\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nreplace github.com/lestrrat-go/rasql => "+filepath.ToSlash(root)+"\n"), 0o600))
	generated, err := querygen.TypedGoSource(querygen.TypedInput{Package: "queries", Function: "Find", Engine: "sqlite", SQL: "SELECT id, name FROM users WHERE id = ? OR id = ? ORDER BY id", Operation: "select", Cardinality: "many", Result: "FindResult", Decoder: "FindDecoder", Parameters: []compilerir.GoField{{Name: "id", Type: "int64"}}, ArgumentNames: []string{"id", "id"}, Results: []compilerir.GoField{{Name: "id", Type: "int64"}, {Name: "name", Type: "rasql.Nullable[string]", Nullable: true}}})
	require.NoError(t, err)
	require.NoError(t, os.Mkdir(filepath.Join(dir, "queries"), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "find_gen.go"), generated, 0o600))
	consumer := `package queries

import (
  "testing"
  "github.com/lestrrat-go/rasql"
  "github.com/lestrrat-go/rasql/dialect"
  "github.com/lestrrat-go/rasql/query"
  _ "modernc.org/sqlite"
  "database/sql"
)
func TestConsumer(t *testing.T) {
  db, err := sql.Open("sqlite", ":memory:"); if err != nil { t.Fatal(err) }; defer db.Close()
  if _, err = db.Exec("CREATE TABLE users (id INTEGER, name TEXT)"); err != nil { t.Fatal(err) }
  if _, err = db.Exec("INSERT INTO users VALUES (1, NULL), (2, 'two'), (3, 'three')"); err != nil { t.Fatal(err) }
  rdb, err := rasql.New(db, dialect.SQLite()); if err != nil { t.Fatal(err) }
  profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 1); if err != nil { t.Fatal(err) }
  executor, err := rasql.AsExecutor(rdb, profile); if err != nil { t.Fatal(err) }
  q, err := Find(1); if err != nil { t.Fatal(err) }
  rows, err := rasql.All(t.Context(), executor, q); if err != nil || len(rows) != 1 || rows[0].ID != 1 || rows[0].Name.Valid { t.Fatalf("all: %#v %v", rows, err) }
  one, err := rasql.One(t.Context(), executor, q); if err != nil || one.ID != 1 { t.Fatalf("one: %#v %v", one, err) }
  maybe, found, err := rasql.Maybe(t.Context(), executor, q); if err != nil || !found || maybe.ID != 1 { t.Fatalf("maybe: %#v %t %v", maybe, found, err) }
  _ = query.Bind
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "queries", "consumer_test.go"), []byte(consumer), 0o600))
	cmd := exec.CommandContext(t.Context(), "go", "test", "-mod=mod", "./queries")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOWORK=off", "GOCACHE="+filepath.Join(root, ".tmp", "go-cache"))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
