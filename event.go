package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/stmt"
)

// EventKind identifies the unit being observed.
type EventKind uint8

const (
	EventScope EventKind = iota + 1
	EventStatement
	EventMutationBatch
	EventGraph
	EventKindScope         = EventScope
	EventKindStatement     = EventStatement
	EventKindMutationBatch = EventMutationBatch
	EventKindGraph         = EventGraph
)

type logicalInvocationProvider interface {
	beginLogicalInvocation(context.Context, EventKind) (context.Context, Executor, logicalInvocationCompletion)
}

var _ logicalInvocationProvider = eventExecutor{}

type logicalInvocationCompletion interface {
	completeLogicalInvocation(error, int64, bool)
}

type noLogicalInvocationCompletion struct{}

func (noLogicalInvocationCompletion) completeLogicalInvocation(error, int64, bool) {}

var noLogicalInvocation noLogicalInvocationCompletion

func beginLogicalInvocation(ctx context.Context, executor Executor, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	provider, ok := executor.(logicalInvocationProvider)
	if !ok {
		return ctx, executor, noLogicalInvocation
	}
	return provider.beginLogicalInvocation(ctx, kind)
}

// EventPhase identifies the start or terminal half of an event.
type EventPhase uint8

const (
	EventStart EventPhase = iota + 1
	EventTerminal
	EventPhaseStart    = EventStart
	EventPhaseTerminal = EventTerminal
	EventPhaseComplete = EventTerminal
)

type Event struct {
	LogicalID      string
	ParentID       string
	Kind           EventKind
	Phase          EventPhase
	StatementIndex int
	Rows           int64
	EarlyClose     bool
	Err            error
}

type EventObserver interface {
	Start(context.Context, Event) (context.Context, EventCompletion)
}

// EventObserverFunc adapts a function to EventObserver.
type EventObserverFunc func(context.Context, Event) (context.Context, EventCompletion)

func (f EventObserverFunc) Start(ctx context.Context, event Event) (context.Context, EventCompletion) {
	if f == nil {
		return ctx, nil
	}
	return f(ctx, event)
}

type EventCompletion interface {
	Complete(context.Context, Event) error
}

// EventCompletionFunc adapts a function to EventCompletion.
type EventCompletionFunc func(context.Context, Event) error

func (f EventCompletionFunc) Complete(ctx context.Context, event Event) error {
	if f == nil {
		return nil
	}
	return f(ctx, event)
}

// EventObserverPanic preserves an observer panic while keeping it out of the
// application execution result.
type EventObserverPanic struct{ Value any }

func (e *EventObserverPanic) Error() string {
	return fmt.Sprintf("rasql: event observer panicked: %v", e.Value)
}
func (e *EventObserverPanic) Unwrap() error {
	err, _ := e.Value.(error)
	return err
}

type eventCompletionFunc func(context.Context, Event) error

func (f eventCompletionFunc) Complete(ctx context.Context, event Event) error { return f(ctx, event) }

var eventID atomic.Uint64

type eventExecutor struct {
	Executor
	handler   ExtensionErrorHandler
	observers []EventObserver
	parentID  string
	statement *atomic.Int64
	counter   *atomic.Int64
	scopeCtx  context.Context
}

type observedLogicalCompletion struct {
	e          eventExecutor
	ctx        context.Context
	completion EventCompletion
	event      Event
	done       atomic.Bool
}

func (c *observedLogicalCompletion) completeLogicalInvocation(err error, rows int64, early bool) {
	if !c.done.CompareAndSwap(false, true) {
		return
	}
	c.event.Err = err
	c.event.Rows = rows
	c.event.EarlyClose = early
	c.e.complete(c.ctx, c.completion, c.event)
}

func (e eventExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	if len(e.observers) == 0 {
		return ctx, e.Executor, noLogicalInvocation
	}
	logicalID := nextEventID()
	callCtx, completion := e.start(ctx, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: kind, Phase: EventStart})
	child := e.childScope(e.Executor, callCtx, logicalID, &atomic.Int64{})
	return callCtx, child, &observedLogicalCompletion{
		e: e, ctx: callCtx, completion: completion,
		event: Event{LogicalID: logicalID, ParentID: e.parentID, Kind: kind, Phase: EventTerminal},
	}
}

type eventScopedExecutor struct{ eventExecutor }
type eventCompilerExecutor struct{ eventExecutor }
type eventCodecExecutor struct{ eventExecutor }
type eventCompilerScopedExecutor struct{ eventScopedExecutor }
type eventCodecScopedExecutor struct{ eventScopedExecutor }
type eventCompilerCodecExecutor struct{ eventCodecExecutor }
type eventScopedEvidenceExecutor struct{ eventScopedExecutor }
type eventCompilerScopedEvidenceExecutor struct{ eventCompilerScopedExecutor }
type eventCodecScopedEvidenceExecutor struct{ eventCodecScopedExecutor }
type eventCompilerCodecScopedExecutor struct{ eventCompilerScopedExecutor }
type eventCompilerCodecScopedEvidenceExecutor struct {
	eventCompilerCodecScopedExecutor
}

func WithEventObservers(executor Executor, handler ExtensionErrorHandler, observers ...EventObserver) (Executor, error) {
	if isNilExecutor(executor) {
		return nil, fmt.Errorf("executor must not be nil")
	}
	if len(observers) > 0 && handler == nil {
		return nil, fmt.Errorf("rasql: extension error handler must not be nil when observers are supplied")
	}
	for _, observer := range observers {
		if observer == nil || isNilEventObserver(observer) {
			return nil, fmt.Errorf("rasql: event observer must not be nil")
		}
	}
	if len(observers) == 0 {
		return executor, nil
	}
	base := eventExecutor{Executor: executor, handler: handler, observers: append([]EventObserver(nil), observers...), parentID: "", statement: &atomic.Int64{}}
	return wrapEventExecutor(base), nil
}

func isNilEventObserver(observer EventObserver) bool {
	return observer == nil
}

func (e eventScopedExecutor) BeginScope(ctx context.Context, opts *sql.TxOptions) (Executor, ScopeFinalizer, error) {
	logicalID := nextEventID()
	counter := e.counter
	if counter == nil {
		counter = &atomic.Int64{}
	}
	callCtx, completion := e.start(ctx, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventStart})
	beginner, ok := e.Executor.(ScopeBeginner)
	if !ok {
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: unsupportedScopeError()})
		return nil, nil, unsupportedScopeError()
	}
	child, finalizer, err := beginner.BeginScope(callCtx, opts)
	if err != nil {
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	if isNilExecutor(child) || isNilScopeFinalizer(finalizer) {
		err := planError("transaction_scope_invalid", "scope", "begin returned a nil child or finalizer")
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	return e.childScope(child, callCtx, logicalID, counter), &observedFinalizer{ScopeFinalizer: finalizer, finish: func(err error) {
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
	}}, nil
}

func (e eventScopedExecutor) BeginSavepoint(ctx context.Context) (Executor, ScopeFinalizer, error) {
	logicalID := nextEventID()
	counter := e.counter
	if counter == nil {
		counter = &atomic.Int64{}
	}
	callCtx, completion := e.start(ctx, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventStart})
	beginner, ok := e.Executor.(SavepointBeginner)
	if !ok {
		err := planError("savepoint_unsupported", "scope", "executor does not support savepoints")
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, planError("savepoint_unsupported", "scope", "executor does not support savepoints")
	}
	child, finalizer, err := beginner.BeginSavepoint(callCtx)
	if err != nil {
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	if isNilExecutor(child) || isNilScopeFinalizer(finalizer) {
		err := planError("transaction_scope_invalid", "scope", "begin returned a nil child or finalizer")
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	return e.childScope(child, callCtx, logicalID, counter), &observedFinalizer{ScopeFinalizer: finalizer, finish: func(err error) {
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
	}}, nil
}

func (e eventScopedExecutor) IsTransaction() bool {
	state, ok := e.Executor.(ScopeState)
	return ok && state.IsTransaction()
}

func (e eventScopedEvidenceExecutor) executionDurability() executionDurabilityEvidence {
	provider, ok := e.Executor.(executionDurabilityProvider)
	if !ok {
		return executionDurabilityUnknown
	}
	return provider.executionDurability()
}
func (e eventCompilerExecutor) queryCompiler() *querycompile.Compiler {
	provider, _ := e.Executor.(compilerProvider)
	if provider == nil {
		return nil
	}
	return provider.queryCompiler()
}
func (e eventCompilerScopedExecutor) queryCompiler() *querycompile.Compiler {
	provider, _ := e.Executor.(compilerProvider)
	if provider == nil {
		return nil
	}
	return provider.queryCompiler()
}
func (e eventCodecExecutor) Codecs() CodecRegistry {
	provider, _ := e.Executor.(CodecProvider)
	if provider == nil {
		return nil
	}
	return provider.Codecs()
}
func (e eventCodecScopedExecutor) Codecs() CodecRegistry {
	provider, _ := e.Executor.(CodecProvider)
	if provider == nil {
		return nil
	}
	return provider.Codecs()
}
func (e eventCompilerCodecExecutor) queryCompiler() *querycompile.Compiler {
	provider, _ := e.Executor.(compilerProvider)
	if provider == nil {
		return nil
	}
	return provider.queryCompiler()
}
func (e eventCompilerCodecExecutor) Codecs() CodecRegistry {
	provider, _ := e.Executor.(CodecProvider)
	if provider == nil {
		return nil
	}
	return provider.Codecs()
}
func (e eventCompilerScopedEvidenceExecutor) executionDurability() executionDurabilityEvidence {
	provider, _ := e.Executor.(executionDurabilityProvider)
	if provider == nil {
		return executionDurabilityUnknown
	}
	return provider.executionDurability()
}
func (e eventCodecScopedEvidenceExecutor) executionDurability() executionDurabilityEvidence {
	provider, _ := e.Executor.(executionDurabilityProvider)
	if provider == nil {
		return executionDurabilityUnknown
	}
	return provider.executionDurability()
}

func (e eventCompilerCodecScopedEvidenceExecutor) executionDurability() executionDurabilityEvidence {
	provider, _ := e.Executor.(executionDurabilityProvider)
	if provider == nil {
		return executionDurabilityUnknown
	}
	return provider.executionDurability()
}
func (e eventCompilerCodecScopedExecutor) Codecs() CodecRegistry {
	provider, _ := e.Executor.(CodecProvider)
	if provider == nil {
		return nil
	}
	return provider.Codecs()
}

func (e eventExecutor) childScope(child Executor, ctx context.Context, parentID string, counter *atomic.Int64) Executor {
	base := eventExecutor{Executor: child, handler: e.handler, observers: e.observers, parentID: parentID, statement: e.statement, counter: counter, scopeCtx: ctx}
	return wrapEventExecutor(base)
}

func (e eventExecutor) scopeContext() context.Context { return e.scopeCtx }

type observedFinalizer struct {
	ScopeFinalizer
	finish func(error)
	cause  error
	done   atomic.Bool
}

func (f *observedFinalizer) setCause(err error) { f.cause = err }

func (f *observedFinalizer) Commit(ctx context.Context) error {
	err := f.ScopeFinalizer.Commit(ctx)
	if f.cause != nil {
		err = errors.Join(f.cause, err)
	}
	if f.done.CompareAndSwap(false, true) {
		f.finish(err)
	}
	return err
}
func (f *observedFinalizer) Rollback(ctx context.Context) error {
	err := f.ScopeFinalizer.Rollback(ctx)
	if f.cause != nil {
		err = errors.Join(f.cause, err)
	}
	if f.done.CompareAndSwap(false, true) {
		f.finish(err)
	}
	return err
}

func wrapEventExecutor(base eventExecutor) Executor {
	_, compiler := base.Executor.(compilerProvider)
	_, codecs := base.Executor.(CodecProvider)
	_, scope := base.Executor.(ScopeBeginner)
	_, evidence := base.Executor.(executionDurabilityProvider)
	if scope {
		if compiler && codecs && evidence {
			return eventCompilerCodecScopedEvidenceExecutor{eventCompilerCodecScopedExecutor{eventCompilerScopedExecutor{eventScopedExecutor{base}}}}
		}
		if compiler && codecs {
			return eventCompilerCodecScopedExecutor{eventCompilerScopedExecutor{eventScopedExecutor{base}}}
		}
		if compiler && evidence {
			return eventCompilerScopedEvidenceExecutor{eventCompilerScopedExecutor{eventScopedExecutor{base}}}
		}
		if codecs && evidence {
			return eventCodecScopedEvidenceExecutor{eventCodecScopedExecutor{eventScopedExecutor{base}}}
		}
		if compiler {
			return eventCompilerScopedExecutor{eventScopedExecutor{base}}
		}
		if codecs {
			return eventCodecScopedExecutor{eventScopedExecutor{base}}
		}
		if evidence {
			return eventScopedEvidenceExecutor{eventScopedExecutor{base}}
		}
		return eventScopedExecutor{eventExecutor: base}
	}
	if compiler && codecs {
		return eventCompilerCodecExecutor{eventCodecExecutor{base}}
	}
	if compiler {
		return eventCompilerExecutor{eventExecutor: base}
	}
	if codecs {
		return eventCodecExecutor{eventExecutor: base}
	}
	return base
}

func (e eventExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	logicalID := nextEventID()
	statementIndex := e.nextStatement()
	callCtx, completion := e.start(ctx, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventStart, StatementIndex: statementIndex})
	rows, err := e.Executor.Query(callCtx, statement)
	if err != nil {
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Err: err})
		return nil, err
	}
	if rows == nil {
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex})
		return nil, nil
	}
	return &eventRows{ResultRows: rows, finish: func(rows int64, early bool, err error) {
		e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Rows: rows, EarlyClose: early, Err: err})
	}}, nil
}

func (e eventExecutor) Exec(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	logicalID := nextEventID()
	statementIndex := e.nextStatement()
	callCtx, completion := e.start(ctx, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventStart, StatementIndex: statementIndex})
	result, err := e.Executor.Exec(callCtx, statement)
	e.complete(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Err: err})
	return result, err
}

func (e eventExecutor) nextStatement() int {
	if e.counter == nil {
		return 0
	}
	return int(e.counter.Add(1) - 1)
}

func nextEventID() string { return fmt.Sprintf("rasql-%d", eventID.Add(1)) }

func (e eventExecutor) start(ctx context.Context, event Event) (context.Context, EventCompletion) {
	current := ctx
	var completions []EventCompletion
	for _, observer := range e.observers {
		derived, completion, err := safeEventStart(observer, current, event)
		if err != nil {
			e.reportEventError(current, err)
		}
		if derived != nil {
			current = derived
		}
		if completion != nil {
			completions = append(completions, completion)
		}
	}
	return current, eventCompletionFunc(func(ctx context.Context, terminal Event) error {
		for index := len(completions) - 1; index >= 0; index-- {
			if err := safeEventComplete(completions[index], ctx, terminal); err != nil {
				e.reportEventError(ctx, err)
			}
		}
		return nil
	})
}

func (e eventExecutor) complete(ctx context.Context, completion EventCompletion, event Event) {
	if completion != nil {
		_ = completion.Complete(ctx, event)
	}
}

func (e eventExecutor) reportEventError(ctx context.Context, err error) {
	if e.handler != nil {
		func() {
			defer func() { _ = recover() }()
			e.handler.HandleExtensionError(ctx, ExtensionError{Errors: []error{err}})
		}()
	}
}

func safeEventStart(observer EventObserver, ctx context.Context, event Event) (derived context.Context, completion EventCompletion, err error) {
	defer func() {
		if value := recover(); value != nil {
			err = &EventObserverPanic{Value: value}
		}
	}()
	derived, completion = observer.Start(ctx, event)
	return derived, completion, nil
}

func safeEventComplete(completion EventCompletion, ctx context.Context, event Event) (err error) {
	defer func() {
		if value := recover(); value != nil {
			err = &EventObserverPanic{Value: value}
		}
	}()
	return completion.Complete(ctx, event)
}

type eventRows struct {
	ResultRows
	finish   func(int64, bool, error)
	rows     int64
	finished atomic.Bool
}

func (r *eventRows) RecordRow() { r.rows++; r.ResultRows.RecordRow() }
func (r *eventRows) Finish(err error, early bool) error {
	result := r.ResultRows.Finish(err, early)
	if r.finished.CompareAndSwap(false, true) {
		r.finish(r.rows, early, result)
	}
	return result
}
