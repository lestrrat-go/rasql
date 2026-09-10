package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestScopeCapabilities(t *testing.T) {
	t.Run("wrappers preserve capabilities conditionally", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := AsExecutor(db, profile)
		require.NoError(t, err)
		assertScopeCapabilities(t, executor, true, true, executionDurabilityCommitted)
		registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"test": passthroughCodec{}})
		require.NoError(t, err)
		wrapped, err := WithCodecs(executor, registry)
		require.NoError(t, err)
		assertScopeCapabilities(t, wrapped, true, true, executionDurabilityCommitted)
		codecs, ok := wrapped.(CodecProvider)
		require.True(t, ok)
		require.Equal(t, registry, codecs.Codecs())
		profiled, err := WithEngineProfile(wrapped, profile)
		require.NoError(t, err)
		assertScopeCapabilities(t, profiled, true, true, executionDurabilityCommitted)

		custom := capabilityTestExecutor{}
		customProfiled, err := WithEngineProfile(custom, profile)
		require.NoError(t, err)
		assertScopeCapabilities(t, customProfiled, false, false, executionDurabilityUnknown)
		customWrapped, err := WithCodecs(customProfiled, registry)
		require.NoError(t, err)
		assertScopeCapabilities(t, customWrapped, false, false, executionDurabilityUnknown)
		customScoped := capabilityScopedExecutor{}
		scopedProfiled, err := WithEngineProfile(customScoped, profile)
		require.NoError(t, err)
		assertScopeCapabilities(t, scopedProfiled, true, true, executionDurabilityUnknown)
		_, hasCompiler := scopedProfiled.(compilerProvider)
		require.True(t, hasCompiler)
		scopedObserved, err := WithEventObservers(customScoped, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}))
		require.NoError(t, err)
		_, hasCompiler = scopedObserved.(compilerProvider)
		_, hasCodecs := scopedObserved.(CodecProvider)
		_, hasEvidence := scopedObserved.(executionDurabilityProvider)
		require.False(t, hasCompiler)
		require.False(t, hasCodecs)
		require.False(t, hasEvidence)
		allWrapped, err := WithEventObservers(customWrapped, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}))
		require.NoError(t, err)
		_, hasCompiler = allWrapped.(compilerProvider)
		_, hasCodecs = allWrapped.(CodecProvider)
		_, hasEvidence = allWrapped.(executionDurabilityProvider)
		require.True(t, hasCompiler)
		require.True(t, hasCodecs)
		require.False(t, hasEvidence)

		beginner := executor.(transactionBeginner)
		child, finalizer, err := beginner.beginScope(t.Context(), nil)
		require.NoError(t, err)
		assertScopeCapabilities(t, child, true, true, executionDurabilityPending)
		require.NoError(t, finalizer.Rollback(t.Context()))
	})

	t.Run("a logical invocation forwards only from observed executors", func(t *testing.T) {
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		handler := ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {})
		custom, err := WithEngineProfile(capabilityTestExecutor{}, profile)
		require.NoError(t, err)
		custom, err = WithCodecs(custom, codecRegistry{})
		require.NoError(t, err)
		_, hasProvider := custom.(logicalInvocationProvider)
		require.False(t, hasProvider)

		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := New(database, dialect.SQLite())
		require.NoError(t, err)
		base, err := AsExecutor(db, profile)
		require.NoError(t, err)
		observed, err := WithEventObservers(base, handler, EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) { return ctx, nil }))
		require.NoError(t, err)
		profiled, err := WithEngineProfile(observed, profile)
		require.NoError(t, err)
		profiled, err = WithCodecs(profiled, codecRegistry{})
		require.NoError(t, err)
		_, hasProvider = profiled.(logicalInvocationProvider)
		require.True(t, hasProvider)

		withoutObservers, err := WithEventObservers(base, handler)
		require.NoError(t, err)
		_, hasProvider = withoutObservers.(logicalInvocationProvider)
		require.False(t, hasProvider)
	})

	t.Run("the child a logical invocation returns retains its capabilities", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		db, err := New(database, dialect.SQLite())
		require.NoError(t, err)
		base, err := AsExecutor(db, profile)
		require.NoError(t, err)
		registry := codecRegistry{}
		observed, err := WithEventObservers(base, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) { return ctx, nil }))
		require.NoError(t, err)
		profiled, err := WithEngineProfile(observed, profile)
		require.NoError(t, err)
		wrapped, err := WithCodecs(profiled, registry)
		require.NoError(t, err)
		_, child, completion := beginLogicalInvocation(context.Background(), wrapped, EventMutationBatch)
		defer completion.completeLogicalInvocation(nil, 0, false)
		_, hasCompiler := child.(compilerProvider)
		_, hasCodecs := child.(CodecProvider)
		_, hasScope := child.(transactionBeginner)
		_, hasSavepoint := child.(savepointBeginner)
		_, hasEvidence := child.(executionDurabilityProvider)
		_, hasLogical := child.(logicalInvocationProvider)
		require.True(t, hasCompiler)
		require.True(t, hasCodecs)
		require.True(t, hasScope)
		require.True(t, hasSavepoint)
		require.True(t, hasEvidence)
		require.True(t, hasLogical)
	})

	t.Run("a profiled scope child retains the compiler identity", func(t *testing.T) {
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		parent, err := WithEngineProfile(capabilityScopedExecutor{}, profile)
		require.NoError(t, err)
		parentCompiler := parent.(compilerProvider).queryCompiler()
		child, finalizer, err := parent.(transactionBeginner).beginScope(t.Context(), nil)
		require.NoError(t, err)
		require.Same(t, parentCompiler, child.(compilerProvider).queryCompiler())
		require.NoError(t, finalizer.Rollback(t.Context()))
	})

	t.Run("children retain the registry whatever the wrapper order", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"test": passthroughCodec{}})
		require.NoError(t, err)
		base, err := AsExecutor(db, profile)
		require.NoError(t, err)
		for _, test := range []struct {
			name string
			wrap func(Executor) (Executor, error)
		}{
			{name: "codecs then profile", wrap: func(executor Executor) (Executor, error) {
				withCodecs, err := WithCodecs(executor, registry)
				if err != nil {
					return nil, err
				}
				return WithEngineProfile(withCodecs, profile)
			}},
			{name: "profile then codecs", wrap: func(executor Executor) (Executor, error) {
				withProfile, err := WithEngineProfile(executor, profile)
				if err != nil {
					return nil, err
				}
				return WithCodecs(withProfile, registry)
			}},
		} {
			t.Run(test.name, func(t *testing.T) {
				executor, err := test.wrap(base)
				require.NoError(t, err)
				child, finalizer, err := executor.(transactionBeginner).beginScope(t.Context(), nil)
				require.NoError(t, err)
				require.Equal(t, registry, child.(CodecProvider).Codecs())
				require.NoError(t, finalizer.Rollback(t.Context()))
			})
		}
	})
}

func TestScopeFinalizers(t *testing.T) {
	t.Run("Within rejects a nil begin result before the callback", func(t *testing.T) {
		for _, test := range []struct {
			name     string
			executor Executor
		}{
			{name: "nil child", executor: nilBeginExecutor{kind: nilChild}},
			{name: "nil finalizer", executor: nilBeginExecutor{kind: nilFinalizer}},
			{name: "typed nil child", executor: nilBeginExecutor{kind: typedNilChild}},
			{name: "typed nil finalizer", executor: nilBeginExecutor{kind: typedNilFinalizerKind}},
		} {
			t.Run(test.name, func(t *testing.T) {
				called := false
				err := Within(t.Context(), test.executor, nil, func(context.Context, Executor) error { called = true; return nil })
				var planErr *PlanError
				require.ErrorAs(t, err, &planErr)
				require.Equal(t, "transaction_scope_invalid", planErr.Code)
				require.False(t, called)
			})
		}
	})

	t.Run("event wrappers reject a nil begin result before the callback", func(t *testing.T) {
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		registry := codecRegistry{}
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
				base := Executor(nilBeginExecutor{kind: test.kind})
				observed, observeErr := WithEventObservers(base, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) { return ctx, nil }))
				require.NoError(t, observeErr)
				profiled, profileErr := WithEngineProfile(observed, profile)
				require.NoError(t, profileErr)
				wrapped, codecErr := WithCodecs(profiled, registry)
				require.NoError(t, codecErr)
				called := false
				scopeErr := Within(context.Background(), wrapped, nil, func(context.Context, Executor) error { called = true; return nil })
				var planErr *PlanError
				require.ErrorAs(t, scopeErr, &planErr)
				require.Equal(t, "transaction_scope_invalid", planErr.Code)
				require.False(t, called)
			})
		}
	})

	t.Run("an observed finalizer emits one scope terminal", func(t *testing.T) {
		terminals := 0
		observed, err := WithEventObservers(capabilityScopedExecutor{}, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
			return ctx, EventCompletionFunc(func(_ context.Context, event Event) error {
				if event.Kind == EventScope && event.Phase == EventTerminal {
					terminals++
				}
				return nil
			})
		}))
		require.NoError(t, err)
		beginner, ok := observed.(transactionBeginner)
		require.True(t, ok)
		_, finalizer, err := beginner.beginScope(context.Background(), nil)
		require.NoError(t, err)
		require.NoError(t, finalizer.Rollback(context.Background()))
		require.NoError(t, finalizer.Rollback(context.Background()))
		require.Equal(t, 1, terminals)
	})

	t.Run("the outer finalizer rejects open rows on commit", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		db, err := New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := AsExecutor(db, profile)
		require.NoError(t, err)
		child, finalizer, err := executor.(transactionBeginner).beginScope(context.Background(), nil)
		require.NoError(t, err)
		rows, err := child.Query(context.Background(), stmt.New("SELECT 1"))
		require.NoError(t, err)
		var planErr *PlanError
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
		db, err := New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := AsExecutor(db, profile)
		require.NoError(t, err)
		child, finalizer, err := executor.(transactionBeginner).beginScope(context.Background(), nil)
		require.NoError(t, err)
		rows, err := child.Query(context.Background(), stmt.New("SELECT 1"))
		require.NoError(t, err)
		var planErr *PlanError
		err = finalizer.Rollback(context.Background())
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "transaction_concurrent_use", planErr.Code)
		require.NoError(t, rows.Finish(nil, true))
		require.NoError(t, finalizer.Rollback(context.Background()))
	})
}

func assertScopeCapabilities(t *testing.T, executor Executor, scope, savepoint bool, evidence executionDurabilityEvidence) {
	t.Helper()
	_, hasScope := executor.(transactionBeginner)
	_, hasSavepoint := executor.(savepointBeginner)
	provider, hasEvidence := executor.(executionDurabilityProvider)
	require.Equal(t, scope, hasScope)
	require.Equal(t, savepoint, hasSavepoint)
	if hasEvidence {
		require.Equal(t, evidence, provider.executionDurability())
		return
	}
	require.Equal(t, executionDurabilityUnknown, evidence)
}

type passthroughCodec struct{}

func (passthroughCodec) Encode(value any) (driver.Value, error)   { return value, nil }
func (passthroughCodec) Decode(source any, destination any) error { return nil }

type capabilityTestExecutor struct{}

func (capabilityTestExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (capabilityTestExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	return nil, nil
}
func (capabilityTestExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return nil, nil
}

type capabilityScopedExecutor struct{}

func (capabilityScopedExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }
func (capabilityScopedExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	return nil, nil
}
func (capabilityScopedExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return nil, nil
}
func (capabilityScopedExecutor) beginScope(context.Context, *sql.TxOptions) (Executor, scopeFinalizer, error) {
	return capabilityScopedExecutor{}, capabilityFinalizer{}, nil
}
func (capabilityScopedExecutor) beginSavepoint(context.Context) (Executor, scopeFinalizer, error) {
	return capabilityScopedExecutor{}, capabilityFinalizer{}, nil
}
func (capabilityScopedExecutor) scopeIsTransaction() bool { return false }

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

func (nilBeginExecutor) Dialect() dialect.Dialect                                  { return dialect.SQLite() }
func (nilBeginExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) { return nil, nil }
func (nilBeginExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error)  { return nil, nil }
func (e nilBeginExecutor) beginScope(context.Context, *sql.TxOptions) (Executor, scopeFinalizer, error) {
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
func (e nilBeginExecutor) beginSavepoint(context.Context) (Executor, scopeFinalizer, error) {
	return e.beginScope(context.Background(), nil)
}
func (nilBeginExecutor) scopeIsTransaction() bool { return false }

type typedNilExecutor struct{}

func (*typedNilExecutor) Dialect() dialect.Dialect                                  { return dialect.SQLite() }
func (*typedNilExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) { return nil, nil }
func (*typedNilExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error)  { return nil, nil }

type typedNilFinalizer struct{}

func (*typedNilFinalizer) Commit(context.Context) error   { return nil }
func (*typedNilFinalizer) Rollback(context.Context) error { return nil }
