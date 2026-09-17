package rasql

import (
	"context"
	"database/sql"
	"sync/atomic"

	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/stmt"
)

// decoratedExecutor is the one decorator this package builds around a
// foreign Executor. WithCodecs, WithEngineProfile, and WithEventObservers
// each set the one field their own trait owns; wrapping the same executor
// with more than one of them nests a decoratedExecutor around a
// decoratedExecutor, the way wrapping it with more than one trait always did.
//
// It implements every optional interface a caller or this package's own
// Within and ExecMutationBatch look for, unconditionally: CodecProvider,
// ScopeBeginner, SavepointBeginner, ScopeState, compilerProvider,
// logicalInvocationProvider, scopeContextProvider. Each method answers from
// its own field first and falls back to the executor it wraps, so a single
// type assertion on any decoratedExecutor in a chain already reaches the
// right answer, the same one an equivalent chain of the old per-trait
// wrapper types would have. A decorator that always implements ScopeBeginner
// and SavepointBeginner is the one place this changes what a caller sees:
// Within and ExecMutationBatch used to read an executor's capability off a
// type assertion that could fail; now it always succeeds, and
// transaction_scope_unsupported or savepoint_unsupported comes back from a
// BeginScope or BeginSavepoint call that exists, instead.
type decoratedExecutor struct {
	Executor
	// compiler and codecs are set by WithEngineProfile and WithCodecs. Either
	// may be nil, in which case the corresponding method forwards to the
	// executor this decorator wraps instead of answering for itself.
	compiler *querycompile.Compiler
	codecs   CodecRegistry
	// eventHandler and eventObservers are set by WithEventObservers.
	// eventParentID, eventCounter, and eventScopeCtx are this decorator's own
	// event identity, minted fresh for the child BeginScope, BeginSavepoint,
	// and a logical invocation each return, the same way DB mints them for
	// its own child.
	eventHandler   ExtensionErrorHandler
	eventObservers []EventObserver
	eventParentID  string
	eventCounter   *atomic.Int64
	eventScopeCtx  context.Context
}

// wrapCodecExecutor wraps executor so its own codecs shadow whatever the
// executor it wraps reports, or the builtin registry when neither has one of
// its own. A DB already carries its own codec registry as a field, so
// setting it is enough: nothing needs a decorator around a DB to answer
// Codecs.
func wrapCodecExecutor(executor Executor, codecs CodecRegistry) Executor {
	if db, ok := executor.(DB); ok {
		db.codecs = codecs
		return db
	}
	return &decoratedExecutor{Executor: executor, codecs: codecs}
}

// wrapProfiledChild is wrapProfiledChildWithCodecs with no codecs of its own
// to shadow the child's.
func wrapProfiledChild(child Executor, compiler *querycompile.Compiler) Executor {
	return wrapProfiledChildWithCodecs(child, compiler, nil)
}

// wrapProfiledChildWithCodecs wraps child so compiler and codecs (when
// non-nil) shadow whatever child itself reports. A DB already carries both as
// fields, so setting them is enough. A child that needs neither trait set is
// handed back unwrapped: a decorator that would forward everything to the
// executor it wraps contributes nothing, so building one is pure overhead.
func wrapProfiledChildWithCodecs(child Executor, compiler *querycompile.Compiler, codecs CodecRegistry) Executor {
	if db, ok := child.(DB); ok {
		db.compiler = compiler
		if codecs != nil {
			db.codecs = codecs
		}
		return db
	}
	if compiler == nil && codecs == nil {
		return child
	}
	dec := &decoratedExecutor{Executor: child, compiler: compiler}
	if codecs != nil {
		dec.codecs = codecs
	}
	return dec
}

// Codecs satisfies CodecProvider. It reports d's own registry, or the one the
// executor it wraps reports if that executor is itself a CodecProvider
// (including one returning nil deliberately, which executorCodecs reports as
// codec_registry_unavailable), or the builtin registry when neither has one
// of its own.
func (d *decoratedExecutor) Codecs() CodecRegistry {
	if d.codecs != nil {
		return d.codecs
	}
	if provider, ok := d.Executor.(CodecProvider); ok {
		return provider.Codecs()
	}
	return builtinCodecs
}

// queryCompiler satisfies compilerProvider, forwarding to the executor d
// wraps when d carries no compiler of its own. The executor outside this
// package can never implement compilerProvider itself, since it is
// unexported, so the only executor this ever reaches is another
// decoratedExecutor or the point where the chain runs out.
func (d *decoratedExecutor) queryCompiler() *querycompile.Compiler {
	if d.compiler != nil {
		return d.compiler
	}
	if provider, ok := d.Executor.(compilerProvider); ok {
		return provider.queryCompiler()
	}
	return nil
}

// IsTransaction satisfies ScopeState by forwarding to the executor d wraps,
// which is the only place this answer ever comes from: WithCodecs,
// WithEngineProfile, and WithEventObservers configure no transaction state of
// their own.
func (d *decoratedExecutor) IsTransaction() bool { return scopeStateFrom(d.Executor) }

// scopeContext satisfies scopeContextProvider. d's own value is set only when
// d is itself the child a BeginScope, BeginSavepoint, or a logical invocation
// minted for an observed scope; otherwise it forwards, though the executor d
// wraps can never itself implement this unexported interface, so the forward
// only ever reaches another decoratedExecutor's own minted value or nothing.
func (d *decoratedExecutor) scopeContext() context.Context {
	if d.eventScopeCtx != nil {
		return d.eventScopeCtx
	}
	return scopeContextFrom(d.Executor)
}

// startEvent starts event against d's own observers and handler.
func (d *decoratedExecutor) startEvent(ctx context.Context, event Event) (context.Context, EventCompletion) {
	return startEventObservers(ctx, d.eventObservers, d.eventHandler, event)
}

// beginLogicalInvocation satisfies logicalInvocationProvider unconditionally.
// A decorator with no event observers of its own contributes nothing to the
// observation itself, but still has to reapply its own compiler and codecs to
// whatever child the forwarded call hands back, because that call has no way
// to know about a layer above it.
func (d *decoratedExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	if len(d.eventObservers) == 0 {
		callCtx, child, completion := beginLogicalFrom(d.Executor, ctx, kind)
		return callCtx, wrapProfiledChildWithCodecs(child, d.compiler, d.codecs), completion
	}
	logicalID := nextEventID()
	callCtx, completion := d.startEvent(ctx, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: kind, Phase: EventStart})
	child := &decoratedExecutor{
		Executor: d.Executor, compiler: d.compiler, codecs: d.codecs,
		eventHandler: d.eventHandler, eventObservers: d.eventObservers,
		eventParentID: logicalID, eventCounter: &atomic.Int64{}, eventScopeCtx: callCtx,
	}
	return callCtx, child, &observedLogicalCompletion{
		ctx: callCtx, completion: completion,
		event: Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: kind, Phase: EventTerminal},
	}
}

// BeginScope satisfies ScopeBeginner unconditionally. When the executor d
// wraps cannot open a scope, this reports transaction_scope_unsupported from
// this method rather than from a failed type assertion further up.
func (d *decoratedExecutor) BeginScope(ctx context.Context, opts *sql.TxOptions) (Executor, ScopeFinalizer, error) {
	if len(d.eventObservers) == 0 {
		child, finalizer, err := d.beginScopePlain(ctx, opts)
		if err != nil {
			return nil, nil, err
		}
		return child, finalizer, nil
	}
	logicalID := nextEventID()
	counter := d.eventCounter
	if counter == nil {
		counter = &atomic.Int64{}
	}
	callCtx, completion := d.startEvent(ctx, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventScope, Phase: EventStart})
	innerChild, finalizer, err := beginScopeFrom(d.Executor, callCtx, opts)
	if err == nil && (innerChild == nil || finalizer == nil) {
		err = planError("transaction_scope_invalid", "scope", "begin returned a nil child or finalizer")
	}
	if err != nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	child := &decoratedExecutor{
		Executor: innerChild, compiler: d.compiler, codecs: d.codecs,
		eventHandler: d.eventHandler, eventObservers: d.eventObservers,
		eventParentID: logicalID, eventCounter: counter, eventScopeCtx: callCtx,
	}
	return child, &observedFinalizer{ScopeFinalizer: finalizer, finish: func(err error) {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventScope, Phase: EventTerminal, Err: err})
	}}, nil
}

// beginScopePlain opens a scope on the executor d wraps and reapplies d's own
// compiler and codecs to the child, without the event layer BeginScope adds
// on top.
func (d *decoratedExecutor) beginScopePlain(ctx context.Context, opts *sql.TxOptions) (Executor, ScopeFinalizer, error) {
	child, finalizer, err := beginScopeFrom(d.Executor, ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	if child == nil || finalizer == nil {
		return nil, nil, planError("transaction_scope_invalid", "scope", "begin returned a nil child or finalizer")
	}
	return wrapProfiledChildWithCodecs(child, d.compiler, d.codecs), finalizer, nil
}

// BeginSavepoint satisfies SavepointBeginner unconditionally, the same way
// BeginScope satisfies ScopeBeginner: when the executor d wraps cannot open a
// savepoint, this reports savepoint_unsupported from this method rather than
// from a failed type assertion further up.
func (d *decoratedExecutor) BeginSavepoint(ctx context.Context) (Executor, ScopeFinalizer, error) {
	if len(d.eventObservers) == 0 {
		child, finalizer, err := d.beginSavepointPlain(ctx)
		if err != nil {
			return nil, nil, err
		}
		return child, finalizer, nil
	}
	logicalID := nextEventID()
	counter := d.eventCounter
	if counter == nil {
		counter = &atomic.Int64{}
	}
	callCtx, completion := d.startEvent(ctx, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventScope, Phase: EventStart})
	innerChild, finalizer, err := beginSavepointFrom(d.Executor, callCtx)
	if err == nil && (innerChild == nil || finalizer == nil) {
		err = planError("transaction_scope_invalid", "scope", "begin returned a nil child or finalizer")
	}
	if err != nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventScope, Phase: EventTerminal, Err: err})
		return nil, nil, err
	}
	child := &decoratedExecutor{
		Executor: innerChild, compiler: d.compiler, codecs: d.codecs,
		eventHandler: d.eventHandler, eventObservers: d.eventObservers,
		eventParentID: logicalID, eventCounter: counter, eventScopeCtx: callCtx,
	}
	return child, &observedFinalizer{ScopeFinalizer: finalizer, finish: func(err error) {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventScope, Phase: EventTerminal, Err: err})
	}}, nil
}

// beginSavepointPlain opens a savepoint on the executor d wraps and reapplies
// d's own compiler and codecs to the child, without the event layer
// BeginSavepoint adds on top.
func (d *decoratedExecutor) beginSavepointPlain(ctx context.Context) (Executor, ScopeFinalizer, error) {
	child, finalizer, err := beginSavepointFrom(d.Executor, ctx)
	if err != nil {
		return nil, nil, err
	}
	if child == nil || finalizer == nil {
		return nil, nil, planError("transaction_scope_invalid", "scope", "begin returned a nil child or finalizer")
	}
	return wrapProfiledChildWithCodecs(child, d.compiler, d.codecs), finalizer, nil
}

// Query satisfies Executor.Query. When d carries event observers, it reports
// an EventStatement around the call; otherwise it runs straight through to
// the executor d wraps.
func (d *decoratedExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	if len(d.eventObservers) == 0 {
		return d.Executor.Query(ctx, statement)
	}
	logicalID := nextEventID()
	statementIndex := nextEventStatement(d.eventCounter)
	callCtx, completion := d.startEvent(ctx, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventStatement, Phase: EventStart, StatementIndex: statementIndex})
	rows, err := d.Executor.Query(callCtx, statement)
	if err != nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Err: err})
		return nil, err
	}
	if rows == nil {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex})
		return nil, nil
	}
	return &eventRows{ResultRows: rows, finish: func(rowsRead int64, early bool, err error) {
		completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Rows: rowsRead, EarlyClose: early, Err: err})
	}}, nil
}

// Exec satisfies Executor.Exec. See Query for the event layer it shares.
func (d *decoratedExecutor) Exec(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	if len(d.eventObservers) == 0 {
		return d.Executor.Exec(ctx, statement)
	}
	logicalID := nextEventID()
	statementIndex := nextEventStatement(d.eventCounter)
	callCtx, completion := d.startEvent(ctx, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventStatement, Phase: EventStart, StatementIndex: statementIndex})
	result, err := d.Executor.Exec(callCtx, statement)
	completeEvent(callCtx, completion, Event{LogicalID: logicalID, ParentID: d.eventParentID, Kind: EventStatement, Phase: EventTerminal, StatementIndex: statementIndex, Err: err})
	return result, err
}
