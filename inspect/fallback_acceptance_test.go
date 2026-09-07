package inspect_test

import (
	"context"
	"database/sql"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSQLiteObjectNamesFallsBackAfterEmptyModernPragma(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	}()
	ins, err := inspect.New(db, dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery("PRAGMA table_list").WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}))
	mock.ExpectQuery("PRAGMA database_list").WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "file"}).AddRow(0, "main", ""))
	mock.ExpectQuery(`SELECT name, type, sql FROM "main".sqlite_master WHERE type IN ('table', 'view')`).WillReturnRows(
		sqlmock.NewRows([]string{"name", "type", "sql"}).AddRow("events", "table", "CREATE TABLE events (id INTEGER)"),
	)
	objects, err := ins.ObjectNames(t.Context())
	require.NoError(t, err)
	require.Equal(t, []inspect.ObjectName{{Schema: "main", Name: "events", Kind: schema.ObjectTable}}, objects)
}

func TestSQLiteObjectNamesPreservesWrappedCancellationWithoutFallback(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"canceled", fmt.Errorf("driver: %w", context.Canceled)},
		{"deadline", fmt.Errorf("driver: %w", context.DeadlineExceeded)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			mock.ExpectQuery("PRAGMA table_list").WillReturnError(tc.err)
			mock.ExpectClose()
			require.ErrorIs(t, mustObjectNames(t, db), tc.err)
			require.NoError(t, db.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func mustObjectNames(t *testing.T, db interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}) error {
	t.Helper()
	ins, err := inspect.New(db, dialect.SQLite())
	if err != nil {
		return err
	}
	_, err = ins.ObjectNames(t.Context())
	return err
}

func TestSQLiteObjectNamesFallsBackInRequestedNamespace(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	defer func() {
		mock.ExpectClose()
		require.NoError(t, db.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	}()
	ins, err := inspect.New(db, dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery(`PRAGMA "aux".table_list`).WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}))
	mock.ExpectQuery(`SELECT name, type, sql FROM "aux".sqlite_master WHERE type IN ('table', 'view')`).WillReturnRows(
		sqlmock.NewRows([]string{"name", "type", "sql"}).AddRow("archive", "table", "CREATE TABLE archive (id INTEGER)"),
	)
	objects, err := ins.ObjectNamesIn(t.Context(), "aux")
	require.NoError(t, err)
	require.Equal(t, []inspect.ObjectName{{Schema: "aux", Name: "archive", Kind: schema.ObjectTable}}, objects)
}

func TestSQLiteObjectNamesAcceptsEmptyModernAndLegacyCatalogs(t *testing.T) {
	for _, tc := range []struct {
		name  string
		query string
	}{
		{"modern", "PRAGMA table_list"},
		{"legacy", "PRAGMA table_list"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			ins, err := inspect.New(db, dialect.SQLite())
			require.NoError(t, err)
			if tc.name == "modern" {
				mock.ExpectQuery(tc.query).WillReturnRows(sqlmock.NewRows([]string{"schema", "name", "type", "ncol", "wr", "strict"}))
			} else {
				mock.ExpectQuery(tc.query).WillReturnError(fmt.Errorf("unsupported pragma"))
			}
			mock.ExpectQuery("PRAGMA database_list").WillReturnRows(sqlmock.NewRows([]string{"seq", "name", "file"}).AddRow(0, "main", "").AddRow(1, "aux", ""))
			for _, database := range []string{"main", "aux"} {
				mock.ExpectQuery(`SELECT name, type, sql FROM "` + database + `".sqlite_master WHERE type IN ('table', 'view')`).WillReturnRows(sqlmock.NewRows([]string{"name", "type", "sql"}))
			}
			objects, err := ins.ObjectNames(t.Context())
			require.NoError(t, err)
			require.Empty(t, objects)
			mock.ExpectClose()
			require.NoError(t, db.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
