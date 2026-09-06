package rasql_test

import (
	"context"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestRootExecAndTypedInsertPreserveResultAfterHookError(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	hook := rasql.HookFunc{AfterFunc: func(context.Context, rasql.Operation, error) error {
		return errors.New("export failed")
	}}
	db, err := rasql.New(database, dialect.SQLite(), hook)
	require.NoError(t, err)
	table, err := query.NewTableRef(schema.TableDef{
		Name:    "users",
		Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}},
	})
	require.NoError(t, err)
	statement, err := query.NewInsert(table, query.Set(table.Column("email"), "ada@example.com"))
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"email\") VALUES (?)").WithArgs("ada@example.com").WillReturnResult(sqlmock.NewResult(1, 1))
	result, err := rasql.Exec(t.Context(), db, statement)
	require.NotNil(t, result)
	require.Error(t, err)
	var extensionErr *rasql.ExtensionError
	require.ErrorAs(t, err, &extensionErr)
	require.True(t, extensionErr.ExecutionSucceeded())
	rows, rowsErr := result.RowsAffected()
	require.NoError(t, rowsErr)
	require.Equal(t, int64(1), rows)

	type user struct {
		Email string `rasql:"email"`
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name:    "users",
		Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}},
	})
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"email\") VALUES (?)").WithArgs("grace@example.com").WillReturnResult(sqlmock.NewResult(2, 1))
	result, err = rasql.Insert(t.Context(), db, users, user{Email: "grace@example.com"})
	require.NotNil(t, result)
	require.Error(t, err)
	rows, rowsErr = result.RowsAffected()
	require.NoError(t, rowsErr)
	require.Equal(t, int64(1), rows)
}
