package querydescribe_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func newSQLiteMock(t *testing.T) (querydescribe.Queryer, sqlmock.Sqlmock, func()) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	return db, mock, func() { require.NoError(t, mock.ExpectationsWereMet()) }
}

func wrapperSQL(sqlText string) string {
	return "SELECT * FROM (" + sqlText + ") AS rasql_description LIMIT 0"
}

func TestSQLiteMetadataRequestAndQueryErrors(t *testing.T) {
	_, err := querydescribe.NewSQLite(nil).Describe(t.Context(), querydescribe.Request{Name: "nil_queryer", SQL: "SELECT 1"})
	require.ErrorIs(t, err, querydescribe.ErrInvalidRequest)
	require.Contains(t, err.Error(), "nil_queryer")
	require.Contains(t, err.Error(), "nil queryer")

	queryer, mock, verify := newSQLiteMock(t)
	queryErr := errors.New("query failed")
	mock.ExpectQuery(wrapperSQL("SELECT broken AS value")).WillReturnError(queryErr)
	_, err = querydescribe.NewSQLite(queryer).Describe(t.Context(), querydescribe.Request{Name: "broken", SQL: "SELECT broken AS value"})
	require.ErrorIs(t, err, queryErr)
	require.Contains(t, err.Error(), "broken")
	require.Contains(t, err.Error(), "query failed")
	verify()
}

func TestSQLiteMetadataIncompleteShapesAndRowsClose(t *testing.T) {
	tests := []struct {
		name string
		sql  string
		rows *sqlmock.Rows
		want []string
	}{
		{
			name: "zero columns",
			sql:  "SELECT 1 WHERE 0",
			rows: sqlmock.NewRows([]string{}),
			want: []string{"empty", "returned no columns"},
		},
		{
			name: "empty database type",
			sql:  "SELECT dynamic_expression() AS value",
			rows: sqlmock.NewRowsWithColumnDefinition(sqlmock.NewColumn("value").Nullable(false)),
			want: []string{"dynamic", "column 0", "value", "has no database type"},
		},
		{
			name: "unknown nullability",
			sql:  "SELECT value FROM source",
			rows: sqlmock.NewRowsWithColumnDefinition(sqlmock.NewColumn("value").OfType("INTEGER", int64(0))),
			want: []string{"nullable", "column 0", "value", "has no nullability metadata"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			queryer, mock, verify := newSQLiteMock(t)
			mock.ExpectQuery(wrapperSQL(test.sql)).WillReturnRows(test.rows).RowsWillBeClosed()
			_, err := querydescribe.NewSQLite(queryer).Describe(t.Context(), querydescribe.Request{Name: test.want[0], SQL: test.sql})
			require.ErrorIs(t, err, querydescribe.ErrIncomplete)
			for _, word := range test.want {
				require.Contains(t, err.Error(), word)
			}
			verify()
		})
	}
}

func TestSQLiteMetadataInvalidAndDuplicateNames(t *testing.T) {
	for i, name := range []string{"", "1value", "value-name"} {
		t.Run(fmt.Sprintf("invalid_%d", i), func(t *testing.T) {
			queryer, mock, verify := newSQLiteMock(t)
			rows := sqlmock.NewRowsWithColumnDefinition(sqlmock.NewColumn(name).OfType("INTEGER", int64(0)).Nullable(false))
			mock.ExpectQuery(wrapperSQL("SELECT value")).WillReturnRows(rows).RowsWillBeClosed()
			_, err := querydescribe.NewSQLite(queryer).Describe(t.Context(), querydescribe.Request{Name: "invalid_alias", SQL: "SELECT value"})
			require.ErrorIs(t, err, querydescribe.ErrIncomplete)
			require.Contains(t, err.Error(), "invalid_alias")
			require.Contains(t, err.Error(), "column 0")
			require.Contains(t, err.Error(), "invalid name")
			verify()
		})
	}

	queryer, mock, verify := newSQLiteMock(t)
	rows := sqlmock.NewRowsWithColumnDefinition(
		sqlmock.NewColumn("value").OfType("INTEGER", int64(0)).Nullable(false),
		sqlmock.NewColumn("value").OfType("INTEGER", int64(0)).Nullable(false),
	)
	mock.ExpectQuery(wrapperSQL("SELECT first, second")).WillReturnRows(rows).RowsWillBeClosed()
	_, err := querydescribe.NewSQLite(queryer).Describe(t.Context(), querydescribe.Request{Name: "duplicate", SQL: "SELECT first, second"})
	require.ErrorIs(t, err, querydescribe.ErrIncomplete)
	require.Contains(t, err.Error(), "duplicate column \"value\"")
	verify()
}

func TestSQLiteMetadataSuccessfulDescriptionClosesRows(t *testing.T) {
	queryer, mock, verify := newSQLiteMock(t)
	rows := sqlmock.NewRowsWithColumnDefinition(sqlmock.NewColumn("value").OfType("INTEGER", int64(0)).Nullable(false))
	mock.ExpectQuery(wrapperSQL("SELECT value")).WillReturnRows(rows).RowsWillBeClosed()
	got, err := querydescribe.NewSQLite(queryer).Describe(t.Context(), querydescribe.Request{Name: "success", SQL: "SELECT value"})
	require.NoError(t, err)
	require.Equal(t, querydescribe.Description{
		Columns: []querydescribe.Column{{Name: "value", Binding: schema.GoBinding{Type: "int64"}, Nullable: false}},
	}, got)
	verify()
}

func TestSQLiteMetadataRepeatedParametersRemainPositional(t *testing.T) {
	queryer, mock, verify := newSQLiteMock(t)
	sqlText := "SELECT ? AS first_value, ? AS second_value"
	rows := sqlmock.NewRowsWithColumnDefinition(
		sqlmock.NewColumn("first_value").OfType("INTEGER", int64(0)).Nullable(false),
		sqlmock.NewColumn("second_value").OfType("INTEGER", int64(0)).Nullable(false),
	)
	mock.ExpectQuery(wrapperSQL(sqlText)).WithArgs(nil, nil).WillReturnRows(rows).RowsWillBeClosed()
	got, err := querydescribe.NewSQLite(queryer).Describe(t.Context(), querydescribe.Request{
		Name: "repeated", SQL: sqlText, Parameters: []string{"same", "same"},
	})
	require.NoError(t, err)
	require.Equal(t, []string{"first_value", "second_value"}, []string{got.Columns[0].Name, got.Columns[1].Name})
	verify()
}
