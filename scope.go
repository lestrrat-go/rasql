package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/lestrrat-go/rasql/internal/querycompile"
)

// Scope is one owned transaction or savepoint callback.
type Scope func(context.Context, Executor) error

type transactionBeginner interface {
	beginScope(context.Context, *sql.TxOptions) (Executor, scopeFinalizer, error)
}

type savepointBeginner interface {
	beginSavepoint(context.Context) (Executor, scopeFinalizer, error)
}

type scopeFinalizer interface {
	Commit(context.Context) error
	Rollback(context.Context) error
}

type scopeState interface{ scopeIsTransaction() bool }

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
	_ transactionBeginner         = dbExecutor{}
	_ savepointBeginner           = dbExecutor{}
	_ scopeFinalizer              = guardedScopeFinalizer{}
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

func (e profiledScopedExecutor) scopeIsTransaction() bool {
	state, ok := e.Executor.(scopeState)
	return ok && state.scopeIsTransaction()
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

func (e profiledCodecScopedExecutor) beginScope(ctx context.Context, opts *sql.TxOptions) (Executor, scopeFinalizer, error) {
	beginner, ok := e.Executor.(transactionBeginner)
	if !ok {
		return nil, nil, unsupportedScopeError()
	}
	child, finalizer, err := beginner.beginScope(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	return wrapProfiledChildWithCodecs(child, e.compiler, e.Codecs()), finalizer, nil
}

func (e profiledCodecScopedExecutor) beginSavepoint(ctx context.Context) (Executor, scopeFinalizer, error) {
	beginner, ok := e.Executor.(savepointBeginner)
	if !ok {
		return nil, nil, planError("savepoint_unsupported", "scope", "executor does not support savepoints")
	}
	child, finalizer, err := beginner.beginSavepoint(ctx)
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

func (e codecScopedExecutor) scopeIsTransaction() bool {
	state, ok := e.Executor.(scopeState)
	return ok && state.scopeIsTransaction()
}
func (e codecScopedExecutor) scopeContext() context.Context {
	provider, _ := e.Executor.(scopeContextProvider)
	if provider == nil {
		return nil
	}
	return provider.scopeContext()
}

func (e codecScopedExecutor) beginScope(ctx context.Context, opts *sql.TxOptions) (Executor, scopeFinalizer, error) {
	beginner, ok := e.Executor.(transactionBeginner)
	if !ok {
		return nil, nil, unsupportedScopeError()
	}
	child, finalizer, err := beginner.beginScope(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	return wrapCodecExecutor(child, e.codecs), finalizer, nil
}

func (e codecScopedExecutor) beginSavepoint(ctx context.Context) (Executor, scopeFinalizer, error) {
	beginner, ok := e.Executor.(savepointBeginner)
	if !ok {
		return nil, nil, planError("savepoint_unsupported", "scope", "executor does not support savepoints")
	}
	child, finalizer, err := beginner.beginSavepoint(ctx)
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

func (e profiledScopedExecutor) beginScope(ctx context.Context, opts *sql.TxOptions) (Executor, scopeFinalizer, error) {
	beginner, ok := e.Executor.(transactionBeginner)
	if !ok {
		return nil, nil, unsupportedScopeError()
	}
	child, finalizer, err := beginner.beginScope(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	return wrapProfiledChild(child, e.compiler), finalizer, nil
}

func (e profiledScopedExecutor) beginSavepoint(ctx context.Context) (Executor, scopeFinalizer, error) {
	beginner, ok := e.Executor.(savepointBeginner)
	if !ok {
		return nil, nil, planError("savepoint_unsupported", "scope", "executor does not support savepoints")
	}
	child, finalizer, err := beginner.beginSavepoint(ctx)
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
	if _, scope := child.(transactionBeginner); scope {
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
func Within(ctx context.Context, executor Executor, opts *sql.TxOptions, fn Scope) (err error) {
	if isNilExecutor(executor) {
		return unsupportedScopeError()
	}
	if fn == nil {
		return fmt.Errorf("rasql: scope function must not be nil")
	}
	var child Executor
	var finalizer scopeFinalizer
	state, _ := executor.(scopeState)
	if state != nil && state.scopeIsTransaction() {
		tx, ok := executor.(savepointBeginner)
		if !ok {
			return unsupportedScopeError()
		}
		if opts != nil {
			return planError("nested_options", "scope", "nested scopes do not accept transaction options")
		}
		child, finalizer, err = tx.beginSavepoint(ctx)
	} else if tx, ok := executor.(transactionBeginner); ok {
		child, finalizer, err = tx.beginScope(ctx, opts)
	} else {
		return unsupportedScopeError()
	}
	if err != nil {
		return err
	}
	if isNilExecutor(child) {
		return planError("transaction_scope_invalid", "scope", "scope beginner returned a nil child executor")
	}
	if isNilScopeFinalizer(finalizer) {
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

func isNilScopeFinalizer(finalizer scopeFinalizer) bool {
	if finalizer == nil {
		return true
	}
	value := reflect.ValueOf(finalizer)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
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
