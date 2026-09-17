package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync/atomic"

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

var (
	_ logicalInvocationProvider = eventExecutor{}
	_ logicalInvocationProvider = DB{}
	_ scopeContextProvider      = DB{}
)

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
	counter   *atomic.Int64
	scopeCtx  context.Context
}

// observedLogicalCompletion closes the logical invocation a mutation batch or
// a graph query opened, exactly once, whichever eventExecutor or DB opened
// it: completeEvent needs nothing from either, so this carries no reference
// back to the executor that started it.
type observedLogicalCompletion struct {
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
	completeEvent(c.ctx, c.completion, c.event)
}

func (e eventExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	if len(e.observers) == 0 {
		return ctx, e.Executor, noLogicalInvocation
	}
	logicalID := nextEventID()
	callCtx, completion := e.start(ctx, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: kind, Phase: EventStart})
	child := e.childScope(e.Executor, callCtx, logicalID, &atomic.Int64{})
	return callCtx, child, &observedLogicalCompletion{
		ctx: callCtx, completion: completion,
		event: Event{LogicalID: logicalID, ParentID: e.parentID, Kind: kind, Phase: EventTerminal},
	}
}

// The observed executor exposes a transaction scope and a codec registry only
// when the executor it wraps has one, because both interfaces are exported and
// a caller reads their presence. The compiler and the durability evidence need
// no variant of their own: both are unexported, so executorCapability reaches
// them through unwrapExecutor.
type eventScopedExecutor struct{ eventExecutor }
type eventCodecExecutor struct{ eventExecutor }
type eventCodecScopedExecutor struct{ eventScopedExecutor }

// WithEventObservers wraps executor so every observer sees the start and the
// terminal event of each scope, statement and mutation batch it runs, and
// handler receives the extension errors those observers raise. It returns
// executor unchanged when observers is empty.
//
// A DB already carries event observers as fields, and a scope it opens mints
// its own event identity as it goes, so setting them is enough: nothing needs
// a wrapper around a DB to report events.
//
// `executor` must not be nil; `handler` must not be nil when any observer is
// given, and no element of `observers` may be nil.
func WithEventObservers(executor Executor, handler ExtensionErrorHandler, observers ...EventObserver) (Executor, error) {
	if executor == nil {
		return nil, fmt.Errorf("executor must not be nil")
	}
	if len(observers) > 0 && handler == nil {
		return nil, fmt.Errorf("rasql: extension error handler must not be nil when observers are supplied")
	}
	for _, observer := range observers {
		if observer == nil {
			return nil, fmt.Errorf("rasql: event observer must not be nil")
		}
	}
	if len(observers) == 0 {
		return executor, nil
	}
	copied := append([]EventObserver(nil), observers...)
	if db, ok := executor.(DB); ok {
		db.eventHandler = handler
		db.eventObservers = copied
		db.eventParentID = ""
		db.eventCounter = nil
		db.eventScopeCtx = nil
		db.eventScopeComplete = nil
		return db, nil
	}
	base := eventExecutor{Executor: executor, handler: handler, observers: copied, parentID: ""}
	return wrapEventExecutor(base), nil
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
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: unsupportedScopeError()})
		return nil, nil, unsupportedScopeError()
	}
	child, finalizer, err := beginner.BeginScope(callCtx, opts)
	if err != nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	if child == nil || finalizer == nil {
		err := planError("transaction_scope_invalid", "scope", "begin returned a nil child or finalizer")
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	return e.childScope(child, callCtx, logicalID, counter), &observedFinalizer{ScopeFinalizer: finalizer, finish: func(err error) {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
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
		err := unsupportedSavepointError()
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	child, finalizer, err := beginner.BeginSavepoint(callCtx)
	if err != nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	if child == nil || finalizer == nil {
		err := planError("transaction_scope_invalid", "scope", "begin returned a nil child or finalizer")
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	return e.childScope(child, callCtx, logicalID, counter), &observedFinalizer{ScopeFinalizer: finalizer, finish: func(err error) {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventScope, Phase: EventTerminal, Err: err})
	}}, nil
}

func (e eventScopedExecutor) IsTransaction() bool { return scopeStateFrom(e.Executor) }

func (e eventCodecExecutor) Codecs() CodecRegistry       { return codecsFrom(e.Executor) }
func (e eventCodecScopedExecutor) Codecs() CodecRegistry { return codecsFrom(e.Executor) }

func (e eventExecutor) childScope(child Executor, ctx context.Context, parentID string, counter *atomic.Int64) Executor {
	base := eventExecutor{Executor: child, handler: e.handler, observers: e.observers, parentID: parentID, counter: counter, scopeCtx: ctx}
	return wrapEventExecutor(base)
}

func (e eventExecutor) scopeContext() context.Context { return e.scopeCtx }
func (e eventExecutor) unwrapExecutor() Executor      { return e.Executor }

// childEventScope returns a copy of db that reports parentID as its own
// event identity from here on, sharing observers and handler with db but
// carrying ctx and counter as its own scope-local state. It mirrors what
// eventExecutor.childScope does for a wrapped executor, without needing a
// wrapper: a DB already carries every field childScope would otherwise have
// to reconstruct.
func (db DB) childEventScope(parentID string, ctx context.Context, counter *atomic.Int64) DB {
	db.eventParentID = parentID
	db.eventCounter = counter
	db.eventScopeCtx = ctx
	return db
}

// scopeContext satisfies scopeContextProvider, so Within runs a scope's
// callback under the ctx event observers derived for it, the same way it
// would for a wrapped executor. It is nil for a DB that never opened an
// observed scope, which Within already treats as no override.
func (db DB) scopeContext() context.Context { return db.eventScopeCtx }

// startEvent starts event against db's own observers and handler.
func (db DB) startEvent(ctx context.Context, event Event) (context.Context, EventCompletion) {
	return startEventObservers(ctx, db.eventObservers, db.eventHandler, event)
}

// beginLogicalInvocation satisfies logicalInvocationProvider unconditionally,
// the same way ScopeBeginner and CodecProvider do: a DB with no event
// observers reports back the same ctx and itself unchanged, exactly what a
// caller would see if it held the executor logicalInvocationProvider is
// missing from entirely.
func (db DB) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	if len(db.eventObservers) == 0 {
		return ctx, db, noLogicalInvocation
	}
	logicalID := nextEventID()
	callCtx, completion := db.startEvent(ctx, Event{LogicalID: logicalID, ParentID: db.eventParentID, Kind: kind, Phase: EventStart})
	child := db.childEventScope(logicalID, callCtx, &atomic.Int64{})
	return callCtx, child, &observedLogicalCompletion{
		ctx: callCtx, completion: completion,
		event: Event{LogicalID: logicalID, ParentID: db.eventParentID, Kind: kind, Phase: EventTerminal},
	}
}

// queryObserved is Query's event layer, added on top of queryGuarded.
func (db DB) queryObserved(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	logicalID := nextEventID()
	statementIndex := nextEventStatement(db.eventCounter)
	callCtx, completion := db.startEvent(ctx, Event{LogicalID: logicalID, ParentID: db.eventParentID, Kind: EventStatement, Phase: EventStart, StatementIndex: statementIndex})
	rows, err := db.queryGuarded(callCtx, statement)
	if err != nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: db.eventParentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Err: err})
		return nil, err
	}
	if rows == nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: db.eventParentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex})
		return nil, nil
	}
	return &eventRows{ResultRows: rows, finish: func(rowsRead int64, early bool, err error) {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: db.eventParentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Rows: rowsRead, EarlyClose: early, Err: err})
	}}, nil
}

// execObserved is Exec's event layer, added on top of execGuarded.
func (db DB) execObserved(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	logicalID := nextEventID()
	statementIndex := nextEventStatement(db.eventCounter)
	callCtx, completion := db.startEvent(ctx, Event{LogicalID: logicalID, ParentID: db.eventParentID, Kind: EventStatement, Phase: EventStart, StatementIndex: statementIndex})
	result, err := db.execGuarded(callCtx, statement)
	completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: db.eventParentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Err: err})
	return result, err
}

// eventScopeCompletion fires the EventScope terminal event Begin started,
// exactly once, from whichever of Commit or Rollback finishes the
// transaction first. It fires using the ctx and identity Begin captured
// rather than whatever ctx Commit or Rollback themselves were called with,
// the same way observedFinalizer does for BeginScope and BeginSavepoint.
type eventScopeCompletion struct {
	ctx        context.Context
	completion EventCompletion
	event      Event
	done       atomic.Bool
}

// finish is safe to call on a nil *eventScopeCompletion, which is what a DB
// Begin did not build one for carries: Commit and Rollback call it
// unconditionally rather than checking db.eventScopeComplete themselves.
func (c *eventScopeCompletion) finish(err error) {
	if c == nil {
		return
	}
	if !c.done.CompareAndSwap(false, true) {
		return
	}
	c.event.Err = err
	completeEvent(c.ctx, c.completion, c.event)
}

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
	_, codecs := base.Executor.(CodecProvider)
	_, scope := base.Executor.(ScopeBeginner)
	if scope {
		if codecs {
			return eventCodecScopedExecutor{eventScopedExecutor{base}}
		}
		return eventScopedExecutor{eventExecutor: base}
	}
	if codecs {
		return eventCodecExecutor{eventExecutor: base}
	}
	return base
}

func (e eventExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	logicalID := nextEventID()
	statementIndex := nextEventStatement(e.counter)
	callCtx, completion := e.start(ctx, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventStart, StatementIndex: statementIndex})
	rows, err := e.Executor.Query(callCtx, statement)
	if err != nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Err: err})
		return nil, err
	}
	if rows == nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex})
		return nil, nil
	}
	return &eventRows{ResultRows: rows, finish: func(rows int64, early bool, err error) {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Rows: rows, EarlyClose: early, Err: err})
	}}, nil
}

func (e eventExecutor) Exec(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	logicalID := nextEventID()
	statementIndex := nextEventStatement(e.counter)
	callCtx, completion := e.start(ctx, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventStart, StatementIndex: statementIndex})
	result, err := e.Executor.Exec(callCtx, statement)
	completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: e.parentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Err: err})
	return result, err
}

// nextEventStatement numbers a statement within counter's scope, or reports
// StatementIndex 0 for one that runs outside any scope at all, which is what
// a nil counter means for both eventExecutor and DB.
func nextEventStatement(counter *atomic.Int64) int {
	if counter == nil {
		return 0
	}
	return int(counter.Add(1) - 1)
}

func nextEventID() string { return fmt.Sprintf("rasql-%d", eventID.Add(1)) }

func (e eventExecutor) start(ctx context.Context, event Event) (context.Context, EventCompletion) {
	return startEventObservers(ctx, e.observers, e.handler, event)
}

// startEventObservers starts event against observers in registration order,
// derives the ctx each hands back in turn, and returns a completion that
// closes every completion an observer returned, in reverse order, when the
// event's terminal half is reported. eventExecutor and DB both carry their
// own observers and handler as fields and call this the same way.
func startEventObservers(ctx context.Context, observers []EventObserver, handler ExtensionErrorHandler, event Event) (context.Context, EventCompletion) {
	current := ctx
	var completions []EventCompletion
	for _, observer := range observers {
		derived, completion, err := safeEventStart(observer, current, event)
		if err != nil {
			reportEventError(current, handler, err)
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
				reportEventError(ctx, handler, err)
			}
		}
		return nil
	})
}

// completeEvent reports event's terminal half to completion, which is nil
// when every observer start returned no completion of its own.
func completeEvent(ctx context.Context, completion EventCompletion, event Event) {
	if completion != nil {
		_ = completion.Complete(ctx, event)
	}
}

func reportEventError(ctx context.Context, handler ExtensionErrorHandler, err error) {
	if handler != nil {
		func() {
			defer func() { _ = recover() }()
			handler.HandleExtensionError(ctx, ExtensionError{Errors: []error{err}})
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
