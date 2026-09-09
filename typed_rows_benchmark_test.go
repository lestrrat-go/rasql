package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
)

const (
	benchmarkDriverName   = "rasql-typed-row-scan"
	benchmarkFullQuery    = "SELECT id, name, email"
	benchmarkNameQuery    = "SELECT name"
	benchmarkRowsPerQuery = 10
	benchmarkLargeRows    = 10000
)

func init() {
	sql.Register(benchmarkDriverName, benchmarkDriver{})
}

type benchmarkMemberRow struct {
	ID    int64  `rasql:"id"`
	Name  string `rasql:"name"`
	Email string `rasql:"email"`
}

type benchmarkMemberName struct {
	Name string `rasql:"name"`
}

type benchmarkMemberDecoder struct{ schema ResultSchema }

func (d benchmarkMemberDecoder) ResultSchema() ResultSchema { return d.schema }
func (benchmarkMemberDecoder) Presence() []Presence         { return nil }
func (benchmarkMemberDecoder) DecodeRow(source ScanSource, row *benchmarkMemberRow) error {
	return source.Scan(&row.ID, &row.Name, &row.Email)
}

type benchmarkMemberNameDecoder struct{ schema ResultSchema }

func (d benchmarkMemberNameDecoder) ResultSchema() ResultSchema { return d.schema }
func (benchmarkMemberNameDecoder) Presence() []Presence         { return nil }
func (benchmarkMemberNameDecoder) DecodeRow(source ScanSource, row *benchmarkMemberName) error {
	return source.Scan(&row.Name)
}

func benchmarkExecutor(b *testing.B) Executor {
	b.Helper()
	database, err := sql.Open(benchmarkDriverName, "")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Error(err)
		}
	})
	db, err := New(database, dialect.SQLite())
	if err != nil {
		b.Fatal(err)
	}
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	if err != nil {
		b.Fatal(err)
	}
	executor, err := AsExecutor(db, profile)
	if err != nil {
		b.Fatal(err)
	}
	return executor
}

func benchmarkNativeQuery[R any](b *testing.B, sqlText string, projection Projection[R]) Query[R] {
	b.Helper()
	query, err := Native(NativeStatement{Engine: "sqlite", SQL: sqlText}, projection, Many)
	if err != nil {
		b.Fatal(err)
	}
	return query
}

// BenchmarkTypedRowScan preserves the established scan series while measuring
// the two canonical decoder choices through the same Executor and All path.
func BenchmarkTypedRowScan(b *testing.B) {
	fullSchema, err := NewResultSchema(
		ResultColumn{Name: "id", Type: schema.IntegerType{}},
		ResultColumn{Name: "name", Type: schema.TextType{}},
		ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	if err != nil {
		b.Fatal(err)
	}
	nameSchema, err := NewResultSchema(ResultColumn{Name: "name", Type: schema.TextType{}})
	if err != nil {
		b.Fatal(err)
	}
	fullStatic, err := NativeProjection[benchmarkMemberRow](benchmarkMemberDecoder{schema: fullSchema})
	if err != nil {
		b.Fatal(err)
	}
	fullDynamic, err := DynamicProjection[benchmarkMemberRow](fullSchema)
	if err != nil {
		b.Fatal(err)
	}
	nameStatic, err := NativeProjection[benchmarkMemberName](benchmarkMemberNameDecoder{schema: nameSchema})
	if err != nil {
		b.Fatal(err)
	}
	nameDynamic, err := DynamicProjection[benchmarkMemberName](nameSchema)
	if err != nil {
		b.Fatal(err)
	}

	for _, testCase := range []struct {
		name       string
		query      string
		projection Projection[benchmarkMemberRow]
	}{
		{name: "full_static_generated", query: benchmarkFullQuery, projection: fullStatic},
		{name: "full_dynamic", query: benchmarkFullQuery, projection: fullDynamic},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			executor := benchmarkExecutor(b)
			query := benchmarkNativeQuery(b, testCase.query, testCase.projection)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				rows, err := All(b.Context(), executor, query)
				if err != nil {
					b.Fatal(err)
				}
				if len(rows) != benchmarkRowsPerQuery || rows[0].ID != 7 {
					b.Fatalf("rows = %#v", rows)
				}
			}
			b.ReportMetric(float64(b.Elapsed())/float64(b.N*benchmarkRowsPerQuery), "ns/row")
		})
	}
	for _, testCase := range []struct {
		name       string
		projection Projection[benchmarkMemberName]
	}{
		{name: "partial_generated", projection: nameStatic},
		{name: "partial_dynamic", projection: nameDynamic},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			executor := benchmarkExecutor(b)
			query := benchmarkNativeQuery(b, benchmarkNameQuery, testCase.projection)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				rows, err := All(b.Context(), executor, query)
				if err != nil {
					b.Fatal(err)
				}
				if len(rows) != benchmarkRowsPerQuery || rows[0].Name != "Ada Lovelace" {
					b.Fatalf("rows = %#v", rows)
				}
			}
			b.ReportMetric(float64(b.Elapsed())/float64(b.N*benchmarkRowsPerQuery), "ns/row")
		})
	}
}

func benchmarkCollectionQuery(b *testing.B, limit *int) Query[benchmarkMemberRow] {
	b.Helper()
	table, err := ReadTableOf[benchmarkMemberRow](schema.TableDef{Name: "members", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
		{Name: "email", Type: schema.TextType{}},
	}})
	if err != nil {
		b.Fatal(err)
	}
	relation, err := SourceOf(table, "")
	if err != nil {
		b.Fatal(err)
	}
	id, err := BindColumn[benchmarkMemberRow, int64](relation, "id", "")
	if err != nil {
		b.Fatal(err)
	}
	name, err := BindColumn[benchmarkMemberRow, string](relation, "name", "")
	if err != nil {
		b.Fatal(err)
	}
	email, err := BindColumn[benchmarkMemberRow, string](relation, "email", "")
	if err != nil {
		b.Fatal(err)
	}
	resultSchema, err := NewResultSchema(
		ResultColumn{Name: "id", Type: schema.IntegerType{}},
		ResultColumn{Name: "name", Type: schema.TextType{}},
		ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	if err != nil {
		b.Fatal(err)
	}
	projection, err := NewProjection([]ProjectionItem{
		Item("id", id.Expr(), schema.IntegerType{}, ""),
		Item("name", name.Expr(), schema.TextType{}, ""),
		Item("email", email.Expr(), schema.TextType{}, ""),
	}, benchmarkMemberDecoder{schema: resultSchema})
	if err != nil {
		b.Fatal(err)
	}
	query := Select(relation.Source(), projection)
	if limit != nil {
		query, err = query.Limit(*limit)
		if err != nil {
			b.Fatal(err)
		}
	}
	return query
}

// BenchmarkCollectAll preserves the collection series and now measures the
// canonical All terminal with absent, exact, and deliberately large limits.
func BenchmarkCollectAll(b *testing.B) {
	for _, testCase := range []struct {
		name  string
		limit *int
	}{
		{name: "no_hint"},
		{name: "exact_hint", limit: benchmarkInt(benchmarkLargeRows)},
		{name: "capped_hint", limit: benchmarkInt(100_000_000)},
	} {
		b.Run(testCase.name, func(b *testing.B) {
			executor := benchmarkExecutor(b)
			query := benchmarkCollectionQuery(b, testCase.limit)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				rows, err := All(b.Context(), executor, query)
				if err != nil {
					b.Fatal(err)
				}
				if len(rows) != benchmarkLargeRows {
					b.Fatalf("got %d rows, want %d", len(rows), benchmarkLargeRows)
				}
			}
		})
	}
}

func benchmarkInt(value int) *int { return &value }

type benchmarkCountRow struct {
	ID int64
}

type benchmarkCountRowDecoder struct{ schema ResultSchema }

func (d benchmarkCountRowDecoder) ResultSchema() ResultSchema { return d.schema }
func (benchmarkCountRowDecoder) Presence() []Presence         { return nil }
func (benchmarkCountRowDecoder) DecodeRow(source ScanSource, result *benchmarkCountRow) error {
	return source.Scan(&result.ID)
}

func benchmarkCountBaseQuery(b *testing.B) Query[benchmarkCountRow] {
	b.Helper()
	table, err := ReadTableOf[benchmarkCountRow](schema.TableDef{
		Name:    "members",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	if err != nil {
		b.Fatal(err)
	}
	relation, err := SourceOf(table, "")
	if err != nil {
		b.Fatal(err)
	}
	id, err := BindColumn[benchmarkCountRow, int64](relation, "id", "")
	if err != nil {
		b.Fatal(err)
	}
	resultSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	if err != nil {
		b.Fatal(err)
	}
	projection, err := NewProjection(
		[]ProjectionItem{Item("id", id.Expr(), schema.IntegerType{}, "")},
		benchmarkCountRowDecoder{schema: resultSchema},
	)
	if err != nil {
		b.Fatal(err)
	}
	return Select(relation.Source(), projection)
}

func BenchmarkTypedSelectCount(b *testing.B) {
	executor := benchmarkExecutor(b)
	base := benchmarkCountBaseQuery(b)

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		query := CountQuery(base, false)
		count, err := One(b.Context(), executor, query)
		if err != nil {
			b.Fatal(err)
		}
		if count != 3 {
			b.Fatalf("got count %d, want 3", count)
		}
	}
}

type benchmarkDriver struct{}

func (benchmarkDriver) Open(string) (driver.Conn, error) { return benchmarkConn{}, nil }

type benchmarkConn struct{}

func (benchmarkConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}
func (benchmarkConn) Close() error { return nil }
func (benchmarkConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (benchmarkConn) QueryContext(_ context.Context, query string, args []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "COUNT(*)") {
		return &benchmarkResultRows{columns: []string{"count"}, values: []driver.Value{int64(3)}, remaining: 1}, nil
	}
	if strings.Contains(query, `FROM "members"`) {
		remaining := benchmarkLargeRows
		if len(args) > 0 {
			if limit, ok := args[len(args)-1].Value.(int64); ok && limit < int64(remaining) {
				remaining = int(limit)
			}
		}
		return &benchmarkResultRows{
			columns: []string{"id", "name", "email"},
			values:  []driver.Value{int64(7), "Ada Lovelace", "ada@example.com"}, remaining: remaining,
		}, nil
	}
	switch query {
	case benchmarkFullQuery:
		return &benchmarkResultRows{columns: []string{"id", "name", "email"}, values: []driver.Value{int64(7), "Ada Lovelace", "ada@example.com"}, remaining: benchmarkRowsPerQuery}, nil
	case benchmarkNameQuery:
		return &benchmarkResultRows{columns: []string{"name"}, values: []driver.Value{"Ada Lovelace"}, remaining: benchmarkRowsPerQuery}, nil
	default:
		return nil, fmt.Errorf("unsupported query %q", query)
	}
}

type benchmarkResultRows struct {
	columns   []string
	values    []driver.Value
	remaining int
}

func (r *benchmarkResultRows) Columns() []string { return r.columns }
func (*benchmarkResultRows) Close() error        { return nil }
func (r *benchmarkResultRows) Next(destinations []driver.Value) error {
	if r.remaining == 0 {
		return io.EOF
	}
	copy(destinations, r.values)
	r.remaining--
	return nil
}
