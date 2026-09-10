package catalogread_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/stretchr/testify/require"
)

func TestAcceptanceCatalogReadScanFailureRollsBackAndPreservesCause(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { mock.ExpectClose(); require.NoError(t, db.Close()) })
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT c\\.relname").WillReturnRows(sqlmock.NewRows([]string{"name"}).AddRow("broken"))
	mock.ExpectRollback()
	result, err := catalogread.Read(t.Context(), db, transactionAcceptanceProfile(t), catalogread.Scope{})
	require.Error(t, err)
	require.NotErrorIs(t, err, catalogread.ErrUnresolvedFact)
	require.Empty(t, result.Tables)
	require.Empty(t, result.Unresolved)
	require.NoError(t, mock.ExpectationsWereMet())
}
