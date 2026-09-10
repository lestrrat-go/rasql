package querydescribe_test

import (
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/stretchr/testify/require"
)

func TestDeclaredAdapterReturnsExplicitAvailabilityMarker(t *testing.T) {
	db, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	mock.ExpectPrepare("SELECT 1").WillBeClosed()
	description, err := querydescribe.NewSQLitePrepare(db).Describe(t.Context(), compilerquery.DescribeRequest{DB: db, SQL: "SELECT 1"})
	require.NoError(t, err)
	require.True(t, description.DeclaredOnly)
	require.Nil(t, description.Parameters)
	require.Nil(t, description.Results)
	require.NoError(t, mock.ExpectationsWereMet())
}
