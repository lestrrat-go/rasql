package querygen_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestGeneratedCardinalityConsumersCompileExecuteAndScan(t *testing.T) {
	root, err := filepath.Abs("../..")
	require.NoError(t, err)
	dir := t.TempDir()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE rows(id INTEGER); INSERT INTO rows VALUES (1),(2); CREATE TABLE bad_rows(id INTEGER); INSERT INTO bad_rows VALUES ('bad')")
	require.NoError(t, err)
	reportSQL := `SELECT u.id AS user_id, p.nickname AS nickname, count(p.user_id) AS profile_count FROM users AS u LEFT JOIN profiles AS p ON p.user_id = u.id GROUP BY u.id, p.nickname ORDER BY u.id`
	_, err = db.ExecContext(t.Context(), "CREATE TABLE users(id INTEGER); CREATE TABLE profiles(user_id INTEGER, nickname TEXT); INSERT INTO users VALUES (1),(2); INSERT INTO profiles VALUES (1,'one')")
	require.NoError(t, err)
	reportDescription, err := querydescribe.NewSQLite(db).Describe(t.Context(), querydescribe.Request{Name: "Report", SQL: reportSQL, Cardinality: querydescribe.Many})
	require.NoError(t, err)
	reportSource, err := querygen.GoSource(namedsql.QueryDef{Name: "Report", SQL: reportSQL, Result: &reportDescription}, "generated", "Report")
	require.NoError(t, err)
	makeSource := func(name string, cardinality querydescribe.Cardinality) []byte {
		sqlText := "SELECT id AS id FROM rows"
		if name == "OptionalZero" || name == "ExactZero" {
			sqlText += " WHERE id = 99"
		}
		if name == "OptionalOne" || name == "ExactOne" {
			sqlText += " WHERE id = 1"
		}
		if name == "ManyBad" {
			sqlText = "SELECT id AS id FROM bad_rows"
		}
		description, describeErr := querydescribe.NewSQLite(db).Describe(t.Context(), querydescribe.Request{Name: name, SQL: sqlText, Cardinality: cardinality})
		require.NoError(t, describeErr)
		def := namedsql.QueryDef{Name: name, SQL: sqlText, Result: &description}
		source, sourceErr := querygen.GoSource(def, "generated", name)
		require.NoError(t, sourceErr)
		return source
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go.mod"), []byte("module example.com/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nreplace github.com/lestrrat-go/rasql => "+root+"\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "many_gen.go"), makeSource("Many", querydescribe.Many), 0o644))
	for _, name := range []string{"OptionalZero", "OptionalOne", "OptionalTwo"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+"_gen.go"), makeSource(name, querydescribe.ZeroOrOne), 0o644))
	}
	for _, name := range []string{"ExactZero", "ExactOne", "ExactTwo"} {
		require.NoError(t, os.WriteFile(filepath.Join(dir, name+"_gen.go"), makeSource(name, querydescribe.ExactlyOne), 0o644))
	}
	require.NoError(t, os.WriteFile(filepath.Join(dir, "many_bad_gen.go"), makeSource("ManyBad", querydescribe.Many), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "report_gen.go"), reportSource, 0o644))
	testSource := `
import ("testing"; "database/sql"; "github.com/lestrrat-go/rasql"; "github.com/lestrrat-go/rasql/dialect"; _ "modernc.org/sqlite")
func TestGenerated(t *testing.T) {
	dbsql, _ := sql.Open("sqlite", ":memory:"); defer dbsql.Close(); db, _ := rasql.New(dbsql, dialect.SQLite())
	dbsql.Exec("CREATE TABLE rows(id INTEGER)"); dbsql.Exec("INSERT INTO rows VALUES (1),(2)"); dbsql.Exec("CREATE TABLE bad_rows(id INTEGER)"); dbsql.Exec("INSERT INTO bad_rows VALUES ('bad')")
	dbsql.Exec("CREATE TABLE users(id INTEGER)"); dbsql.Exec("CREATE TABLE profiles(user_id INTEGER, nickname TEXT)")
	dbsql.Exec("INSERT INTO users VALUES (1),(2)"); dbsql.Exec("INSERT INTO profiles VALUES (1,'one')")
	rows, _ := QueryMany(t.Context(), db); var n int; for row, err := range rows { if err != nil || row.ID == nil || *row.ID < 1 { t.Fatal(row, err) }; n++ }; if n != 2 { t.Fatal(n) }
	reportRows, err := QueryReport(t.Context(), db); if err != nil { t.Fatal(err) }; var reports []ReportRow; for row, rowErr := range reportRows { if rowErr != nil { t.Fatal(rowErr) }; reports = append(reports, row) }; if len(reports) != 2 || reports[0].UserID == nil || *reports[0].UserID != 1 || reports[0].Nickname == nil || *reports[0].Nickname != "one" || reports[0].ProfileCount != 1 || reports[1].UserID == nil || *reports[1].UserID != 2 || reports[1].Nickname != nil || reports[1].ProfileCount != 0 { t.Fatal(reports) }
	if _, found, err := QueryOptionalZeroOne(t.Context(), db); err != nil || found { t.Fatal(found, err) }; if row, found, err := QueryOptionalOneOne(t.Context(), db); err != nil || !found || row.ID == nil || *row.ID != 1 { t.Fatal(row, found, err) }; if _, _, err := QueryOptionalTwoOne(t.Context(), db); !errors.Is(err, rasql.ErrMultipleRows) { t.Fatal(err) }
	if _, err := QueryExactZeroOne(t.Context(), db); !errors.Is(err, rasql.ErrNoRows) { t.Fatal(err) }; if row, err := QueryExactOneOne(t.Context(), db); err != nil || row.ID == nil || *row.ID != 1 { t.Fatal(row, err) }; if _, err := QueryExactTwoOne(t.Context(), db); !errors.Is(err, rasql.ErrMultipleRows) { t.Fatal(err) }
	badRows, _ := QueryManyBad(t.Context(), db); for _, err := range badRows { if err != nil { return } }; t.Fatal("expected generated Many scan error")
}
`
	require.NoError(t, os.WriteFile(filepath.Join(dir, "consumer_test.go"), []byte("package generated\n\nimport \"errors\"\n"+testSource), 0o644))
	cmd := exec.CommandContext(context.Background(), "go", "test", "-mod=mod", "./...")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".tmp", "gocache"))
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, string(output))
}
