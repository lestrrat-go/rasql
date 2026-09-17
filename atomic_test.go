package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestAtomicOwnsTransactionAndNestedSavepoint(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	db, err := rasql.Open(t.Context(), database, dialect.SQLite())
	require.NoError(t, err)
	_, err = db.Exec(t.Context(), stmt.New("CREATE TABLE values_table (value INTEGER)"))
	require.NoError(t, err)
	callbackErr := errors.New("nested failure")
	err = db.Atomic(t.Context(), nil, func(ctx context.Context, outer rasql.DB) error {
		_, err := outer.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (1)"))
		require.NoError(t, err)
		err = outer.Atomic(ctx, nil, func(ctx context.Context, nested rasql.DB) error {
			_, err := nested.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (2)"))
			require.NoError(t, err)
			return callbackErr
		})
		require.ErrorIs(t, err, callbackErr)
		_, err = outer.Exec(ctx, stmt.New("INSERT INTO values_table VALUES (3)"))
		return err
	})
	require.NoError(t, err)
	var values string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT group_concat(value, ',') FROM values_table").Scan(&values))
	require.Equal(t, "1,3", values)
}

func TestAtomicNestedNamesAreQuotedAndDistinct(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	mock.ExpectBegin()
	var statements []string
	db, err = db.WithHooks(rasql.HookFunc{BeforeFunc: func(_ context.Context, operation rasql.Operation) error {
		if operation.Kind() == rasql.ExecOperation && regexp.MustCompile(`^(SAVEPOINT|RELEASE SAVEPOINT)`).MatchString(operation.SQL()) {
			statements = append(statements, operation.SQL())
		}
		return nil
	}})
	require.NoError(t, err)
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, tx.Atomic(t.Context(), nil, func(ctx context.Context, nested rasql.DB) error {
		return nested.Atomic(ctx, nil, func(context.Context, rasql.DB) error { return nil })
	}))
	require.NoError(t, tx.Commit())
	require.Len(t, statements, 4)
	require.NotEqual(t, statements[0], statements[1])
	require.Equal(t, statements[1][len("SAVEPOINT "):], statements[2][len("RELEASE SAVEPOINT "):])
	require.Equal(t, statements[0][len("SAVEPOINT "):], statements[3][len("RELEASE SAVEPOINT "):])
}

func TestAtomicSavepointChildRejectsOuterFinalization(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	db, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	err = db.Atomic(t.Context(), nil, func(ctx context.Context, tx rasql.DB) error {
		return tx.Atomic(ctx, nil, func(_ context.Context, scoped rasql.DB) error {
			require.ErrorContains(t, scoped.Commit(), "cannot commit")
			require.ErrorContains(t, scoped.Rollback(), "cannot roll back")
			return nil
		})
	})
	require.NoError(t, err)
}

func TestAtomicCleanupUsesDetachedContextAfterCancellation(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	db, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	callbackErr := errors.New("cancelled callback")
	err = tx.Atomic(ctx, nil, func(context.Context, rasql.DB) error {
		cancel()
		return callbackErr
	})
	require.ErrorIs(t, err, callbackErr)
	require.NoError(t, tx.Rollback())
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	tx, err = db.Begin(t.Context(), nil)
	require.NoError(t, err)
	panicValue := errors.New("cancelled panic")
	ctx2, cancel2 := context.WithCancel(t.Context())
	func() {
		defer func() { require.Equal(t, panicValue, recover()) }()
		err = tx.Atomic(ctx2, nil, func(context.Context, rasql.DB) error {
			cancel2()
			panic(panicValue)
		})
	}()
	require.NoError(t, tx.Rollback())
}

func TestAtomicSavepointLifecycleObserversAreInherited(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()
	db, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	var statements []string
	db, err = db.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.InvocationObserverFunc(func(ctx context.Context, _ rasql.Operation) (context.Context, rasql.CompletionObserver) {
		return ctx, rasql.CompletionObserverFunc(func(_ context.Context, completion rasql.Completion) error {
			if completion.Phase == rasql.ExecutionPhase && completion.Operation.Kind() == rasql.ExecOperation {
				statements = append(statements, completion.Operation.SQL())
			}
			return nil
		})
	}))
	require.NoError(t, err)
	require.NoError(t, db.Atomic(t.Context(), nil, func(ctx context.Context, tx rasql.DB) error {
		return tx.Atomic(ctx, nil, func(context.Context, rasql.DB) error { return nil })
	}))
	require.Len(t, statements, 2)
	require.Contains(t, statements[0], "SAVEPOINT")
	require.Contains(t, statements[1], "RELEASE SAVEPOINT")
}

func TestAtomicReleaseAndOuterRollbackErrorsRemainReachable(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	releaseErr := errors.New("release failed")
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnError(releaseErr)
	mock.ExpectRollback()
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	err = tx.Atomic(t.Context(), nil, func(context.Context, rasql.DB) error { return nil })
	require.ErrorIs(t, err, releaseErr)
	require.NoError(t, tx.Rollback())

	mock.ExpectBegin()
	callbackErr := errors.New("outer callback failed")
	rollbackErr := errors.New("outer rollback failed")
	mock.ExpectRollback().WillReturnError(rollbackErr)
	err = db.Atomic(t.Context(), nil, func(context.Context, rasql.DB) error { return callbackErr })
	require.ErrorIs(t, err, callbackErr)
	require.ErrorIs(t, err, rollbackErr)
	mock.ExpectBegin()
	commitErr := errors.New("outer commit failed")
	mock.ExpectCommit().WillReturnError(commitErr)
	err = db.Atomic(t.Context(), nil, func(context.Context, rasql.DB) error { return nil })
	require.ErrorIs(t, err, commitErr)
}

func TestAtomicOuterPanicCleanupWrapsRollbackFailure(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	mock.ExpectBegin()
	rollbackErr := errors.New("outer rollback failed")
	mock.ExpectRollback().WillReturnError(rollbackErr)
	panicValue := errors.New("outer panic")
	func() {
		defer func() {
			panicErr, ok := recover().(error)
			require.True(t, ok)
			var atomicPanic rasql.AtomicPanic
			require.ErrorAs(t, panicErr, &atomicPanic)
			require.Equal(t, panicValue, atomicPanic.Value)
			require.ErrorIs(t, atomicPanic, rollbackErr)
		}()
		require.NoError(t, db.Atomic(t.Context(), nil, func(context.Context, rasql.DB) error { panic(panicValue) }))
	}()
}

func TestAtomicRejectsNestedOptionsAndUnsupportedDialectBeforeCallback(t *testing.T) {
	database, mock, err := sqlmock.New()
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	mock.ExpectBegin()
	mock.ExpectRollback()
	// The pinned profile mirrors SQLite335's own capabilities except
	// Savepoints, matching what unsupportedSavepointDialect actually answers
	// for every capability. Open validates that the dialect and the profile
	// agree, so a profile claiming a savepoint capability the dialect itself
	// refuses would fail construction before ever reaching Atomic.
	unsupportedCapabilities := rasql.SQLite335().Capabilities()
	unsupportedCapabilities.Savepoints = false
	unsupportedProfile, err := rasql.NewCustomEngineProfile("sqlite-no-savepoint", rasql.SQLite335().Version(), unsupportedCapabilities, rasql.SQLite335().Limits())
	require.NoError(t, err)
	db, err := rasql.Open(t.Context(), database, unsupportedSavepointDialect{Dialect: dialect.SQLite()}, rasql.WithProfile(unsupportedProfile))
	require.NoError(t, err)
	db, err = db.Begin(t.Context(), nil)
	require.NoError(t, err)
	called := false
	err = db.Atomic(t.Context(), nil, func(context.Context, rasql.DB) error { called = true; return nil })
	require.ErrorContains(t, err, "does not support savepoints")
	require.False(t, called)
	require.NoError(t, db.Rollback())
	mock.ExpectBegin()
	mock.ExpectRollback()
	tx, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	tx, err = tx.Begin(t.Context(), nil)
	require.NoError(t, err)
	err = tx.Atomic(t.Context(), &sql.TxOptions{}, func(context.Context, rasql.DB) error { called = true; return nil })
	require.ErrorContains(t, err, "does not accept transaction options")
	require.False(t, called)
	require.NoError(t, tx.Rollback())
}

func TestAtomicJoinsCallbackAndBothCleanupErrors(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	rollbackErr := errors.New("rollback savepoint failed")
	releaseErr := errors.New("release savepoint failed")
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnError(rollbackErr)
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnError(releaseErr)
	mock.ExpectRollback()
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	callbackErr := errors.New("callback failed")
	err = tx.Atomic(t.Context(), nil, func(context.Context, rasql.DB) error { return callbackErr })
	require.ErrorIs(t, err, callbackErr)
	require.ErrorIs(t, err, rollbackErr)
	require.ErrorIs(t, err, releaseErr)
	require.NoError(t, tx.Rollback())
}

func TestAtomicPanicCleanupPreservesOriginalAndWrapsCleanupFailure(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	require.NoError(t, err)
	t.Cleanup(func() {
		mock.ExpectClose()
		require.NoError(t, database.Close())
		require.NoError(t, mock.ExpectationsWereMet())
	})
	db, err := rasql.Open(t.Context(), database, dialect.SQLite(), rasql.WithProfile(rasql.SQLite335()))
	require.NoError(t, err)
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectRollback()
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	original := fmt.Errorf("original panic")
	func() {
		defer func() {
			recovered := recover()
			require.Equal(t, original, recovered)
		}()
		require.NoError(t, tx.Atomic(t.Context(), nil, func(context.Context, rasql.DB) error { panic(original) }))
	}()
	require.NoError(t, tx.Rollback())
	mock.ExpectBegin()
	mock.ExpectExec(`SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnResult(sqlmock.NewResult(0, 0))
	rollbackErr := errors.New("panic rollback failed")
	mock.ExpectExec(`ROLLBACK TO SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnError(rollbackErr)
	releaseErr := errors.New("panic release failed")
	mock.ExpectExec(`RELEASE SAVEPOINT "rasql_sp_[a-z0-9]+"`).WillReturnError(releaseErr)
	mock.ExpectRollback()
	tx, err = db.Begin(t.Context(), nil)
	require.NoError(t, err)
	func() {
		defer func() {
			recovered := recover()
			panicErr, ok := recovered.(error)
			require.True(t, ok)
			var atomicPanic rasql.AtomicPanic
			require.ErrorAs(t, panicErr, &atomicPanic)
			require.Equal(t, original, atomicPanic.Value)
			require.ErrorIs(t, atomicPanic, rollbackErr)
			require.ErrorIs(t, atomicPanic, releaseErr)
		}()
		require.NoError(t, tx.Atomic(t.Context(), nil, func(context.Context, rasql.DB) error { panic(original) }))
	}()
	require.NoError(t, tx.Rollback())
}

type unsupportedSavepointDialect struct{ dialect.Dialect }

func (d unsupportedSavepointDialect) Supports(capability dialect.Capability) bool {
	if capability == dialect.CapabilitySavepoint {
		return false
	}
	return d.Dialect.Supports(capability)
}

// Name reports a name distinct from "sqlite" so Open resolves this dialect
// against a custom engine profile rather than the built-in SQLite one: the
// built-in profile always claims Savepoints, which would make Open itself
// reject the mismatch this dialect exists to exercise, before the callback
// this test asserts never runs.
func (unsupportedSavepointDialect) Name() string { return "sqlite-no-savepoint" }
