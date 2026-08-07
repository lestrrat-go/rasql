package row_test

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/row"
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

		decoded := make([]row.Dynamic, 0)
		for result, err := range row.Scan(rows) {
			require.NoError(t, err)
			decoded = append(decoded, result)
		}
		require.Len(t, decoded, 2)

		id, err := row.Int64("id")
		require.NoError(t, err)
		email, err := row.String("email")
		require.NoError(t, err)

		gotID, err := id.Get(decoded[0])
		require.NoError(t, err)
		gotEmail, err := email.Get(decoded[0])
		require.NoError(t, err)
		require.Equal(t, int64(1), gotID)
		require.Equal(t, "ada@example.com", gotEmail)

		gotID, err = id.Get(decoded[1])
		require.NoError(t, err)
		gotEmail, err = email.Get(decoded[1])
		require.NoError(t, err)
		require.Equal(t, int64(2), gotID)
		require.Equal(t, "bob@example.com", gotEmail)
	})

	t.Run("nil rows yields nothing and does not panic", func(t *testing.T) {
		count := 0
		require.NotPanics(t, func() {
			for range row.Scan(nil) {
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
		for _, err := range row.Scan(rows) {
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
		for _, err := range row.Scan(rows) {
			require.ErrorContains(t, err, "row: iterate result rows")
			count++
		}
		require.Equal(t, 1, count)
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
		for _, err := range row.Scan(rows) {
			require.ErrorContains(t, err, "row: create result row")
			count++
		}
		require.Equal(t, 1, count)
	})
}
