package catalogread_test

import (
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestAcceptanceCatalogRequiredMissingObjectClassifiesCauseAndIdentity(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT c\\.relname").WillReturnRows(sqlmock.NewRows([]string{"name", "kind"}))
	mock.ExpectRollback()
	result, err := catalogread.Read(t.Context(), db, transactionAcceptanceProfile(t), catalogread.Scope{
		Include: []schema.ObjectName{{Schema: "audit", Name: "missing"}},
	})
	require.ErrorIs(t, err, catalogread.ErrUnresolvedFact)
	require.ErrorIs(t, err, inspect.ErrTableNotFound)
	var missing *inspect.TableNotFoundError
	require.ErrorAs(t, err, &missing)
	require.Equal(t, "missing", missing.Table)
	require.Empty(t, result.Tables)
	require.Empty(t, result.Unresolved)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAcceptanceCatalogUnresolvedSentinelIsDistinctFromOperationalErrors(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT c\\.relname").WillReturnError(errors.New("connection lost"))
	mock.ExpectRollback()
	_, err = catalogread.Read(t.Context(), db, transactionAcceptanceProfile(t), catalogread.Scope{})
	require.Error(t, err)
	require.NotErrorIs(t, err, catalogread.ErrUnresolvedFact)
	require.NoError(t, mock.ExpectationsWereMet())
}
