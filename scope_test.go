package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestWithin(t *testing.T) {
	t.Run("commits and a nested savepoint rolls back", func(t *testing.T) {
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
	})

	t.Run("rejects a custom executor before the callback", func(t *testing.T) {
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
	})

	t.Run("rejects a nil callback before begin", func(t *testing.T) {
		executor := sqliteExecutorForScope(t)
		err := rasql.Within(t.Context(), executor, nil, nil)
		require.EqualError(t, err, "rasql: scope function must not be nil")
	})

	t.Run("begin and commit errors preserve their identity", func(t *testing.T) {
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
	})

	t.Run("callback and rollback errors join", func(t *testing.T) {
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
	})

	t.Run("a panic preserves both the panic and the rollback failure", func(t *testing.T) {
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
	})

	t.Run("nested options are rejected before the callback", func(t *testing.T) {
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
	})
}

func TestScopeRowsOpen(t *testing.T) {
	t.Run("a transaction executor rejects concurrent use while rows are open", func(t *testing.T) {
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
	})

	t.Run("a savepoint begin rejects open parent rows before the callback", func(t *testing.T) {
		executor := sqliteExecutorForScope(t)
		require.NoError(t, rasql.Within(t.Context(), executor, nil, func(ctx context.Context, tx rasql.Executor) error {
			rows, err := tx.Query(ctx, stmt.New("SELECT 1"))
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Finish(nil, true)) }()
			called := false
			err = rasql.Within(ctx, tx, nil, func(context.Context, rasql.Executor) error { called = true; return nil })
			var planErr *rasql.PlanError
			require.ErrorAs(t, err, &planErr)
			require.Equal(t, "transaction_concurrent_use", planErr.Code)
			require.False(t, called)
			return nil
		}))
	})

	t.Run("a parent operation rejects open child rows", func(t *testing.T) {
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
	})
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

func TestScopeCapabilities(t *testing.T) {
	t.Run("wrappers preserve capabilities conditionally", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		assertScopeCapabilities(t, executor, true, true, rasql.Q1DurabilityCommitted)
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"test": passthroughCodec{}})
		require.NoError(t, err)
		wrapped, err := rasql.WithCodecs(executor, registry)
		require.NoError(t, err)
		assertScopeCapabilities(t, wrapped, true, true, rasql.Q1DurabilityCommitted)
		codecs, ok := wrapped.(rasql.CodecProvider)
		require.True(t, ok)
		require.Equal(t, registry, codecs.Codecs())
		profiled, err := rasql.WithEngineProfile(wrapped, profile)
		require.NoError(t, err)
		assertScopeCapabilities(t, profiled, true, true, rasql.Q1DurabilityCommitted)

		custom := capabilityTestExecutor{}
		customProfiled, err := rasql.WithEngineProfile(custom, profile)
		require.NoError(t, err)
		assertScopeCapabilities(t, customProfiled, false, false, rasql.Q1DurabilityUnknown)
		customWrapped, err := rasql.WithCodecs(customProfiled, registry)
		require.NoError(t, err)
		assertScopeCapabilities(t, customWrapped, false, false, rasql.Q1DurabilityUnknown)
		customScoped := capabilityScopedExecutor{}
		scopedProfiled, err := rasql.WithEngineProfile(customScoped, profile)
		require.NoError(t, err)
		assertScopeCapabilities(t, scopedProfiled, true, true, rasql.Q1DurabilityUnknown)
		hasCompiler := rasql.Q1QueryCompilerOf(scopedProfiled) != nil
		require.True(t, hasCompiler)
		scopedObserved, err := rasql.WithEventObservers(customScoped, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}))
		require.NoError(t, err)
		hasCompiler = rasql.Q1QueryCompilerOf(scopedObserved) != nil
		_, hasCodecs := scopedObserved.(rasql.CodecProvider)
		_, hasEvidence := rasql.Q1DurabilityOf(scopedObserved)
		require.False(t, hasCompiler)
		require.False(t, hasCodecs)
		require.False(t, hasEvidence)
		allWrapped, err := rasql.WithEventObservers(customWrapped, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}))
		require.NoError(t, err)
		hasCompiler = rasql.Q1QueryCompilerOf(allWrapped) != nil
		_, hasCodecs = allWrapped.(rasql.CodecProvider)
		_, hasEvidence = rasql.Q1DurabilityOf(allWrapped)
		require.True(t, hasCompiler)
		require.True(t, hasCodecs)
		require.False(t, hasEvidence)

		beginner := executor.(rasql.ScopeBeginner)
		child, finalizer, err := beginner.BeginScope(t.Context(), nil)
		require.NoError(t, err)
		assertScopeCapabilities(t, child, true, true, rasql.Q1DurabilityPending)
		require.NoError(t, finalizer.Rollback(t.Context()))
	})

	t.Run("a logical invocation forwards only from observed executors", func(t *testing.T) {
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		handler := rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {})
		custom, err := rasql.WithEngineProfile(capabilityTestExecutor{}, profile)
		require.NoError(t, err)
		custom, err = rasql.WithCodecs(custom, mustEmptyRegistry(t))
		require.NoError(t, err)
		hasProvider := rasql.Q1ForwardsLogicalInvocation(custom)
		require.False(t, hasProvider)

		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		base, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		observed, err := rasql.WithEventObservers(base, handler, rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) { return ctx, nil }))
		require.NoError(t, err)
		profiled, err := rasql.WithEngineProfile(observed, profile)
		require.NoError(t, err)
		profiled, err = rasql.WithCodecs(profiled, mustEmptyRegistry(t))
		require.NoError(t, err)
		hasProvider = rasql.Q1ForwardsLogicalInvocation(profiled)
		require.True(t, hasProvider)

		withoutObservers, err := rasql.WithEventObservers(base, handler)
		require.NoError(t, err)
		hasProvider = rasql.Q1ForwardsLogicalInvocation(withoutObservers)
		require.False(t, hasProvider)
	})

	t.Run("the child a logical invocation returns retains its capabilities", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		base, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		registry := mustEmptyRegistry(t)
		observed, err := rasql.WithEventObservers(base, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) { return ctx, nil }))
		require.NoError(t, err)
		profiled, err := rasql.WithEngineProfile(observed, profile)
		require.NoError(t, err)
		wrapped, err := rasql.WithCodecs(profiled, registry)
		require.NoError(t, err)
		_, child, invocation := rasql.Q1BeginLogicalInvocation(context.Background(), wrapped, rasql.EventMutationBatch)
		defer invocation.Complete()
		hasCompiler := rasql.Q1QueryCompilerOf(child) != nil
		_, hasCodecs := child.(rasql.CodecProvider)
		_, hasScope := child.(rasql.ScopeBeginner)
		_, hasSavepoint := child.(rasql.SavepointBeginner)
		_, hasEvidence := rasql.Q1DurabilityOf(child)
		hasLogical := rasql.Q1ForwardsLogicalInvocation(child)
		require.True(t, hasCompiler)
		require.True(t, hasCodecs)
		require.True(t, hasScope)
		require.True(t, hasSavepoint)
		require.True(t, hasEvidence)
		require.True(t, hasLogical)
	})

	t.Run("a profiled scope child retains the compiler identity", func(t *testing.T) {
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		parent, err := rasql.WithEngineProfile(capabilityScopedExecutor{}, profile)
		require.NoError(t, err)
		parentCompiler := rasql.Q1QueryCompilerOf(parent)
		child, finalizer, err := parent.(rasql.ScopeBeginner).BeginScope(t.Context(), nil)
		require.NoError(t, err)
		require.Same(t, parentCompiler, rasql.Q1QueryCompilerOf(child))
		require.NoError(t, finalizer.Rollback(t.Context()))
	})

	t.Run("children retain the registry whatever the wrapper order", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"test": passthroughCodec{}})
		require.NoError(t, err)
		base, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		for _, test := range []struct {
			name string
			wrap func(rasql.Executor) (rasql.Executor, error)
		}{
			{name: "codecs then profile", wrap: func(executor rasql.Executor) (rasql.Executor, error) {
				withCodecs, err := rasql.WithCodecs(executor, registry)
				if err != nil {
					return nil, err
				}
				return rasql.WithEngineProfile(withCodecs, profile)
			}},
			{name: "profile then codecs", wrap: func(executor rasql.Executor) (rasql.Executor, error) {
				withProfile, err := rasql.WithEngineProfile(executor, profile)
				if err != nil {
					return nil, err
				}
				return rasql.WithCodecs(withProfile, registry)
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				executor, err := test.wrap(base)
				require.NoError(t, err)
				child, finalizer, err := executor.(rasql.ScopeBeginner).BeginScope(t.Context(), nil)
				require.NoError(t, err)
				require.Equal(t, registry, child.(rasql.CodecProvider).Codecs())
				require.NoError(t, finalizer.Rollback(t.Context()))
			})
		}
	})
}

func TestScopeFinalizers(t *testing.T) {
	t.Run("Within rejects a nil begin result before the callback", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			executor rasql.Executor
		}{
			{name: "nil child", executor: nilBeginExecutor{kind: nilChild}},
			{name: "nil finalizer", executor: nilBeginExecutor{kind: nilFinalizer}},
			{name: "typed nil child", executor: nilBeginExecutor{kind: typedNilChild}},
			{name: "typed nil finalizer", executor: nilBeginExecutor{kind: typedNilFinalizerKind}},
		} {
			t.Run(test.name, func(t *testing.T) {
				called := false
				err := rasql.Within(t.Context(), test.executor, nil, func(context.Context, rasql.Executor) error { called = true; return nil })
				var planErr *rasql.PlanError
				require.ErrorAs(t, err, &planErr)
				require.Equal(t, "transaction_scope_invalid", planErr.Code)
				require.False(t, called)
			})
		}
	})

	t.Run("event wrappers reject a nil begin result before the callback", func(t *testing.T) {
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		registry := mustEmptyRegistry(t)
		for _, test := range []struct {
			name string
			kind nilBeginKind
		}{
			{name: "nil child", kind: nilChild},
			{name: "nil finalizer", kind: nilFinalizer},
			{name: "typed nil child", kind: typedNilChild},
			{name: "typed nil finalizer", kind: typedNilFinalizerKind},
		} {
			t.Run(test.name, func(t *testing.T) {
				base := rasql.Executor(nilBeginExecutor{kind: test.kind})
				observed, observeErr := rasql.WithEventObservers(base, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) { return ctx, nil }))
				require.NoError(t, observeErr)
				profiled, profileErr := rasql.WithEngineProfile(observed, profile)
				require.NoError(t, profileErr)
				wrapped, codecErr := rasql.WithCodecs(profiled, registry)
				require.NoError(t, codecErr)
				called := false
				scopeErr := rasql.Within(context.Background(), wrapped, nil, func(context.Context, rasql.Executor) error { called = true; return nil })
				var planErr *rasql.PlanError
				require.ErrorAs(t, scopeErr, &planErr)
				require.Equal(t, "transaction_scope_invalid", planErr.Code)
				require.False(t, called)
			})
		}
	})

	t.Run("an observed finalizer emits one scope terminal", func(t *testing.T) {
		terminals := 0
		observed, err := rasql.WithEventObservers(capabilityScopedExecutor{}, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, event rasql.Event) error {
				if event.Kind == rasql.EventScope && event.Phase == rasql.EventTerminal {
					terminals++
				}
				return nil
			})
		}))
		require.NoError(t, err)
		beginner, ok := observed.(rasql.ScopeBeginner)
		require.True(t, ok)
		_, finalizer, err := beginner.BeginScope(context.Background(), nil)
		require.NoError(t, err)
		require.NoError(t, finalizer.Rollback(context.Background()))
		require.NoError(t, finalizer.Rollback(context.Background()))
		require.Equal(t, 1, terminals)
	})

	t.Run("the outer finalizer rejects open rows on commit", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		child, finalizer, err := executor.(rasql.ScopeBeginner).BeginScope(context.Background(), nil)
		require.NoError(t, err)
		rows, err := child.Query(context.Background(), stmt.New("SELECT 1"))
		require.NoError(t, err)
		var planErr *rasql.PlanError
		err = finalizer.Commit(context.Background())
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "transaction_concurrent_use", planErr.Code)
		require.NoError(t, rows.Finish(nil, true))
		require.NoError(t, finalizer.Rollback(context.Background()))
	})

	t.Run("the outer finalizer rejects open rows on rollback", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		child, finalizer, err := executor.(rasql.ScopeBeginner).BeginScope(context.Background(), nil)
		require.NoError(t, err)
		rows, err := child.Query(context.Background(), stmt.New("SELECT 1"))
		require.NoError(t, err)
		var planErr *rasql.PlanError
		err = finalizer.Rollback(context.Background())
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "transaction_concurrent_use", planErr.Code)
		require.NoError(t, rows.Finish(nil, true))
		require.NoError(t, finalizer.Rollback(context.Background()))
	})
}

func assertScopeCapabilities(t *testing.T, executor rasql.Executor, scope, savepoint bool, evidence int) {
	t.Helper()
	_, hasScope := executor.(rasql.ScopeBeginner)
	_, hasSavepoint := executor.(rasql.SavepointBeginner)
	require.Equal(t, scope, hasScope)
	require.Equal(t, savepoint, hasSavepoint)
	durability, hasEvidence := rasql.Q1DurabilityOf(executor)
	if hasEvidence {
		require.Equal(t, evidence, durability)
		return
	}
	require.Equal(t, rasql.Q1DurabilityUnknown, evidence)
}

func mustEmptyRegistry(t *testing.T) rasql.CodecRegistry {
	t.Helper()
	registry, err := rasql.NewCodecRegistry(nil)
	require.NoError(t, err)
	return registry
}

type passthroughCodec struct{}

func (passthroughCodec) Encode(value any) (driver.Value, error)   { return value, nil }
func (passthroughCodec) Decode(source any, destination any) error { return nil }

type capabilityTestExecutor struct{}

func (capabilityTestExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (capabilityTestExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	return nil, nil
}
func (capabilityTestExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return nil, nil
}

type capabilityScopedExecutor struct{}

func (capabilityScopedExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (capabilityScopedExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	return nil, nil
}
func (capabilityScopedExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return nil, nil
}
func (capabilityScopedExecutor) BeginScope(context.Context, *sql.TxOptions) (rasql.Executor, rasql.ScopeFinalizer, error) {
	return capabilityScopedExecutor{}, capabilityFinalizer{}, nil
}
func (capabilityScopedExecutor) BeginSavepoint(context.Context) (rasql.Executor, rasql.ScopeFinalizer, error) {
	return capabilityScopedExecutor{}, capabilityFinalizer{}, nil
}
func (capabilityScopedExecutor) IsTransaction() bool { return false }

type capabilityFinalizer struct{}

func (capabilityFinalizer) Commit(context.Context) error   { return nil }
func (capabilityFinalizer) Rollback(context.Context) error { return nil }

type nilBeginKind uint8

const (
	nilChild nilBeginKind = iota
	nilFinalizer
	typedNilChild
	typedNilFinalizerKind
)

type nilBeginExecutor struct{ kind nilBeginKind }

func (nilBeginExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (nilBeginExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	return nil, nil
}
func (nilBeginExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) { return nil, nil }
func (e nilBeginExecutor) BeginScope(context.Context, *sql.TxOptions) (rasql.Executor, rasql.ScopeFinalizer, error) {
	if e.kind == nilChild {
		return nil, capabilityFinalizer{}, nil
	}
	if e.kind == nilFinalizer {
		return capabilityTestExecutor{}, nil, nil
	}
	if e.kind == typedNilChild {
		var child *typedNilExecutor
		return child, capabilityFinalizer{}, nil
	}
	var finalizer *typedNilFinalizer
	return capabilityTestExecutor{}, finalizer, nil
}
func (e nilBeginExecutor) BeginSavepoint(context.Context) (rasql.Executor, rasql.ScopeFinalizer, error) {
	return e.BeginScope(context.Background(), nil)
}
func (nilBeginExecutor) IsTransaction() bool { return false }

type typedNilExecutor struct{}

func (*typedNilExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (*typedNilExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	return nil, nil
}
func (*typedNilExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) { return nil, nil }

type typedNilFinalizer struct{}

func (*typedNilFinalizer) Commit(context.Context) error   { return nil }
func (*typedNilFinalizer) Rollback(context.Context) error { return nil }
