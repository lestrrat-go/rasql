package catalogread_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

const enumerateObjects = `SELECT c\.relname, CASE WHEN c\.relkind.*FROM pg_catalog\.pg_class`
const inspectVersion = `SHOW server_version_num`

func TestAcceptanceCatalogSweepRecordsSortedUnresolvedFacts(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	mock.ExpectBegin()
	mock.ExpectQuery(enumerateObjects).WillReturnRows(sqlmock.NewRows([]string{"relname", "kind"}).AddRow("z_missing", "table").AddRow("a_partial", "table"))
	mock.ExpectQuery(inspectVersion).WillReturnError(fmt.Errorf("missing fixture: %w", inspect.ErrTableNotFound))
	mock.ExpectQuery(inspectVersion).WillReturnError(fmt.Errorf("partial fixture: %w", inspect.ErrIncompleteMetadata))
	mock.ExpectCommit()
	result, err := catalogread.Read(t.Context(), db, transactionAcceptanceProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	require.Empty(t, result.Tables)
	require.Equal(t, transactionAcceptanceProfile(t), result.Observed)
	require.Equal(t, []catalogread.UnresolvedFact{
		{Object: schema.ObjectName{Name: "a_partial"}, Path: "columns", Code: "columns_incomplete", Detail: "inspect: read PostgreSQL server version: partial fixture: inspect: incomplete table metadata"},
		{Object: schema.ObjectName{Name: "z_missing"}, Path: "$", Code: "object_missing", Detail: "inspect: read PostgreSQL server version: missing fixture: inspect: table not found"},
	}, result.Unresolved)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAcceptanceCatalogExactEnumeratedRequestReturnsUnresolvedCause(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sentinel error
	}{
		{"missing", inspect.ErrTableNotFound},
		{"incomplete", inspect.ErrIncompleteMetadata},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
			mock.ExpectBegin()
			mock.ExpectQuery(enumerateObjects).WillReturnRows(sqlmock.NewRows([]string{"relname", "kind"}).AddRow("wanted", "table"))
			mock.ExpectQuery(inspectVersion).WillReturnError(fmt.Errorf("wrapped fixture: %w", tc.sentinel))
			mock.ExpectRollback()
			result, err := catalogread.Read(t.Context(), db, transactionAcceptanceProfile(t), catalogread.Scope{Include: []schema.ObjectName{{Name: "wanted"}}})
			require.ErrorIs(t, err, catalogread.ErrUnresolvedFact)
			require.ErrorIs(t, err, tc.sentinel)
			require.Empty(t, result.Tables)
			require.Empty(t, result.Unresolved)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAcceptanceCatalogOperationalEnumerationFailuresStayOperational(t *testing.T) {
	for _, tc := range []struct {
		name string
		rows *sqlmock.Rows
	}{
		{"scan", sqlmock.NewRows([]string{"relname"}).AddRow("broken")},
		{"row error", sqlmock.NewRows([]string{"relname", "kind"}).AddRow("first", "table").AddRow("second", "table").RowError(1, errors.New("enumeration row failed"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
			mock.ExpectBegin()
			mock.ExpectQuery(enumerateObjects).WillReturnRows(tc.rows)
			mock.ExpectRollback()
			result, err := catalogread.Read(t.Context(), db, transactionAcceptanceProfile(t), catalogread.Scope{})
			require.Error(t, err)
			require.NotErrorIs(t, err, catalogread.ErrUnresolvedFact)
			if tc.name == "row error" {
				require.ErrorContains(t, err, "enumeration row failed")
			}
			require.Empty(t, result.Tables)
			require.Empty(t, result.Unresolved)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAcceptanceCatalogEnumerationCloseFailureStaysOperational(t *testing.T) {
	closeErr := errors.New("enumeration close failed")
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	mock.ExpectBegin()
	mock.ExpectQuery(enumerateObjects).WillReturnRows(sqlmock.NewRows([]string{"relname", "kind"}).CloseError(closeErr))
	mock.ExpectRollback()
	result, err := catalogread.Read(t.Context(), db, transactionAcceptanceProfile(t), catalogread.Scope{})
	require.ErrorIs(t, err, closeErr)
	require.NotErrorIs(t, err, catalogread.ErrUnresolvedFact)
	require.Empty(t, result.Tables)
	require.Empty(t, result.Unresolved)
	require.NoError(t, mock.ExpectationsWereMet())
}
