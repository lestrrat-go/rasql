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
	"github.com/lestrrat-go/rasql/internal/rowvalue"
	"github.com/lestrrat-go/rasql/schema"
)

const (
	benchmarkDriverName   = "rasql-typed-row-scan"
	benchmarkFullQuery    = "SELECT id, name, email"
	benchmarkNameQuery    = "SELECT name"
	benchmarkLargeQuery   = "SELECT id, name, email LARGE"
	benchmarkRowsPerQuery = 10
	benchmarkLargeRows    = 10000
)

func init() {
	sql.Register(benchmarkDriverName, benchmarkDriver{})
}

type benchmarkMemberRow struct {
	ID    int64
	Name  string
	Email string
}

func (r *benchmarkMemberRow) ScanRow(source ScanSource) error {
	return source.Scan(&r.ID, &r.Name, &r.Email)
}

// ScanDestinations mirrors what rasqlgen emits, so the benchmarks below measure
// the generated shape rather than a hand-tuned stand-in for it.
func (r *benchmarkMemberRow) ScanDestinations(columns []string) ([]any, error) {
	const (
		scanIndexID = iota
		scanIndexName
		scanIndexEmail
	)
	destinations := make([]any, len(columns))
	scanned := NewScanMask(3)
	var discard any
	for index, column := range columns {
		switch column {
		case "id":
			if !scanned.Mark(scanIndexID) {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
			destinations[index] = &r.ID
		case "name":
			if !scanned.Mark(scanIndexName) {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
			destinations[index] = &r.Name
		case "email":
			if !scanned.Mark(scanIndexEmail) {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
			destinations[index] = &r.Email
		default:
			destinations[index] = &discard
		}
	}
	return destinations, nil
}

type benchmarkMemberRowBool benchmarkMemberRow

func (r *benchmarkMemberRowBool) ScanDestinations(columns []string) ([]any, error) {
	destinations := make([]any, len(columns))
	var scannedID bool
	var scannedName bool
	var scannedEmail bool
	var discard any
	for index, column := range columns {
		switch column {
		case "id":
			if scannedID {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
			scannedID = true
			destinations[index] = &r.ID
		case "name":
			if scannedName {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
			scannedName = true
			destinations[index] = &r.Name
		case "email":
			if scannedEmail {
				return nil, fmt.Errorf("duplicate result column %q", column)
			}
			scannedEmail = true
			destinations[index] = &r.Email
		default:
			destinations[index] = &discard
		}
	}
	return destinations, nil
}

type benchmarkMemberName struct {
	Name string
}

func BenchmarkScanDestinations(b *testing.B) {
	columns := []string{"id", "name", "email"}
	b.Run("scanmask", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			var result benchmarkMemberRow
			destinations, err := result.ScanDestinations(columns)
			if err != nil {
				b.Fatal(err)
			}
			if len(destinations) != len(columns) {
				b.Fatalf("got %d destinations, want %d", len(destinations), len(columns))
			}
		}
	})
	b.Run("bool", func(b *testing.B) {
		b.ReportAllocs()
		for range b.N {
			var result benchmarkMemberRowBool
			destinations, err := result.ScanDestinations(columns)
			if err != nil {
				b.Fatal(err)
			}
			if len(destinations) != len(columns) {
				b.Fatalf("got %d destinations, want %d", len(destinations), len(columns))
			}
		}
	})
}

func BenchmarkTypedRowScan(b *testing.B) {
	b.Run("full_static_generated", func(b *testing.B) {
		b.ReportAllocs()
		benchmarkQueryRows(b, benchmarkFullQuery, benchmarkRowsPerQuery, func(rows *sql.Rows) {
			for result, err := range scanTypedRowsStatic[benchmarkMemberRow](rows) {
				if err != nil {
					b.Fatal(err)
				}
				if result.ID != 7 {
					b.Fatalf("scanned ID = %d, want 7", result.ID)
				}
			}
		})
	})

	b.Run("full_dynamic", func(b *testing.B) {
		b.ReportAllocs()
		benchmarkQueryRows(b, benchmarkFullQuery, benchmarkRowsPerQuery, func(rows *sql.Rows) {
			for result, err := range decodeRows[benchmarkMemberRow](rowvalue.Scan(rows)) {
				if err != nil {
					b.Fatal(err)
				}
				if result.ID != 7 {
					b.Fatalf("scanned ID = %d, want 7", result.ID)
				}
			}
		})
	})

	b.Run("partial_generated", func(b *testing.B) {
		b.ReportAllocs()
		benchmarkQueryRows(b, benchmarkNameQuery, benchmarkRowsPerQuery, func(rows *sql.Rows) {
			for result, err := range scanTypedRows[benchmarkMemberRow](rows) {
				if err != nil {
					b.Fatal(err)
				}
				if result.Name != "Ada Lovelace" {
					b.Fatalf("scanned name = %q", result.Name)
				}
			}
		})
	})

	b.Run("partial_dynamic", func(b *testing.B) {
		b.ReportAllocs()
		benchmarkQueryRows(b, benchmarkNameQuery, benchmarkRowsPerQuery, func(rows *sql.Rows) {
			for result, err := range decodeRows[benchmarkMemberName](rowvalue.Scan(rows)) {
				if err != nil {
					b.Fatal(err)
				}
				if result.Name != "Ada Lovelace" {
					b.Fatalf("scanned name = %q", result.Name)
				}
			}
		})
	})
}

// BenchmarkCollectAll isolates the slice-growth cost collectAll pays when it
// has no hint against what it pays when it can pre-size the slice from a
// LIMIT, using the 10,000-row query so the growth pattern actually shows up.
func BenchmarkCollectAll(b *testing.B) {
	database, err := sql.Open(benchmarkDriverName, "")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Error(err)
		}
	})

	run := func(b *testing.B, hint int) {
		b.Helper()
		b.ReportAllocs()
		for range b.N {
			rows, err := database.QueryContext(b.Context(), benchmarkLargeQuery)
			if err != nil {
				b.Fatal(err)
			}
			decoded, err := collectAll(scanTypedRowsStatic[benchmarkMemberRow](rows), hint)
			if err != nil {
				b.Fatal(err)
			}
			if len(decoded) != benchmarkLargeRows {
				b.Fatalf("got %d rows, want %d", len(decoded), benchmarkLargeRows)
			}
		}
	}

	b.Run("no_hint", func(b *testing.B) {
		run(b, 0)
	})
	b.Run("exact_hint", func(b *testing.B) {
		run(b, benchmarkLargeRows)
	})
	// capped_hint uses an absurd hint far beyond any real LIMIT to prove the
	// byte-budget clamp holds: this must not attempt a hundred-million-element
	// allocation.
	b.Run("capped_hint", func(b *testing.B) {
		run(b, 100_000_000)
	})
}

// benchmarkCountTable is the table BenchmarkTypedSelectCount runs Count
// against. Its shape does not matter to the benchmark; only the COUNT(*)
// statement Count builds ever reaches the fake driver.
type benchmarkCountRow struct {
	ID int64 `rasql:"id"`
}

// BenchmarkTypedSelectCount isolates TypedSelectBuilder.Count's own cost: the
// route from a rendered COUNT(*) statement to the returned int64, with the
// query itself served by a fake driver that returns instantly. Comparing this
// benchmark's numbers before and after the typed_select.go rebuild in step 4
// of the ORM/helper split is the one behavior change that rebuild makes: Count
// moves off the reflective dynamic.Get[int64] path onto a countRow scanned
// directly by database/sql.
func BenchmarkTypedSelectCount(b *testing.B) {
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
	table, err := TableOf[benchmarkCountRow](schema.TableDef{
		Name: "members",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	})
	if err != nil {
		b.Fatal(err)
	}

	b.ResetTimer()
	b.ReportAllocs()
	for range b.N {
		count, err := SelectFrom(table).Count(b.Context(), db)
		if err != nil {
			b.Fatal(err)
		}
		if count != 3 {
			b.Fatalf("got count %d, want 3", count)
		}
	}
}

func benchmarkQueryRows(b *testing.B, query string, rowsPerQuery int, consume func(*sql.Rows)) {
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

	b.ResetTimer()
	for range b.N {
		rows, err := database.QueryContext(b.Context(), query)
		if err != nil {
			b.Fatal(err)
		}
		consume(rows)
	}
	b.StopTimer()
	b.ReportMetric(float64(b.Elapsed())/float64(b.N*rowsPerQuery), "ns/row")
}

type benchmarkDriver struct{}

func (benchmarkDriver) Open(string) (driver.Conn, error) {
	return benchmarkConn{}, nil
}

type benchmarkConn struct{}

func (benchmarkConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}

func (benchmarkConn) Close() error {
	return nil
}

func (benchmarkConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (benchmarkConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if strings.Contains(query, "COUNT(*)") {
		return &benchmarkResultRows{
			columns:   []string{"count"},
			values:    []driver.Value{int64(3)},
			remaining: 1,
		}, nil
	}
	switch query {
	case benchmarkFullQuery:
		return &benchmarkResultRows{
			columns:   []string{"id", "name", "email"},
			values:    []driver.Value{int64(7), "Ada Lovelace", "ada@example.com"},
			remaining: benchmarkRowsPerQuery,
		}, nil
	case benchmarkNameQuery:
		return &benchmarkResultRows{
			columns:   []string{"name"},
			values:    []driver.Value{"Ada Lovelace"},
			remaining: benchmarkRowsPerQuery,
		}, nil
	case benchmarkLargeQuery:
		return &benchmarkResultRows{
			columns:   []string{"id", "name", "email"},
			values:    []driver.Value{int64(7), "Ada Lovelace", "ada@example.com"},
			remaining: benchmarkLargeRows,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported query %q", query)
	}
}

type benchmarkResultRows struct {
	columns   []string
	values    []driver.Value
	remaining int
}

func (r *benchmarkResultRows) Columns() []string {
	return r.columns
}

func (*benchmarkResultRows) Close() error {
	return nil
}

func (r *benchmarkResultRows) Next(destinations []driver.Value) error {
	if r.remaining == 0 {
		return io.EOF
	}
	copy(destinations, r.values)
	r.remaining--
	return nil
}
