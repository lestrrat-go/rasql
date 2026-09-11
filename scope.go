package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/lestrrat-go/rasql/internal/querycompile"
)

// Scope is one owned transaction or savepoint callback.
type Scope func(context.Context, Executor) error

// ScopeBeginner is an Executor that can open an owned transaction. Within and
// ExecMutationBatch open a transaction through this interface, so an executor
// written outside this package joins them by implementing it. An executor that
// does not implement it is rejected with the code transaction_scope_unsupported.
//
// BeginScope returns the child executor that runs inside the new transaction
// and the finalizer that ends it. Neither return value may be nil when the
// error is nil.
type ScopeBeginner interface {
	BeginScope(context.Context, *sql.TxOptions) (Executor, ScopeFinalizer, error)
}

// SavepointBeginner is an Executor that can open an owned savepoint. Within
// opens a savepoint instead of a transaction when the executor it is given
// reports IsTransaction, so an executor that reports true implements this
// interface as well as ScopeBeginner.
//
// BeginSavepoint returns the child executor that runs inside the new savepoint
// and the finalizer that ends it. Neither return value may be nil when the
// error is nil.
type SavepointBeginner interface {
	BeginSavepoint(context.Context) (Executor, ScopeFinalizer, error)
}

// ScopeFinalizer ends the transaction or savepoint a ScopeBeginner or a
// SavepointBeginner opened. Within calls Commit once the callback returns nil
// and Rollback once it returns an error or panics.
type ScopeFinalizer interface {
	Commit(context.Context) error
	Rollback(context.Context) error
}

// ScopeState is an Executor that reports whether it already runs inside a
// transaction. Within opens a savepoint on an executor that reports true and a
// transaction on one that reports false or does not implement this interface.
type ScopeState interface{ IsTransaction() bool }

type scopeContextProvider interface{ scopeContext() context.Context }
type scopeCauseSetter interface{ setCause(error) }

// executionDurabilityEvidence is deliberately private. Mutation packages can
// map it to their public outcome without allowing external executors to forge
// durable evidence.
type executionDurabilityEvidence uint8

const (
	executionDurabilityUnknown executionDurabilityEvidence = iota
	executionDurabilityPending
	executionDurabilityCommitted
)

type executionDurabilityProvider interface {
	executionDurability() executionDurabilityEvidence
}

var (
	_ ScopeBeginner               = dbExecutor{}
	_ SavepointBeginner           = dbExecutor{}
	_ ScopeFinalizer              = guardedScopeFinalizer{}
	_ executionDurabilityProvider = dbExecutor{}
)

type profiledScopedExecutor struct{ profiledExecutor }

type profiledScopedEvidenceExecutor struct{ profiledScopedExecutor }

type profiledCodecScopedExecutor struct{ profiledScopedExecutor }

type profiledCodecScopedEvidenceExecutor struct{ profiledCodecScopedExecutor }

type codecScopedExecutor struct{ codecExec }
type codecCompilerScopedExecutor struct{ codecScopedExecutor }
type codecScopedEvidenceExecutor struct{ codecScopedExecutor }
type codecCompilerScopedEvidenceExecutor struct{ codecCompilerScopedExecutor }

type logicalProfiledScopedExecutor struct{ profiledScopedExecutor }
type logicalProfiledScopedEvidenceExecutor struct{ profiledScopedEvidenceExecutor }
type logicalProfiledCodecScopedExecutor struct{ profiledCodecScopedExecutor }
type logicalProfiledCodecScopedEvidenceExecutor struct {
	profiledCodecScopedEvidenceExecutor
}
type logicalCodecScopedExecutor struct{ codecScopedExecutor }
type logicalCodecCompilerScopedExecutor struct{ codecCompilerScopedExecutor }
type logicalCodecScopedEvidenceExecutor struct{ codecScopedEvidenceExecutor }
type logicalCodecCompilerScopedEvidenceExecutor struct {
	codecCompilerScopedEvidenceExecutor
}

func (e logicalProfiledScopedExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapProfiledChild(child, e.compiler), completion
}
func (e logicalProfiledScopedEvidenceExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapProfiledChild(child, e.compiler), completion
}
func (e logicalProfiledCodecScopedExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapProfiledChildWithCodecs(child, e.compiler, e.Codecs()), completion
}
func (e logicalProfiledCodecScopedEvidenceExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapProfiledChildWithCodecs(child, e.compiler, e.Codecs()), completion
}
func (e logicalCodecScopedExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapCodecExecutor(child, e.codecs), completion
}
func (e logicalCodecCompilerScopedExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapCodecExecutor(child, e.codecs), completion
}
func (e logicalCodecScopedEvidenceExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapCodecExecutor(child, e.codecs), completion
}
func (e logicalCodecCompilerScopedEvidenceExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapCodecExecutor(child, e.codecs), completion
}

func (e profiledScopedExecutor) IsTransaction() bool {
	state, ok := e.Executor.(ScopeState)
	return ok && state.IsTransaction()
}
func (e profiledScopedExecutor) scopeContext() context.Context {
	provider, _ := e.Executor.(scopeContextProvider)
	if provider == nil {
		return nil
	}
	return provider.scopeContext()
}

func (e profiledCodecScopedExecutor) Codecs() CodecRegistry {
	provider, _ := e.Executor.(CodecProvider)
	if provider == nil {
		return nil
	}
	return provider.Codecs()
}

func (e profiledCodecScopedExecutor) BeginScope(ctx context.Context, opts *sql.TxOptions) (Executor, ScopeFinalizer, error) {
	beginner, ok := e.Executor.(ScopeBeginner)
	if !ok {
		return nil, nil, unsupportedScopeError()
	}
	child, finalizer, err := beginner.BeginScope(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	return wrapProfiledChildWithCodecs(child, e.compiler, e.Codecs()), finalizer, nil
}

func (e profiledCodecScopedExecutor) BeginSavepoint(ctx context.Context) (Executor, ScopeFinalizer, error) {
	beginner, ok := e.Executor.(SavepointBeginner)
	if !ok {
		return nil, nil, planError("savepoint_unsupported", "scope", "executor does not support savepoints")
	}
	child, finalizer, err := beginner.BeginSavepoint(ctx)
	if err != nil {
		return nil, nil, err
	}
	return wrapProfiledChildWithCodecs(child, e.compiler, e.Codecs()), finalizer, nil
}

func (e profiledScopedEvidenceExecutor) executionDurability() executionDurabilityEvidence {
	provider, _ := e.Executor.(executionDurabilityProvider)
	if provider == nil {
		return executionDurabilityUnknown
	}
	return provider.executionDurability()
}

func (e profiledCodecScopedEvidenceExecutor) executionDurability() executionDurabilityEvidence {
	provider, _ := e.Executor.(executionDurabilityProvider)
	if provider == nil {
		return executionDurabilityUnknown
	}
	return provider.executionDurability()
}

func (e codecScopedExecutor) IsTransaction() bool {
	state, ok := e.Executor.(ScopeState)
	return ok && state.IsTransaction()
}
func (e codecScopedExecutor) scopeContext() context.Context {
	provider, _ := e.Executor.(scopeContextProvider)
	if provider == nil {
		return nil
	}
	return provider.scopeContext()
}

func (e codecScopedExecutor) BeginScope(ctx context.Context, opts *sql.TxOptions) (Executor, ScopeFinalizer, error) {
	beginner, ok := e.Executor.(ScopeBeginner)
	if !ok {
		return nil, nil, unsupportedScopeError()
	}
	child, finalizer, err := beginner.BeginScope(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	return wrapCodecExecutor(child, e.codecs), finalizer, nil
}

func (e codecScopedExecutor) BeginSavepoint(ctx context.Context) (Executor, ScopeFinalizer, error) {
	beginner, ok := e.Executor.(SavepointBeginner)
	if !ok {
		return nil, nil, planError("savepoint_unsupported", "scope", "executor does not support savepoints")
	}
	child, finalizer, err := beginner.BeginSavepoint(ctx)
	if err != nil {
		return nil, nil, err
	}
	return wrapCodecExecutor(child, e.codecs), finalizer, nil
}

func (e codecCompilerScopedExecutor) queryCompiler() *querycompile.Compiler {
	provider, _ := e.Executor.(compilerProvider)
	if provider == nil {
		return nil
	}
	return provider.queryCompiler()
}

func (e codecScopedEvidenceExecutor) executionDurability() executionDurabilityEvidence {
	provider, ok := e.Executor.(executionDurabilityProvider)
	if !ok {
		return executionDurabilityUnknown
	}
	return provider.executionDurability()
}

func (e codecCompilerScopedEvidenceExecutor) executionDurability() executionDurabilityEvidence {
	provider, ok := e.Executor.(executionDurabilityProvider)
	if !ok {
		return executionDurabilityUnknown
	}
	return provider.executionDurability()
}

func (e profiledScopedExecutor) BeginScope(ctx context.Context, opts *sql.TxOptions) (Executor, ScopeFinalizer, error) {
	beginner, ok := e.Executor.(ScopeBeginner)
	if !ok {
		return nil, nil, unsupportedScopeError()
	}
	child, finalizer, err := beginner.BeginScope(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	return wrapProfiledChild(child, e.compiler), finalizer, nil
}

func (e profiledScopedExecutor) BeginSavepoint(ctx context.Context) (Executor, ScopeFinalizer, error) {
	beginner, ok := e.Executor.(SavepointBeginner)
	if !ok {
		return nil, nil, planError("savepoint_unsupported", "scope", "executor does not support savepoints")
	}
	child, finalizer, err := beginner.BeginSavepoint(ctx)
	if err != nil {
		return nil, nil, err
	}
	return wrapProfiledChild(child, e.compiler), finalizer, nil
}

func wrapProfiledChild(child Executor, compiler *querycompile.Compiler) Executor {
	return wrapProfiledChildWithCodecs(child, compiler, nil)
}

func wrapProfiledChildWithCodecs(child Executor, compiler *querycompile.Compiler, codecs CodecRegistry) Executor {
	if codecs != nil {
		child = wrapCodecExecutor(child, codecs)
	}
	base := profiledExecutor{Executor: child, compiler: compiler}
	hasLogical := false
	if _, ok := child.(logicalInvocationProvider); ok {
		hasLogical = true
	}
	if _, scope := child.(ScopeBeginner); scope {
		if _, evidence := child.(executionDurabilityProvider); evidence {
			if _, codecs := child.(CodecProvider); codecs {
				if hasLogical {
					return logicalProfiledCodecScopedEvidenceExecutor{profiledCodecScopedEvidenceExecutor{profiledCodecScopedExecutor{profiledScopedExecutor{base}}}}
				}
				return profiledCodecScopedEvidenceExecutor{profiledCodecScopedExecutor: profiledCodecScopedExecutor{profiledScopedExecutor: profiledScopedExecutor{profiledExecutor: base}}}
			}
			if hasLogical {
				return logicalProfiledScopedEvidenceExecutor{profiledScopedEvidenceExecutor{profiledScopedExecutor{base}}}
			}
			return profiledScopedEvidenceExecutor{profiledScopedExecutor: profiledScopedExecutor{profiledExecutor: base}}
		}
		if _, codecs := child.(CodecProvider); codecs {
			if hasLogical {
				return logicalProfiledCodecScopedExecutor{profiledCodecScopedExecutor{profiledScopedExecutor{base}}}
			}
			return profiledCodecScopedExecutor{profiledScopedExecutor: profiledScopedExecutor{profiledExecutor: base}}
		}
		if hasLogical {
			return logicalProfiledScopedExecutor{profiledScopedExecutor{base}}
		}
		return profiledScopedExecutor{profiledExecutor: base}
	}
	if _, codecs := child.(CodecProvider); codecs {
		if hasLogical {
			return logicalProfiledCodecExecutor{profiledCodecExecutor{base}}
		}
		return profiledCodecExecutor{profiledExecutor: base}
	}
	if hasLogical {
		return logicalProfiledExecutor{base}
	}
	return base
}

func unsupportedScopeError() error {
	return planError("transaction_scope_unsupported", "scope", "executor does not support transaction scopes")
}

func atomicCleanupContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
}

// Within owns one transaction on a regular executor and one savepoint on a
// transaction executor. Cleanup runs with a detached thirty-second bound.
//
// `executor` and `fn` must not be nil.
func Within(ctx context.Context, executor Executor, opts *sql.TxOptions, fn Scope) (err error) {
	if executor == nil {
		return unsupportedScopeError()
	}
	if fn == nil {
		return fmt.Errorf("rasql: scope function must not be nil")
	}
	var child Executor
	var finalizer ScopeFinalizer
	state, _ := executor.(ScopeState)
	if state != nil && state.IsTransaction() {
		tx, ok := executor.(SavepointBeginner)
		if !ok {
			return unsupportedScopeError()
		}
		if opts != nil {
			return planError("nested_options", "scope", "nested scopes do not accept transaction options")
		}
		child, finalizer, err = tx.BeginSavepoint(ctx)
	} else if tx, ok := executor.(ScopeBeginner); ok {
		child, finalizer, err = tx.BeginScope(ctx, opts)
	} else {
		return unsupportedScopeError()
	}
	if err != nil {
		return err
	}
	if child == nil {
		return planError("transaction_scope_invalid", "scope", "scope beginner returned a nil child executor")
	}
	if finalizer == nil {
		return planError("transaction_scope_invalid", "scope", "scope beginner returned a nil finalizer")
	}
	callbackCtx := ctx
	if provider, ok := child.(scopeContextProvider); ok && provider.scopeContext() != nil {
		callbackCtx = provider.scopeContext()
	}
	result := runScopeCallback(callbackCtx, child, fn)
	cleanupCtx, cancel := atomicCleanupContext(callbackCtx)
	defer cancel()
	if result.panicked {
		if setter, ok := finalizer.(scopeCauseSetter); ok {
			setter.setCause(nil)
		}
		cleanupErr := finalizer.Rollback(cleanupCtx)
		if cleanupErr != nil {
			panic(AtomicPanic{Value: result.value, Cleanup: cleanupErr})
		}
		panic(result.value)
	}
	if result.err != nil {
		if setter, ok := finalizer.(scopeCauseSetter); ok {
			setter.setCause(result.err)
		}
		cleanupErr := finalizer.Rollback(cleanupCtx)
		return errors.Join(result.err, cleanupErr)
	}
	return finalizer.Commit(cleanupCtx)
}

type scopeCallbackResult struct {
	err      error
	panicked bool
	value    any
}

func runScopeCallback(ctx context.Context, executor Executor, fn Scope) (result scopeCallbackResult) {
	defer func() {
		if value := recover(); value != nil {
			result.panicked = true
			result.value = value
		}
	}()
	result.err = fn(ctx, executor)
	return result
}
