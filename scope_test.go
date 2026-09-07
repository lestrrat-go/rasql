package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWithinCommitsAndNestedSavepointRollsBack(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE values_table (value INTEGER)")
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	sentinel := errors.New("nested failure")
	require.NoError(t, rasql.Within(t.Context(), executor, nil, func(ctx context.Context, outer rasql.Executor) error {
		_, err := outer.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (1)"))
		require.NoError(t, err)
		err = rasql.Within(ctx, outer, nil, func(ctx context.Context, nested rasql.Executor) error {
			_, err := nested.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (2)"))
			require.NoError(t, err)
			return sentinel
		})
		require.ErrorIs(t, err, sentinel)
		_, err = outer.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (3)"))
		return err
	}))
	var values string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT group_concat(value, ',') FROM values_table").Scan(&values))
	require.Equal(t, "1,3", values)
}

func TestWithinRejectsCustomExecutorBeforeCallback(t *testing.T) {
	var called bool
	executor := customExecutor{}
	err := rasql.Within(t.Context(), executor, nil, func(context.Context, rasql.Executor) error {
		called = true
		return nil
	})
	var planErr *rasql.PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "transaction_scope_unsupported", planErr.Code)
	require.False(t, called)
}

func TestWithinRejectsNilCallbackBeforeBegin(t *testing.T) {
	executor := sqliteExecutorForScope(t)
	err := rasql.Within(t.Context(), executor, nil, nil)
	require.EqualError(t, err, "rasql: scope function must not be nil")
}

func TestWithinBeginAndCommitErrorsPreserveIdentity(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()); require.NoError(t, mock.ExpectationsWereMet()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	beginErr := errors.New("begin failed")
	mock.ExpectBegin().WillReturnError(beginErr)
	err = rasql.Within(t.Context(), executor, nil, func(context.Context, rasql.Executor) error { return errors.New("must not run") })
	require.ErrorIs(t, err, beginErr)

	commitErr := errors.New("commit failed")
	mock.ExpectBegin()
	mock.ExpectCommit().WillReturnError(commitErr)
	mock.ExpectClose()
	err = rasql.Within(t.Context(), executor, nil, func(context.Context, rasql.Executor) error { return nil })
	require.ErrorIs(t, err, commitErr)
}

func TestWithinCallbackAndRollbackErrorsJoin(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()); require.NoError(t, mock.ExpectationsWereMet()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	callbackErr := errors.New("callback failed")
	rollbackErr := errors.New("rollback failed")
	mock.ExpectBegin()
	mock.ExpectRollback().WillReturnError(rollbackErr)
	mock.ExpectClose()
	err = rasql.Within(t.Context(), executor, nil, func(context.Context, rasql.Executor) error { return callbackErr })
	require.ErrorIs(t, err, callbackErr)
	require.ErrorIs(t, err, rollbackErr)
}

func TestWithinPanicPreservesPanicAndRollbackFailure(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()); require.NoError(t, mock.ExpectationsWereMet()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	rollbackErr := errors.New("rollback failed")
	mock.ExpectBegin()
	mock.ExpectRollback().WillReturnError(rollbackErr)
	mock.ExpectClose()
	defer func() {
		value := recover()
		var panicErr rasql.AtomicPanic
		require.ErrorAs(t, value.(error), &panicErr)
		require.ErrorIs(t, panicErr, rollbackErr)
	}()
	_ = rasql.Within(t.Context(), executor, nil, func(context.Context, rasql.Executor) error { panic("callback panic") })
}

func TestWithinNestedOptionsRejectedBeforeCallback(t *testing.T) {
	executor := sqliteExecutorForScope(t)
	called := false
	require.NoError(t, rasql.Within(t.Context(), executor, nil, func(ctx context.Context, tx rasql.Executor) error {
		err := rasql.Within(ctx, tx, &sql.TxOptions{}, func(context.Context, rasql.Executor) error { called = true; return nil })
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "nested_options", planErr.Code)
		return nil
	}))
	require.False(t, called)
}

func TestTransactionExecutorRejectsConcurrentUseWhileRowsOpen(t *testing.T) {
	executor := sqliteExecutorForScope(t)
	require.NoError(t, rasql.Within(t.Context(), executor, nil, func(ctx context.Context, tx rasql.Executor) error {
		rows, err := tx.Query(ctx, stmt.New("SELECT 1"))
		require.NoError(t, err)
		require.NotNil(t, rows)
		result := make(chan error, 1)
		go func() {
			_, err := tx.Exec(ctx, stmt.New("SELECT 2"))
			result <- err
		}()
		var planErr *rasql.PlanError
		err = <-result
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "transaction_concurrent_use", planErr.Code)
		require.NoError(t, rows.Finish(nil, true))
		return nil
	}))
}

func TestSavepointBeginRejectsParentRowsOpenBeforeCallback(t *testing.T) {
	executor := sqliteExecutorForScope(t)
	require.NoError(t, rasql.Within(t.Context(), executor, nil, func(ctx context.Context, tx rasql.Executor) error {
		rows, err := tx.Query(ctx, stmt.New("SELECT 1"))
		require.NoError(t, err)
		defer rows.Finish(nil, true)
		called := false
		err = rasql.Within(ctx, tx, nil, func(context.Context, rasql.Executor) error { called = true; return nil })
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "transaction_concurrent_use", planErr.Code)
		require.False(t, called)
		return nil
	}))
}

func TestParentOperationRejectsChildRowsOpen(t *testing.T) {
	executor := sqliteExecutorForScope(t)
	require.NoError(t, rasql.Within(t.Context(), executor, nil, func(ctx context.Context, parent rasql.Executor) error {
		return rasql.Within(ctx, parent, nil, func(ctx context.Context, child rasql.Executor) error {
			rows, err := child.Query(ctx, stmt.New("SELECT 1"))
			require.NoError(t, err)
			_, err = parent.Exec(ctx, stmt.New("SELECT 2"))
			var planErr *rasql.PlanError
			require.ErrorAs(t, err, &planErr)
			require.Equal(t, "transaction_concurrent_use", planErr.Code)
			require.NoError(t, rows.Finish(nil, true))
			return nil
		})
	}))
}

func sqliteExecutorForScope(t *testing.T) rasql.Executor {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	return executor
}

type customExecutor struct{}

func (customExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (customExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	return nil, errors.New("unused")
}
func (customExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return nil, errors.New("unused")
}
