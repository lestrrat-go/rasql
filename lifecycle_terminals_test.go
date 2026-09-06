package rasql_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/dynamic"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestTypedOneAndWriteOneCompletionOwnCardinality(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	var completions []rasql.Completion
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, value rasql.Completion) error {
			if value.Phase == rasql.ConsumptionPhase {
				completions = append(completions, value)
			}
			return nil
		})
	}))
	require.NoError(t, err)
	users, err := rasql.TableOf[terminalUser](schema.TableDef{
		Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	id := users.Column("id")
	builder := rasql.SelectFrom(users).WhereEqual(id, 1)
	statement, err := builder.Build(dialect.SQLite())
	require.NoError(t, err)
	mock.ExpectQuery(statement.SQL()).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err = builder.One(t.Context(), db)
	require.ErrorIs(t, err, rasql.ErrNoRows)
	require.Len(t, completions, 1)
	require.ErrorIs(t, completions[0].Err, rasql.ErrNoRows)
	completions = nil
	mock.ExpectQuery(statement.SQL()).WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2))
	_, err = builder.One(t.Context(), db)
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	require.Len(t, completions, 1)
	require.ErrorIs(t, completions[0].Err, rasql.ErrMultipleRows)

	deleteStatement, err := query.NewDelete(users.Ref())
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.AllowAll()
	require.NoError(t, err)
	deleteStatement, err = deleteStatement.WithReturning(id)
	require.NoError(t, err)
	completions = nil
	mock.ExpectQuery("DELETE FROM \"users\" RETURNING \"id\"").WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err = rasql.QueryWriteOne[terminalUser](t.Context(), db, deleteStatement)
	require.ErrorIs(t, err, rasql.ErrNoRows)
	require.Len(t, completions, 1)
	require.ErrorIs(t, completions[0].Err, rasql.ErrNoRows)
	completions = nil
	mock.ExpectQuery("DELETE FROM \"users\" RETURNING \"id\"").WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2))
	_, err = rasql.QueryWriteOne[terminalUser](t.Context(), db, deleteStatement)
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	require.Len(t, completions, 1)
	require.ErrorIs(t, completions[0].Err, rasql.ErrMultipleRows)

	completions = nil
	mock.ExpectQuery("SELECT value").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1).AddRow("bad"))
	_, err = rasql.QueryRenderedAll[terminalValue](t.Context(), db, stmt.New("SELECT value"))
	require.Error(t, err)
	require.Len(t, completions, 1)
	require.Equal(t, int64(1), completions[0].RowsRead)
	require.ErrorIs(t, completions[0].Err, err)
}

func TestDynamicCountConversionCompletesWithError(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	var completions []rasql.Completion
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, value rasql.Completion) error {
			if value.Phase == rasql.ConsumptionPhase {
				completions = append(completions, value)
			}
			return nil
		})
	}))
	require.NoError(t, err)
	table, err := query.NewTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	mock.ExpectQuery("SELECT").WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow("bad"))
	_, err = dynamic.SelectFrom(table).Select("id").Count(t.Context(), db)
	require.Error(t, err)
	require.Len(t, completions, 1)
	require.ErrorIs(t, completions[0].Err, err)
}

type terminalUser struct {
	ID int64 `rasql:"id"`
}

type terminalValue struct {
	Value int `rasql:"value"`
}
