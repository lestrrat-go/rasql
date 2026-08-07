package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"testing"

	"github.com/lestrrat-go/rasql/row"
)

const (
	benchmarkDriverName   = "rasql-typed-row-scan"
	benchmarkFullQuery    = "SELECT id, name, email"
	benchmarkNameQuery    = "SELECT name"
	benchmarkRowsPerQuery = 10
)

func init() {
	sql.Register(benchmarkDriverName, benchmarkDriver{})
}

type benchmarkMemberRow struct {
	ID    int64
	Name  string
	Email string
}

func (r *benchmarkMemberRow) DecodeRow(source row.Dynamic) error {
	if err := row.Assign(source, "id", &r.ID); err != nil {
		return err
	}
	if err := row.Assign(source, "name", &r.Name); err != nil {
		return err
	}
	return row.Assign(source, "email", &r.Email)
}

func (r *benchmarkMemberRow) ScanColumns() []string {
	return []string{"id", "name", "email"}
}

func (r *benchmarkMemberRow) ScanRow(source row.ScanSource) error {
	return source.Scan(&r.ID, &r.Name, &r.Email)
}

func (r *benchmarkMemberRow) ScanDestinations(columns []string) ([]any, error) {
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

func (r *benchmarkMemberName) DecodeRow(source row.Dynamic) error {
	return row.Assign(source, "name", &r.Name)
}

func BenchmarkTypedRowScan(b *testing.B) {
	b.Run("full_generated", func(b *testing.B) {
		b.ReportAllocs()
		benchmarkQueryRows(b, benchmarkFullQuery, func(rows *sql.Rows) {
			for result, err := range scanTypedRows[benchmarkMemberRow](rows) {
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
		benchmarkQueryRows(b, benchmarkFullQuery, func(rows *sql.Rows) {
			for result, err := range decodeRows[benchmarkMemberRow](row.Scan(rows)) {
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
		benchmarkQueryRows(b, benchmarkNameQuery, func(rows *sql.Rows) {
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
		benchmarkQueryRows(b, benchmarkNameQuery, func(rows *sql.Rows) {
			for result, err := range decodeRows[benchmarkMemberName](row.Scan(rows)) {
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

func benchmarkQueryRows(b *testing.B, query string, consume func(*sql.Rows)) {
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
	b.ReportMetric(float64(b.Elapsed())/float64(b.N*benchmarkRowsPerQuery), "ns/row")
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
