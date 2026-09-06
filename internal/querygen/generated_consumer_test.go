package querygen_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestGeneratedCardinalityConsumersCompileExecuteAndScan(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	dir := t.TempDir()
	makeSource := func(name string, cardinality querydescribe.Cardinality) []byte {
		sqlText := "SELECT id AS id FROM rows"
		if cardinality == querydescribe.ZeroOrOne {
			sqlText += " WHERE id = 99"
		}
		def := namedsql.QueryDef{Name: name, SQL: sqlText, Result: &querydescribe.Description{Cardinality: cardinality, Columns: []querydescribe.Column{{Name: "id", Binding: schema.GoBinding{Type: "int64"}}}}}
		source, sourceErr := querygen.GoSource(def, "generated", name)
		require.NoError(t, sourceErr)
		return source
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nreplace github.com/lestrrat-go/rasql => "+root+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "many_gen.go"), makeSource("Many", querydescribe.Many), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "optional_gen.go"), makeSource("Optional", querydescribe.ZeroOrOne), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "exact_gen.go"), makeSource("Exact", querydescribe.ExactlyOne), 0o644))
	testSource := `
import ("testing"; "database/sql"; "github.com/lestrrat-go/rasql"; "github.com/lestrrat-go/rasql/dialect"; _ "modernc.org/sqlite")
func TestGenerated(t *testing.T) { dbsql, _ := sql.Open("sqlite", ":memory:"); defer dbsql.Close(); db, _ := rasql.New(dbsql, dialect.SQLite()); dbsql.Exec("CREATE TABLE rows(id INTEGER)"); dbsql.Exec("INSERT INTO rows VALUES (1),(2)"); rows, _ := QueryMany(t.Context(), db); var n int; for row, err := range rows { if err != nil || row.ID < 1 { t.Fatal(row, err) }; n++ }; if n != 2 { t.Fatal(n) }; _, found, err := QueryOptionalOne(t.Context(), db); if err != nil || found { t.Fatal(found, err) }; _, err = QueryExactOne(t.Context(), db); if !errors.Is(err, rasql.ErrMultipleRows) { t.Fatal(err) }; var row OptionalRow; if _, err := row.ScanDestinations([]string{"id", "id"}); err == nil { t.Fatal("duplicate accepted") }; if _, err := row.ScanDestinations([]string{"missing"}); err == nil { t.Fatal("unknown accepted") } }
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "consumer_test.go"), []byte("package generated\n\nimport \"errors\"\n"+testSource), 0o644))
	cmd := exec.CommandContext(context.Background(), "go", "test", "-mod=mod", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".tmp", "gocache"))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
