package row_test

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

// Every name and constant here is unique to this file, so this fake driver
// can never collide with the one typed_rows_benchmark_test.go registers in
// the root package.
const (
	decodeBenchDriverName = "rasql-row-decode-plan-cache"
	decodeBenchQuery      = "SELECT id, name, email"
)

func init() {
	sql.Register(decodeBenchDriverName, decodeBenchDriver{})
}

// decodeBenchFieldedRow has three fields and no methods, so it takes Decode's
// field-mapping path -- the path the per-type plan cache changes the most.
type decodeBenchFieldedRow struct {
	ID    int64
	Name  string
	Email string
}

// decodeBenchSelfDecodedRow maps itself through DecodeRow, so it takes
// Decode's mapsItself path.
type decodeBenchSelfDecodedRow struct {
	ID    int64
	Name  string
	Email string
}

func (r *decodeBenchSelfDecodedRow) DecodeRow(source row.Dynamic) error {
	if err := row.Assign(source, "id", &r.ID); err != nil {
		return err
	}
	if err := row.Assign(source, "name", &r.Name); err != nil {
		return err
	}
	return row.Assign(source, "email", &r.Email)
}

func BenchmarkRowDecode(b *testing.B) {
	for _, rowCount := range []int{10, 1000} {
		b.Run(fmt.Sprintf("field_mapped/%d_rows", rowCount), func(b *testing.B) {
			b.ReportAllocs()
			decodeBenchRun(b, rowCount, func(rows *sql.Rows) {
				for source, err := range row.Scan(rows) {
					if err != nil {
						b.Fatal(err)
					}
					result, err := row.Decode[decodeBenchFieldedRow](source)
					if err != nil {
						b.Fatal(err)
					}
					if result.ID != 7 {
						b.Fatalf("decoded ID = %d, want 7", result.ID)
					}
				}
			})
		})

		b.Run(fmt.Sprintf("self_decoded/%d_rows", rowCount), func(b *testing.B) {
			b.ReportAllocs()
			decodeBenchRun(b, rowCount, func(rows *sql.Rows) {
				for source, err := range row.Scan(rows) {
					if err != nil {
						b.Fatal(err)
					}
					result, err := row.Decode[decodeBenchSelfDecodedRow](source)
					if err != nil {
						b.Fatal(err)
					}
					if result.ID != 7 {
						b.Fatalf("decoded ID = %d, want 7", result.ID)
					}
				}
			})
		})
	}
}

func decodeBenchRun(b *testing.B, rowCount int, consume func(*sql.Rows)) {
	b.Helper()
	database, err := sql.Open(decodeBenchDriverName, "")
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if err := database.Close(); err != nil {
			b.Error(err)
		}
	})
	decodeBenchRowsPerQuery = rowCount

	b.ResetTimer()
	for range b.N {
		rows, err := database.QueryContext(b.Context(), decodeBenchQuery)
		if err != nil {
			b.Fatal(err)
		}
		consume(rows)
	}
	b.StopTimer()
	b.ReportMetric(float64(b.Elapsed())/float64(b.N*rowCount), "ns/row")
}

// decodeBenchRowsPerQuery is read by decodeBenchConn.QueryContext to decide
// how many rows the fake driver hands back. decodeBenchRun sets it before
// every sub-benchmark runs, and no sub-benchmark runs concurrently with
// another, so this package-level variable never races.
var decodeBenchRowsPerQuery int

type decodeBenchDriver struct{}

func (decodeBenchDriver) Open(string) (driver.Conn, error) {
	return decodeBenchConn{}, nil
}

type decodeBenchConn struct{}

func (decodeBenchConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}

func (decodeBenchConn) Close() error {
	return nil
}

func (decodeBenchConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (decodeBenchConn) QueryContext(_ context.Context, query string, _ []driver.NamedValue) (driver.Rows, error) {
	if query != decodeBenchQuery {
		return nil, fmt.Errorf("unsupported query %q", query)
	}
	return &decodeBenchResultRows{
		columns:   []string{"id", "name", "email"},
		values:    []driver.Value{int64(7), "Ada Lovelace", "ada@example.com"},
		remaining: decodeBenchRowsPerQuery,
	}, nil
}

type decodeBenchResultRows struct {
	columns   []string
	values    []driver.Value
	remaining int
}

func (r *decodeBenchResultRows) Columns() []string {
	return r.columns
}

func (*decodeBenchResultRows) Close() error {
	return nil
}

func (r *decodeBenchResultRows) Next(destinations []driver.Value) error {
	if r.remaining == 0 {
		return io.EOF
	}
	copy(destinations, r.values)
	r.remaining--
	return nil
}
