package rowvalue_test

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/internal/rowvalue"
	"github.com/stretchr/testify/require"
)

func TestScan(t *testing.T) {
	t.Run("decodes every row", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		mock.ExpectQuery("SELECT").WillReturnRows(
			sqlmock.NewRows([]string{"id", "email"}).
				AddRow(int64(1), "ada@example.com").
				AddRow(int64(2), "bob@example.com"),
		)
		rows, err := database.QueryContext(t.Context(), "SELECT")
		require.NoError(t, err)

		decoded := make([]rowvalue.Row, 0)
		for result, err := range rowvalue.Scan(rows) {
			require.NoError(t, err)
			decoded = append(decoded, result)
		}
		require.Len(t, decoded, 2)

		gotID, err := rowvalue.Get[int64](decoded[0], "id")
		require.NoError(t, err)
		gotEmail, err := rowvalue.Get[string](decoded[0], "email")
		require.NoError(t, err)
		require.Equal(t, int64(1), gotID)
		require.Equal(t, "ada@example.com", gotEmail)

		gotID, err = rowvalue.Get[int64](decoded[1], "id")
		require.NoError(t, err)
		gotEmail, err = rowvalue.Get[string](decoded[1], "email")
		require.NoError(t, err)
		require.Equal(t, int64(2), gotID)
		require.Equal(t, "bob@example.com", gotEmail)
	})

	t.Run("nil rows yields nothing and does not panic", func(t *testing.T) {
		count := 0
		require.NotPanics(t, func() {
			for range rowvalue.Scan(nil) {
				count++
			}
		})
		require.Zero(t, count)
	})

	t.Run("breaking early closes the rows", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		mock.ExpectQuery("SELECT").
			WillReturnRows(
				sqlmock.NewRows([]string{"id"}).
					AddRow(int64(1)).
					AddRow(int64(2)),
			).
			RowsWillBeClosed()
		rows, err := database.QueryContext(t.Context(), "SELECT")
		require.NoError(t, err)

		count := 0
		for _, err := range rowvalue.Scan(rows) {
			require.NoError(t, err)
			count++
			break
		}
		require.Equal(t, 1, count)
	})

	t.Run("a row iteration error is reported once", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		mock.ExpectQuery("SELECT").WillReturnRows(
			sqlmock.NewRows([]string{"id"}).
				AddRow(int64(1)).
				RowError(0, errors.New("row failure")),
		)
		rows, err := database.QueryContext(t.Context(), "SELECT")
		require.NoError(t, err)

		count := 0
		for _, err := range rowvalue.Scan(rows) {
			require.ErrorContains(t, err, "row: iterate result rows")
			count++
		}
		require.Equal(t, 1, count)
	})

	// TestScan hoists its values and destinations buffers above the row loop
	// and reuses them for every row rather than allocating fresh ones, on the
	// argument that rows.Scan rebinds each *any destination to a new
	// interface value rather than mutating the object the previous value
	// pointed at, and that the per-row Dynamic copies those values into its
	// own slice before rows.Next runs again. Collecting every Dynamic before
	// reading any of them back is what makes this a regression guard: without
	// the hoisting, this would pass even if a later row's Dynamic aliased an
	// earlier row's buffer.
	t.Run("buffer reuse does not alias across rows", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		mock.ExpectQuery("SELECT").WillReturnRows(
			sqlmock.NewRows([]string{"id", "payload"}).
				AddRow(int64(1), []byte("first")).
				AddRow(int64(2), []byte("second")).
				AddRow(int64(3), []byte("third")),
		)
		rows, err := database.QueryContext(t.Context(), "SELECT")
		require.NoError(t, err)

		decoded := make([]rowvalue.Row, 0, 3)
		for result, err := range rowvalue.Scan(rows) {
			require.NoError(t, err)
			decoded = append(decoded, result)
		}
		require.Len(t, decoded, 3)

		wantIDs := []int64{1, 2, 3}
		wantPayloads := [][]byte{[]byte("first"), []byte("second"), []byte("third")}
		for i, result := range decoded {
			gotID, err := rowvalue.Get[int64](result, "id")
			require.NoError(t, err)
			require.Equal(t, wantIDs[i], gotID)

			gotPayload, err := rowvalue.Get[[]byte](result, "payload")
			require.NoError(t, err)
			require.Equal(t, wantPayloads[i], gotPayload)
		}
	})

	t.Run("duplicate column names fail to create a row", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
		mock.ExpectQuery("SELECT").WillReturnRows(
			sqlmock.NewRows([]string{"id", "id"}).
				AddRow(int64(1), int64(1)),
		)
		rows, err := database.QueryContext(t.Context(), "SELECT")
		require.NoError(t, err)

		count := 0
		for _, err := range rowvalue.Scan(rows) {
			require.ErrorContains(t, err, "row: create result row")
			count++
		}
		require.Equal(t, 1, count)
	})
}
