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

// This proves that ExecMutation preserves the driver's rows-affected count
// even when an after-hook fails: the write already reached the server, so a
// hook failing afterward must not turn that outcome into zero rows and an
// unknown durability. It exercises both a mutation built directly against a
// query.WriteStatement and one built through the typed CreatePlan
// constructor, since either path reaches the same ExecMutation.
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
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)

	table, err := query.NewTableRef(schema.TableDef{
		Name:    "users",
		Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}},
	})
	require.NoError(t, err)
	statement, err := query.NewInsert(table, query.Set(table.Column("email"), "ada@example.com"))
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"email\") VALUES (?)").WithArgs("ada@example.com").WillReturnResult(sqlmock.NewResult(1, 1))
	plan, err := rasql.NewStatementPlan(statement)
	require.NoError(t, err)
	outcome, err := rasql.ExecMutation(t.Context(), executor, plan)
	require.Error(t, err)
	var extensionErr *rasql.ExtensionError
	require.ErrorAs(t, err, &extensionErr)
	require.True(t, extensionErr.ExecutionSucceeded())
	require.Equal(t, int64(1), outcome.Affected)
	require.Equal(t, rasql.DurabilityCommitted, outcome.Durability)

	type user struct {
		Email string
	}
	users, err := rasql.TableOf[user](schema.TableDef{
		Name:    "users",
		Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}},
	})
	require.NoError(t, err)
	usersSource, err := rasql.SourceOf(users, "")
	require.NoError(t, err)
	email, err := rasql.BindColumn[user, string](usersSource, "email", "")
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"email\") VALUES (?)").WithArgs("grace@example.com").WillReturnResult(sqlmock.NewResult(2, 1))
	createPlan, err := rasql.NewCreatePlan(users, rasql.SetField(email, "grace@example.com"))
	require.NoError(t, err)
	outcome, err = rasql.ExecMutation(t.Context(), executor, createPlan)
	require.Error(t, err)
	require.ErrorAs(t, err, &extensionErr)
	require.True(t, extensionErr.ExecutionSucceeded())
	require.Equal(t, int64(1), outcome.Affected)
	require.Equal(t, rasql.DurabilityCommitted, outcome.Durability)
}
