package orm

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/conformance"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite"
)

var benchmarkSink benchmarkProject

type benchmarkProject struct {
	ID   int64
	Name string
}

func benchmarkConformanceDatabase(b *testing.B) (*sql.DB, rasql.Executor, rasql.Query[benchmarkProject]) {
	b.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		b.Fatal(err)
	}
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)
	if err := conformance.SeedDatabase(b.Context(), database); err != nil {
		_ = database.Close()
		b.Fatal(err)
	}
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		_ = database.Close()
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = database.Close() })
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	if err != nil {
		b.Fatal(err)
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		b.Fatal(err)
	}
	table, err := rasql.TableOf[benchmarkProject](schema.TableDef{Name: "projects", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}},
	}})
	if err != nil {
		b.Fatal(err)
	}
	source, err := rasql.SourceOf(table, "m")
	if err != nil {
		b.Fatal(err)
	}
	id, err := rasql.BindColumn[benchmarkProject, int64](source, "id", "")
	if err != nil {
		b.Fatal(err)
	}
	name, err := rasql.BindColumn[benchmarkProject, string](source, "name", "")
	if err != nil {
		b.Fatal(err)
	}
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	if err != nil {
		b.Fatal(err)
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.Item("name", name.Expr(), schema.TextType{}, ""),
	}, benchmarkDecoder{schema: resultSchema})
	if err != nil {
		b.Fatal(err)
	}
	query := rasql.Select(source.Source(), projection).Where(rasql.EqualValue(id.Expr(), int64(1))).OrderBy(rasql.AscExpr(id.Expr()))
	return database, executor, query
}

type benchmarkDecoder struct{ schema rasql.ResultSchema }

func (d benchmarkDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (d benchmarkDecoder) Presence() []rasql.Presence       { return nil }
func (d benchmarkDecoder) DecodeRow(source rasql.ScanSource, row *benchmarkProject) error {
	return source.Scan(&row.ID, &row.Name)
}

func BenchmarkConformanceHandwrittenRead(b *testing.B) {
	database, _, _ := benchmarkConformanceDatabase(b)
	benchmarkSingleSemantic(b, database)
	if value, err := benchmarkSQLSingle(b.Context(), database); err != nil {
		b.Fatal(err)
	} else {
		benchmarkSink = value
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		value, err := benchmarkSQLSingle(b.Context(), database)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkSink = value
	}
}

func BenchmarkConformanceRasqlRead(b *testing.B) {
	database, executor, query := benchmarkConformanceDatabase(b)
	benchmarkSingleSemantic(b, database)
	if value, err := rasql.One(b.Context(), executor, query); err != nil {
		b.Fatal(err)
	} else {
		benchmarkSink = value
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		value, err := rasql.One(b.Context(), executor, query)
		if err != nil {
			b.Fatal(err)
		}
		benchmarkSink = value
	}
}

func benchmarkSQLSingle(ctx context.Context, database *sql.DB) (benchmarkProject, error) {
	rows, err := database.QueryContext(ctx, "SELECT id, name FROM projects WHERE id = ? ORDER BY id LIMIT 2", int64(1))
	if err != nil {
		return benchmarkProject{}, err
	}
	var value benchmarkProject
	if !rows.Next() {
		_ = rows.Close()
		return value, fmt.Errorf("single read returned no row")
	}
	if err := rows.Scan(&value.ID, &value.Name); err != nil {
		_ = rows.Close()
		return value, err
	}
	if rows.Next() {
		_ = rows.Close()
		return value, fmt.Errorf("single read returned a second row")
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return value, err
	}
	if err := rows.Close(); err != nil {
		return value, err
	}
	return value, nil
}

func benchmarkSingleSemantic(t testing.TB, database *sql.DB) {
	t.Helper()
	_, executor, query := benchmarkConformanceDatabaseFromDB(t, database)
	actual, err := rasql.One(t.Context(), executor, query)
	if err != nil {
		t.Fatal(err)
	}
	expected, err := benchmarkSQLSingle(t.Context(), database)
	if err != nil {
		t.Fatal(err)
	}
	want := benchmarkProject{ID: 1, Name: "project-001"}
	if actual != want || expected != want {
		t.Fatalf("single result: rasql=%#v sql=%#v want=%#v", actual, expected, want)
	}
	left, _ := json.Marshal(actual)
	right, _ := json.Marshal(expected)
	if string(left) != string(right) {
		t.Fatalf("single digest differs: rasql=%s sql=%s", left, right)
	}
}

func benchmarkConformanceDatabaseFromDB(t testing.TB, database *sql.DB) (*sql.DB, rasql.Executor, rasql.Query[benchmarkProject]) {
	t.Helper()
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		t.Fatal(err)
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	if err != nil {
		t.Fatal(err)
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		t.Fatal(err)
	}
	return database, executor, benchmarkProjectQuery()
}

func benchmarkProjectQuery() rasql.Query[benchmarkProject] {
	table, err := rasql.TableOf[benchmarkProject](schema.TableDef{Name: "projects", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}}})
	if err != nil {
		panic(err)
	}
	source, err := rasql.SourceOf(table, "p")
	if err != nil {
		panic(err)
	}
	id, err := rasql.BindColumn[benchmarkProject, int64](source, "id", "")
	if err != nil {
		panic(err)
	}
	name, err := rasql.BindColumn[benchmarkProject, string](source, "name", "")
	if err != nil {
		panic(err)
	}
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	if err != nil {
		panic(err)
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, ""), rasql.Item("name", name.Expr(), schema.TextType{}, "")}, benchmarkDecoder{schema: resultSchema})
	if err != nil {
		panic(err)
	}
	return rasql.Select(source.Source(), projection).Where(rasql.EqualValue(id.Expr(), int64(1))).OrderBy(rasql.AscExpr(id.Expr()))
}
