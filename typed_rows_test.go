package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"math"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestTypedRows(t *testing.T) {
	t.Run("reports a closed column read", testScanTypedRowsReportsAClosedColumnRead)
	t.Run("closes rows on every path", testScanTypedRowsClosesRowsOnEveryPath)
	t.Run("keeps dynamic scanner state independent", testScanTypedRowsKeepsDynamicScannerStateIndependent)
	t.Run("keeps static scanner state independent", testScanTypedRowsKeepsStaticScannerStateIndependent)
	t.Run("validates dynamic mapping for empty results", testScanTypedRowsValidatesEmptyResultMapping)
	t.Run("preallocates within the byte budget", testPreallocCapacity)
}

func testScanTypedRowsKeepsDynamicScannerStateIndependent(t *testing.T) {
	var closes int
	rows := openCloseCountingRows(t, "rasql-typed-rows-dynamic-ownership", &closeCountingDriver{
		columns: []string{"payload"},
		rows:    [][]driver.Value{{"abc"}, {"def"}},
		closes:  &closes,
	})

	collected := make([]dynamicScannerRow, 0, 2)
	for result, err := range scanTypedRows[dynamicScannerRow](rows) {
		require.NoError(t, err)
		collected = append(collected, result)
	}
	require.Len(t, collected, 2)
	require.Equal(t, "abc", string(collected[0].Payload.bytes))
	require.Equal(t, "def", string(collected[1].Payload.bytes))

	collected[0].Payload.bytes[0] = 'x'
	require.Equal(t, "def", string(collected[1].Payload.bytes))
}

func testScanTypedRowsKeepsStaticScannerStateIndependent(t *testing.T) {
	var closes int
	rows := openCloseCountingRows(t, "rasql-typed-rows-static-ownership", &closeCountingDriver{
		columns: []string{"payload"},
		rows:    [][]driver.Value{{"abc"}, {"def"}},
		closes:  &closes,
	})

	collected := make([]staticScannerRow, 0, 2)
	for result, err := range scanTypedRowsStatic[staticScannerRow](rows) {
		require.NoError(t, err)
		collected = append(collected, result)
	}
	require.Len(t, collected, 2)
	require.Equal(t, "abc", string(collected[0].Payload.bytes))
	require.Equal(t, "def", string(collected[1].Payload.bytes))

	collected[0].Payload.bytes[0] = 'x'
	require.Equal(t, "def", string(collected[1].Payload.bytes))
}

func testScanTypedRowsValidatesEmptyResultMapping(t *testing.T) {
	closes := 0
	rows := openCloseCountingRows(t, "rasql-typed-rows-empty-mapping", &closeCountingDriver{
		columns: []string{"payload"},
		closes:  &closes,
	})

	yielded := 0
	for _, err := range scanTypedRows[closeCountingErrRow](rows) {
		yielded++
		require.ErrorIs(t, err, errScanDestinations)
		require.ErrorContains(t, err, "rasql: configure result scan")
	}
	require.Equal(t, 1, yielded)
	require.Equal(t, 1, closes)
}

// testScanTypedRowsReportsAClosedColumnRead documents the branch that yields
// a "row: read result columns" error when rows.Columns() fails, rather than
// proving the defer relocation in scanTypedRows. database/sql's own Columns
// error path (sql: Rows are closed) requires rows to already be closed, so
// this cannot exercise a leak: the connection is already released by the
// time Columns fails. See the "no reachable resource leak" note in the PR
// body for why TestScanTypedRowsClosesRowsOnEveryPath, not this test, is
// what carries regression value for the defer's placement.
func testScanTypedRowsReportsAClosedColumnRead(t *testing.T) {
	driverName := "rasql-typed-rows-closed-column-read"
	sql.Register(driverName, &closeCountingDriver{
		columns: []string{"id"},
		rows:    [][]driver.Value{{int64(1)}},
		closes:  new(int),
	})
	database, err := sql.Open(driverName, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	rows, err := database.QueryContext(t.Context(), "SELECT id")
	require.NoError(t, err)
	require.NoError(t, rows.Close())

	yielded := 0
	for result, err := range scanTypedRows[closeCountingRow](rows) {
		yielded++
		require.Error(t, err)
		require.ErrorContains(t, err, "sql: Rows are closed")
		require.ErrorContains(t, err, "row: read result columns")
		require.Zero(t, result)
	}
	require.Equal(t, 1, yielded)
}

// testScanTypedRowsClosesRowsOnEveryPath is the regression test for the
// defer relocation: it drives scanTypedRows through each reachable exit and
// asserts the underlying driver.Rows is closed exactly once on every one of
// them.
func testScanTypedRowsClosesRowsOnEveryPath(t *testing.T) {
	t.Run("normal exhaustion", func(t *testing.T) {
		closes := 0
		rows := openCloseCountingRows(t, "rasql-typed-rows-close-normal", &closeCountingDriver{
			columns: []string{"id"},
			rows:    [][]driver.Value{{int64(1)}, {int64(2)}},
			closes:  &closes,
		})

		count := 0
		for result, err := range scanTypedRows[closeCountingRow](rows) {
			require.NoError(t, err)
			require.NotZero(t, result.ID)
			count++
		}
		require.Equal(t, 2, count)
		require.Equal(t, 1, closes)
	})

	t.Run("early break", func(t *testing.T) {
		closes := 0
		rows := openCloseCountingRows(t, "rasql-typed-rows-close-break", &closeCountingDriver{
			columns: []string{"id"},
			rows:    [][]driver.Value{{int64(1)}, {int64(2)}},
			closes:  &closes,
		})

		count := 0
		for result, err := range scanTypedRows[closeCountingRow](rows) {
			require.NoError(t, err)
			require.NotZero(t, result.ID)
			count++
			break
		}
		require.Equal(t, 1, count)
		require.Equal(t, 1, closes)
	})

	t.Run("scan error from driver Next", func(t *testing.T) {
		closes := 0
		nextErr := errors.New("driver next failed")
		rows := openCloseCountingRows(t, "rasql-typed-rows-close-next-error", &closeCountingDriver{
			columns: []string{"id"},
			nextErr: nextErr,
			closes:  &closes,
		})

		yielded := 0
		for _, err := range scanTypedRows[closeCountingRow](rows) {
			require.ErrorIs(t, err, nextErr)
			yielded++
		}
		require.Equal(t, 1, yielded)
		require.Equal(t, 1, closes)
	})

	t.Run("ScanDestinations error", func(t *testing.T) {
		closes := 0
		rows := openCloseCountingRows(t, "rasql-typed-rows-close-scan-destinations-error", &closeCountingDriver{
			columns: []string{"id"},
			rows:    [][]driver.Value{{int64(1)}},
			closes:  &closes,
		})

		yielded := 0
		for _, err := range scanTypedRows[closeCountingErrRow](rows) {
			require.ErrorIs(t, err, errScanDestinations)
			yielded++
		}
		require.Equal(t, 1, yielded)
		require.Equal(t, 1, closes)
	})
}

// openCloseCountingRows registers driverImpl under name, opens a *sql.DB on
// it, runs a query, and returns the resulting rows. name must be unique per
// call: sql.Register panics on a duplicate name.
func openCloseCountingRows(t *testing.T, name string, driverImpl *closeCountingDriver) *sql.Rows {
	t.Helper()
	sql.Register(name, driverImpl)
	database, err := sql.Open(name, "")
	require.NoError(t, err)
	t.Cleanup(func() {
		require.NoError(t, database.Close())
	})

	rows, err := database.QueryContext(t.Context(), "SELECT id")
	require.NoError(t, err)
	return rows
}

// closeCountingRow is a row type that implements DestinationScanner so
// scanTypedRows takes the dynamic-column-mapping branch under test.
type closeCountingRow struct {
	ID int64
}

func (r *closeCountingRow) ScanDestinations(columns []string) ([]any, error) {
	destinations := make([]any, len(columns))
	for index := range destinations {
		destinations[index] = &r.ID
	}
	return destinations, nil
}

// errScanDestinations is returned by closeCountingErrRow.ScanDestinations on
// every call, to drive scanTypedRows's ScanDestinations-error exit.
var errScanDestinations = errors.New("scan destinations failed")

// closeCountingErrRow is a row type whose ScanDestinations always fails.
type closeCountingErrRow struct{}

func (*closeCountingErrRow) ScanDestinations([]string) ([]any, error) {
	return nil, errScanDestinations
}

type reusableScanner struct {
	bytes []byte
}

func (s *reusableScanner) Scan(value any) error {
	switch value := value.(type) {
	case nil:
		s.bytes = s.bytes[:0]
	case string:
		s.bytes = append(s.bytes[:0], value...)
	case []byte:
		s.bytes = append(s.bytes[:0], value...)
	default:
		return fmt.Errorf("unsupported scanner value %T", value)
	}
	return nil
}

type dynamicScannerRow struct {
	Payload reusableScanner
}

func (r *dynamicScannerRow) ScanDestinations(columns []string) ([]any, error) {
	if len(columns) != 1 || columns[0] != "payload" {
		return nil, fmt.Errorf("unexpected result columns %q", columns)
	}
	return []any{&r.Payload}, nil
}

type staticScannerRow struct {
	Payload reusableScanner
}

func (r *staticScannerRow) ScanRow(source ScanSource) error {
	return source.Scan(&r.Payload)
}

// closeCountingDriver is a test-only database/sql driver, distinct from
// benchmarkDriver in typed_rows_benchmark_test.go, whose driver.Rows counts
// its own Close calls so a test can assert scanTypedRows closes rows exactly
// once per exit path.
type closeCountingDriver struct {
	columns []string
	rows    [][]driver.Value
	nextErr error
	closes  *int
}

func (d *closeCountingDriver) Open(string) (driver.Conn, error) {
	return &closeCountingConn{driver: d}, nil
}

type closeCountingConn struct {
	driver *closeCountingDriver
}

func (*closeCountingConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("prepared statements are not supported")
}

func (*closeCountingConn) Close() error {
	return nil
}

func (*closeCountingConn) Begin() (driver.Tx, error) {
	return nil, errors.New("transactions are not supported")
}

func (c *closeCountingConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	return &closeCountingRows{
		columns: c.driver.columns,
		values:  c.driver.rows,
		nextErr: c.driver.nextErr,
		closes:  c.driver.closes,
	}, nil
}

type closeCountingRows struct {
	columns []string
	values  [][]driver.Value
	index   int
	nextErr error
	closes  *int
}

func (r *closeCountingRows) Columns() []string {
	return r.columns
}

func (r *closeCountingRows) Close() error {
	*r.closes++
	return nil
}

func (r *closeCountingRows) Next(destinations []driver.Value) error {
	if r.index >= len(r.values) {
		if r.nextErr != nil {
			return r.nextErr
		}
		return io.EOF
	}
	copy(destinations, r.values[r.index])
	r.index++
	return nil
}

// preallocNarrowRow is a few bytes wide, representative of a typical
// generated row type.
type preallocNarrowRow struct {
	ID int32
}

// preallocWideRow carries a multi-kilobyte array field so its element size
// alone approaches, and for a large hint exceeds, maxCollectPreallocBytes.
type preallocWideRow struct {
	Payload [4096]byte
}

// preallocEmptyRow has zero size, which exercises the size < 1 guard in
// preallocCapacity.
type preallocEmptyRow struct{}

// TestPreallocCapacity is a table-driven test over preallocCapacity, table-driven
// across both the hint and the row type so the byte-budget clamp is checked
// against a narrow type, a deliberately wide type, and a zero-sized type.
// math.MaxInt against preallocNarrowRow (4 bytes, but the size < 1 guard makes
// the smallest possible element 1 byte for this check) is the case a naive
// hint * size would overflow.
func testPreallocCapacity(t *testing.T) {
	hints := []int{-1, 0, 1, 8, math.MaxInt}

	t.Run("narrow row", func(t *testing.T) {
		size := int(reflect.TypeFor[preallocNarrowRow]().Size())
		for _, hint := range hints {
			got := preallocCapacity[preallocNarrowRow](hint)
			checkPreallocCapacity(t, hint, size, got)
		}
	})

	t.Run("wide row", func(t *testing.T) {
		size := int(reflect.TypeFor[preallocWideRow]().Size())
		for _, hint := range hints {
			got := preallocCapacity[preallocWideRow](hint)
			checkPreallocCapacity(t, hint, size, got)
		}
	})

	t.Run("empty row", func(t *testing.T) {
		size := int(reflect.TypeFor[preallocEmptyRow]().Size())
		for _, hint := range hints {
			got := preallocCapacity[preallocEmptyRow](hint)
			checkPreallocCapacity(t, hint, size, got)
		}
	})
}

// checkPreallocCapacity asserts the invariants preallocCapacity must hold for
// any hint and any element size: the result is never negative, never exceeds
// the hint, and never reserves more than maxCollectPreallocBytes.
func checkPreallocCapacity(t *testing.T, hint, size, got int) {
	t.Helper()

	require.GreaterOrEqual(t, got, 0, "hint=%d size=%d", hint, size)
	if hint <= 0 {
		require.Zero(t, got, "hint=%d size=%d", hint, size)
	} else {
		require.LessOrEqual(t, got, hint, "hint=%d size=%d", hint, size)
	}

	budgetSize := size
	if budgetSize < 1 {
		budgetSize = 1
	}
	require.LessOrEqual(t, got*budgetSize, maxCollectPreallocBytes, "hint=%d size=%d", hint, size)
}
