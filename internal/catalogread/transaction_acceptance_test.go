package catalogread_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/stretchr/testify/require"
)

func transactionAcceptanceProfile(t *testing.T) engineprofile.Profile {
	t.Helper()
	p, err := engineprofile.Builtin("postgresql-17", engineprofile.Version{Known: true, Major: 17, Minor: 6})
	require.NoError(t, err)
	return p
}

func TestAcceptanceCatalogReadCommitsSuccessfulEmptyResult(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT c\\.relname").WillReturnRows(sqlmock.NewRows([]string{"name", "kind"}))
	mock.ExpectCommit()
	result, err := catalogread.Read(t.Context(), db, transactionAcceptanceProfile(t), catalogread.Scope{})
	require.NoError(t, err)
	require.Empty(t, result.Tables)
	require.Empty(t, result.Unresolved)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAcceptanceCatalogReadTransactionFailuresOwnNoPartialResult(t *testing.T) {
	cases := []struct {
		name          string
		beginError    error
		queryError    error
		commitError   error
		rollbackError error
	}{
		{name: "begin", beginError: errors.New("begin failed")},
		{name: "query", queryError: errors.New("query failed")},
		{name: "commit", commitError: errors.New("commit failed")},
		{name: "rollback", queryError: errors.New("query failed"), rollbackError: errors.New("rollback failed")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			require.NoError(t, err)
			t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
			if tc.beginError != nil {
				mock.ExpectBegin().WillReturnError(tc.beginError)
			} else {
				mock.ExpectBegin()
				if tc.queryError != nil {
					mock.ExpectQuery("SELECT c\\.relname").WillReturnError(tc.queryError)
					mock.ExpectRollback().WillReturnError(tc.rollbackError)
				} else {
					mock.ExpectQuery("SELECT c\\.relname").WillReturnRows(sqlmock.NewRows([]string{"name", "kind"}))
					mock.ExpectCommit().WillReturnError(tc.commitError)
				}
			}
			result, err := catalogread.Read(context.Background(), db, transactionAcceptanceProfile(t), catalogread.Scope{})
			require.Error(t, err)
			require.Empty(t, result.Tables)
			require.Empty(t, result.Unresolved)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}
