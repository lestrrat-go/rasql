package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestScanTypedRowsReportsAClosedColumnRead documents the branch that yields
// a "row: read result columns" error when rows.Columns() fails, rather than
// proving the defer relocation in scanTypedRows. database/sql's own Columns
// error path (sql: Rows are closed) requires rows to already be closed, so
// this cannot exercise a leak: the connection is already released by the
// time Columns fails. See the "no reachable resource leak" note in the PR
// body for why TestScanTypedRowsClosesRowsOnEveryPath, not this test, is
// what carries regression value for the defer's placement.
func TestScanTypedRowsReportsAClosedColumnRead(t *testing.T) {
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

// TestScanTypedRowsClosesRowsOnEveryPath is the regression test for the
// defer relocation: it drives scanTypedRows through each reachable exit and
// asserts the underlying driver.Rows is closed exactly once on every one of
// them.
func TestScanTypedRowsClosesRowsOnEveryPath(t *testing.T) {
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
