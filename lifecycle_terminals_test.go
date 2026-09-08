package rasql_test

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type terminalUser struct{ ID int64 }

type terminalUserDecoder struct{ schema rasql.ResultSchema }

func (d terminalUserDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (terminalUserDecoder) Presence() []rasql.Presence         { return nil }
func (terminalUserDecoder) DecodeRow(source rasql.ScanSource, result *terminalUser) error {
	return source.Scan(&result.ID)
}

type terminalValue struct{ Value int }

type terminalValueDecoder struct{ schema rasql.ResultSchema }

func (d terminalValueDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (terminalValueDecoder) Presence() []rasql.Presence         { return nil }
func (terminalValueDecoder) DecodeRow(source rasql.ScanSource, result *terminalValue) error {
	return source.Scan(&result.Value)
}

func terminalProjection[R any](t *testing.T, decoder rasql.RowDecoder[R]) rasql.Projection[R] {
	t.Helper()
	projection, err := rasql.NativeProjection(decoder)
	require.NoError(t, err)
	return projection
}

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
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
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
	executor, err = rasql.AsExecutor(db, profile)
	require.NoError(t, err)

	userProjection := terminalProjection(t, terminalUserDecoder{schema: mustTerminalSchema(t, rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})})
	query, err := rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: "SELECT id FROM users WHERE id = ?", Args: []rasql.NativeArgument{{Value: int64(1)}}}, userProjection, rasql.ExactlyOne)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id FROM users WHERE id = ?").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}))
	_, err = rasql.One(t.Context(), executor, query)
	require.ErrorIs(t, err, rasql.ErrNoRows)
	require.Len(t, completions, 1)
	require.ErrorIs(t, completions[0].Err, rasql.ErrNoRows)

	completions = nil
	mock.ExpectQuery("SELECT id FROM users WHERE id = ?").WithArgs(int64(1)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(1).AddRow(2))
	_, err = rasql.One(t.Context(), executor, query)
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	require.Len(t, completions, 1)
	require.ErrorIs(t, completions[0].Err, rasql.ErrMultipleRows)

	completions = nil
	valueProjection := terminalProjection(t, terminalValueDecoder{schema: mustTerminalSchema(t, rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})})
	valueQuery, err := rasql.Native(rasql.NativeStatement{Engine: "sqlite", SQL: "SELECT value"}, valueProjection, rasql.Many)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT value").WillReturnRows(sqlmock.NewRows([]string{"value"}).AddRow(1).AddRow("bad"))
	_, err = rasql.All(t.Context(), executor, valueQuery)
	require.Error(t, err)
	require.Len(t, completions, 1)
	require.Equal(t, int64(1), completions[0].RowsRead)
	require.ErrorIs(t, completions[0].Err, err)
}

func mustTerminalSchema(t *testing.T, column rasql.ResultColumn) rasql.ResultSchema {
	t.Helper()
	result, err := rasql.NewResultSchema(column)
	require.NoError(t, err)
	return result
}
