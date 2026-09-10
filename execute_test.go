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

type executionRow struct{ ID int64 }

type executionDecoder struct{ schema rasql.ResultSchema }

func (d executionDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (executionDecoder) Presence() []rasql.Presence         { return nil }
func (executionDecoder) DecodeRow(source rasql.ScanSource, result *executionRow) error {
	return source.Scan(&result.ID)
}

func executionProjection(t *testing.T) rasql.Projection[executionRow] {
	t.Helper()
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NativeProjection(executionDecoder{schema: resultSchema})
	require.NoError(t, err)
	return projection
}

func TestExecution(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("postgresql-17", 17, 0, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)

	query, err := rasql.Native(rasql.NativeStatement{Engine: "postgresql", SQL: "SELECT id FROM users WHERE id = $1", Args: []rasql.NativeArgument{{Value: int64(42)}}}, executionProjection(t), rasql.Many)
	require.NoError(t, err)
	mock.ExpectQuery("SELECT id FROM users WHERE id = $1").WithArgs(int64(42)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(42))
	rows, err := rasql.All(t.Context(), executor, query)
	require.NoError(t, err)
	require.Equal(t, []executionRow{{ID: 42}}, rows)

	type user struct {
		ID    int64
		Email string
	}
	table, err := rasql.TableOf[user](schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	email, err := rasql.BindColumn[user, string](relation, "email", "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[user, int64](relation, "id", "")
	require.NoError(t, err)
	plan, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(email, "ada@example.com"))
	require.NoError(t, err)
	mock.ExpectExec("INSERT INTO \"users\" (\"id\", \"email\") VALUES ($1, $2)").WithArgs(int64(1), "ada@example.com").WillReturnResult(sqlmock.NewResult(1, 1))
	outcome, err := rasql.ExecMutation(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Equal(t, int64(1), outcome.Affected)
}

// This proves that ExecMutation preserves the driver's rows-affected count
// even when an after-hook fails: the write already reached the server, so a
// hook failing afterward must not turn that outcome into zero rows and an
// unknown durability. It exercises both a mutation built directly against a
// query.WriteStatement and one built through the typed CreatePlan
// constructor, since either path reaches the same ExecMutation.
func TestExecPreservesResultAfterHookError(t *testing.T) {
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

func TestOneAndWriteOneOwnTheirCardinality(t *testing.T) {
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
	executor, err := rasql.AsExecutor(db, profile)
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
