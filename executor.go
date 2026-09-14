package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"sync/atomic"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/exec"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/stmt"
)

type Executor interface {
	Dialect() dialect.Dialect
	Query(context.Context, stmt.Statement) (ResultRows, error)
	Exec(context.Context, stmt.Statement) (sql.Result, error)
}
type ResultRows interface {
	ScanSource
	Columns() ([]string, error)
	Next() bool
	Close() error
	Err() error
	RecordRow()
	Finish(error, bool) error
}
type compilerProvider interface{ queryCompiler() *querycompile.Compiler }
type returnedColumnBinder[R any] interface {
	bindReturnedColumns([]string) (RowDecoder[R], ResultSchema, error)
}

// executorUnwrapper hands back the executor a wrapper wraps, so that
// executorCapability can look past a wrapper that carries a capability without
// implementing it. Every wrapper this package builds implements it.
type executorUnwrapper interface{ unwrapExecutor() Executor }

// executorCapability finds the first executor in a wrapper chain that
// implements T, and reports whether it found one. It reads the value it is
// given before unwrapping, so a wrapper carrying its own value for a capability
// still shadows the one further in, the way profiledExecutor's compiler shadows
// the compiler of the executor it wraps. A provider found this way wins even
// when its method returns nil; every caller already treats a nil result the way
// it treats no provider at all, and walking on until a non-nil one turned up
// would change which executors report engine_profile_unavailable.
//
// Only compilerProvider and executionDurabilityProvider are looked up this way.
// Both are unexported, so no executor written outside this package implements
// one, and a chain holding neither reports false rather than a wrapper's
// stand-in value. That is what lets a wrapper stay one type instead of one type
// per combination of the capabilities its base happens to carry.
//
// Five capabilities are deliberately left out, and each wrapper still declares
// them only when the executor it wraps has one:
//
//   - CodecProvider and ScopeBeginner, SavepointBeginner and ScopeState are
//     exported, so a caller outside this package reads their presence off the
//     wrapper and steers on it, as Within and ExecMutationBatch do.
//   - logicalInvocationProvider must not be looked up past a layer, because
//     every layer rewraps the child executor the inner call hands back. A walk
//     that skipped layers would give a batch an unwrapped child and lose the
//     codec registry and the compiler the skipped layers carried.
//
// scopeContextProvider is left out for the same reason as the exported ones,
// stated where profiledScopedExecutor forwards it.
func executorCapability[T any](executor Executor) (T, bool) {
	for executor != nil {
		if value, ok := any(executor).(T); ok {
			return value, true
		}
		unwrapper, ok := executor.(executorUnwrapper)
		if !ok {
			break
		}
		executor = unwrapper.unwrapExecutor()
	}
	var zero T
	return zero, false
}

// The three functions below hold the body every wrapper repeats for a capability
// it declares only because the executor it wraps declares one. Each reads the
// executor one layer in and never unwraps further, for the reasons
// executorCapability's comment gives.

func scopeStateFrom(inner Executor) bool {
	state, ok := inner.(ScopeState)
	return ok && state.IsTransaction()
}

// A scope context stays forwarded rather than looked up, because Within reads
// it off the child a scope returned and takes the caller's context when the
// child reports none. A lookup that unwrapped would reach an observer's derived
// context through a child that carries no scope of its own, which is a
// different context than the one Within uses today.
func scopeContextFrom(inner Executor) context.Context {
	provider, _ := inner.(scopeContextProvider)
	if provider == nil {
		return nil
	}
	return provider.scopeContext()
}

// A wrapper reports what the executor it wraps reports, nil included, so that
// executorCodecs raises the one error rather than each wrapper deciding for
// itself. Substituting the builtin registry here used to hide a nil behind an
// unrelated fact, because WithEngineProfile picks profiledCodecExecutor for an
// executor that opens no scope and profiledCodecScopedExecutor for one that does.
func codecsFrom(inner Executor) CodecRegistry {
	provider, _ := inner.(CodecProvider)
	if provider == nil {
		return nil
	}
	return provider.Codecs()
}

type dbExecutor struct {
	db       DB
	compiler *querycompile.Compiler
	busy     *executorBusy
}

type executorBusy struct{ token chan struct{} }

func newExecutorBusy() *executorBusy { return &executorBusy{token: make(chan struct{}, 1)} }
func (b *executorBusy) acquire() bool {
	select {
	case b.token <- struct{}{}:
		return true
	default:
		return false
	}
}
func (b *executorBusy) release() { <-b.token }

func (e dbExecutor) IsTransaction() bool { return e.db.IsTransaction() }

func (e dbExecutor) Dialect() dialect.Dialect { return e.db.Dialect() }
func (e dbExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	if e.busy != nil && !e.busy.acquire() {
		return nil, planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	rows, err := e.db.QueryOwned(ctx, statement)
	if err != nil {
		if e.busy != nil {
			e.busy.release()
		}
		return nil, err
	}
	if rows == nil {
		if e.busy != nil {
			e.busy.release()
		}
		return nil, nil
	}
	if e.busy == nil {
		return rows, nil
	}
	return &exclusiveRows{ResultRows: rows, release: e.busy.release}, nil
}
func (e dbExecutor) Exec(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	if e.busy != nil && !e.busy.acquire() {
		return nil, planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	if e.busy != nil {
		defer e.busy.release()
	}
	return e.db.ExecRendered(ctx, statement)
}
func (e dbExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }

func (e dbExecutor) executionDurability() executionDurabilityEvidence {
	if !e.db.IsTransaction() {
		return executionDurabilityCommitted
	}
	return executionDurabilityPending
}

func (e dbExecutor) BeginScope(ctx context.Context, opts *sql.TxOptions) (Executor, ScopeFinalizer, error) {
	db, finalizer, err := e.db.BeginScope(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	child := dbExecutor{db: db, compiler: e.compiler, busy: newExecutorBusy()}
	return child, guardedScopeFinalizer{ScopeFinalizer: finalizer, busy: child.busy}, nil
}

func (e dbExecutor) BeginSavepoint(ctx context.Context) (Executor, ScopeFinalizer, error) {
	if e.busy != nil && !e.busy.acquire() {
		return nil, nil, planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	db, finalizer, err := e.db.BeginSavepoint(ctx)
	if e.busy != nil {
		e.busy.release()
	}
	if err != nil {
		var planErr *PlanError
		if errors.As(err, &planErr) {
			return nil, nil, err
		}
		return nil, nil, planError("savepoint_unsupported", "scope", err.Error())
	}
	child := dbExecutor{db: db, compiler: e.compiler, busy: e.busy}
	return child, guardedScopeFinalizer{ScopeFinalizer: finalizer, busy: e.busy}, nil
}

type guardedScopeFinalizer struct {
	exec.ScopeFinalizer
	busy *executorBusy
}

func (f guardedScopeFinalizer) Commit(ctx context.Context) error {
	if f.busy != nil && !f.busy.acquire() {
		return planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	if f.busy != nil {
		defer f.busy.release()
	}
	return f.ScopeFinalizer.Commit(ctx)
}
func (f guardedScopeFinalizer) Rollback(ctx context.Context) error {
	if f.busy != nil && !f.busy.acquire() {
		return planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	if f.busy != nil {
		defer f.busy.release()
	}
	return f.ScopeFinalizer.Rollback(ctx)
}

func AsExecutor(db DB, profile EngineProfile) (Executor, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	c, err := profile.queryCompiler(db.Dialect())
	if err != nil {
		return nil, err
	}
	var busy *executorBusy
	if db.IsTransaction() {
		busy = newExecutorBusy()
	}
	return dbExecutor{db: db, compiler: c, busy: busy}, nil
}

type exclusiveRows struct {
	ResultRows
	release  func()
	released atomic.Bool
}

func (r *exclusiveRows) Finish(err error, early bool) error {
	result := r.ResultRows.Finish(err, early)
	if r.released.CompareAndSwap(false, true) {
		r.release()
	}
	return result
}

type profiledExecutor struct {
	Executor
	compiler *querycompile.Compiler
}

func (e profiledExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }
func (e profiledExecutor) unwrapExecutor() Executor              { return e.Executor }

type profiledCodecExecutor struct{ profiledExecutor }

type logicalProfiledExecutor struct{ profiledExecutor }
type logicalProfiledCodecExecutor struct{ profiledCodecExecutor }

func beginLogicalFrom(executor Executor, ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	provider, ok := executor.(logicalInvocationProvider)
	if !ok {
		return ctx, executor, noLogicalInvocation
	}
	return provider.beginLogicalInvocation(ctx, kind)
}

func (e logicalProfiledExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapProfiledChild(child, e.compiler), completion
}
func (e logicalProfiledCodecExecutor) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapProfiledChildWithCodecs(child, e.compiler, e.Codecs()), completion
}

func (e profiledCodecExecutor) Codecs() CodecRegistry { return codecsFrom(e.Executor) }

// WithEngineProfile wraps executor so every statement it compiles is rendered
// for profile. It reports ErrInvalidEngineProfile when profile is zero and
// ErrEngineProfileMismatch when profile names a different engine than
// executor's dialect speaks.
//
// `executor` must not be nil.
func WithEngineProfile(executor Executor, profile EngineProfile) (Executor, error) {
	if executor == nil {
		return nil, fmt.Errorf("executor must not be nil")
	}
	c, err := profile.queryCompiler(executor.Dialect())
	if err != nil {
		return nil, err
	}
	return wrapProfiledChild(executor, c), nil
}

type preparedRows[R any] struct {
	statement   stmt.Statement
	schema      ResultSchema
	decoder     RowDecoder[R]
	codecs      CodecRegistry
	cardinality Cardinality
	emptyErr    error
}
type rowTerminal struct{ cause error }
type rowTerminalKey struct{}

// executorCompiler returns the compiler executor retains, reporting
// engine_profile_unavailable for one that retains none. Every caller that
// needs to know an executor is usable for compiling or matching against a
// prior compile goes through here, so the error stays the same wherever the
// check runs.
func executorCompiler(executor Executor) (*querycompile.Compiler, error) {
	provider, ok := executorCapability[compilerProvider](executor)
	if !ok {
		return nil, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	compiler := provider.queryCompiler()
	if compiler == nil {
		return nil, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	return compiler, nil
}

func prepareRows[R any](executor Executor, q Query[R], compiled compiledQuery) (preparedRows[R], error) {
	var result preparedRows[R]
	if err := q.Validate(); err != nil {
		return result, err
	}
	composed, err := lowerQuery(q)
	if err != nil {
		return result, err
	}
	return prepareRowsLowered(executor, q, compiled, composed)
}

// prepareRowsLowered prepares q against the lowered form a caller already
// produced for it. q MUST have passed Query.Validate, and composed MUST come
// from lowerQuery called on the same q.
func prepareRowsLowered[R any](executor Executor, q Query[R], compiled compiledQuery, composed query.ResultQuery) (preparedRows[R], error) {
	var result preparedRows[R]
	if _, err := executorCompiler(executor); err != nil {
		return result, err
	}
	engine, cardinality, native := q.nativeInfo()
	if native {
		dialect := executor.Dialect()
		if dialect == nil {
			return result, planError("engine_mismatch", "native.engine", "executor dialect is unavailable")
		}
		if dialect.Name() != engine {
			return result, planError("engine_mismatch", "native.engine", "executor dialect does not match native SQL")
		}
	} else if q.plan.mutation == nil {
		engines := querycompile.NativeEngines(composed)
		for _, required := range engines {
			if engine != "" && required != engine {
				return result, planError("engine_mismatch", "native.engine", "composed native SQL uses multiple engines")
			}
			engine = required
		}
		if engine != "" {
			dialect := executor.Dialect()
			if dialect == nil || dialect.Name() != engine {
				return result, planError("engine_mismatch", "native.engine", "executor dialect does not match native SQL")
			}
		}
	}
	registry, err := executorCodecs(executor)
	if err != nil {
		return result, err
	}
	columns := q.Schema().Columns()
	slots := append([]bindSlot(nil), compiled.Slots...)
	statementCopy, err := compiled.Copy()
	if err != nil {
		return result, err
	}
	if len(slots) != len(statementCopy.BoundArgs()) {
		return result, &PlanError{Code: "bind_mismatch", Detail: "statement arguments and bind slots differ"}
	}
	for i, column := range columns {
		if _, err := codecFor(registry, column.Codec); err != nil {
			if planErr, ok := err.(*PlanError); ok {
				planErr.Path = fmt.Sprintf("result.columns[%d].codec", i)
			}
			return result, err
		}
		_ = i
	}
	for i, slot := range slots {
		if _, err := codecFor(registry, slot.Codec); err != nil {
			if planErr, ok := err.(*PlanError); ok {
				planErr.Path = fmt.Sprintf("binds[%d].codec", i)
			}
			return result, err
		}
		_ = i
	}
	statement, err := encodeStatement(statementCopy, slots, registry)
	if err != nil {
		return result, err
	}
	result.statement, result.schema, result.decoder, result.codecs, result.cardinality, result.emptyErr = statement, q.Schema(), q.Projection().Decoder(), registry, cardinality, q.resultRequirement.emptyErr
	return result, nil
}

func rowsPrepared[R any](ctx context.Context, executor Executor, prepared preparedRows[R]) (iter.Seq2[R, error], error) {
	return rowsPreparedRequired(ctx, executor, prepared, Many)
}

func rowsPreparedRequired[R any](ctx context.Context, executor Executor, prepared preparedRows[R], consumer Cardinality) (iter.Seq2[R, error], error) {
	used := false
	return func(yield func(R, error) bool) {
		if used {
			var zero R
			yield(zero, fmt.Errorf("rows sequence may only be consumed once"))
			return
		}
		used = true
		owned, err := executor.Query(ctx, prepared.statement)
		var zero R
		if err != nil {
			yield(zero, err)
			return
		}
		if owned == nil {
			yield(zero, fmt.Errorf("executor returned nil rows"))
			return
		}
		columns, err := owned.Columns()
		if err != nil {
			finished := owned.Finish(err, true)
			yield(zero, finished)
			return
		}
		expected := prepared.schema.Columns()
		decoder := prepared.decoder
		bound := false
		if binder, ok := any(decoder).(returnedColumnBinder[R]); ok {
			boundDecoder, boundSchema, bindErr := binder.bindReturnedColumns(columns)
			if bindErr != nil {
				finished := owned.Finish(bindErr, true)
				yield(zero, finished)
				return
			}
			decoder, expected = boundDecoder, boundSchema.Columns()
			bound = true
		}
		if len(columns) != len(expected) {
			mismatch := &PlanError{Code: "result_columns_mismatch", Path: "result.columns", Detail: "column count differs from prepared schema"}
			finished := owned.Finish(mismatch, true)
			yield(zero, finished)
			return
		}
		for i, name := range columns {
			if bound {
				break
			}
			if name != expected[i].Name {
				mismatch := &PlanError{Code: "result_columns_mismatch", Path: fmt.Sprintf("result.columns[%d]", i), Detail: "column name differs from prepared schema"}
				finished := owned.Finish(mismatch, true)
				yield(zero, finished)
				return
			}
		}
		codecs := make([]ValueCodec, len(expected))
		for i, column := range expected {
			codecs[i], _ = codecFor(prepared.codecs, column.Codec)
		}
		source := codecScanSource{source: owned, columns: expected, codecs: codecs}
		policy := prepared.cardinality
		if consumer > policy {
			policy = consumer
		}
		terminal, _ := ctx.Value(rowTerminalKey{}).(*rowTerminal)
		// emit hands one row to the consumer. A false report means the consumer stopped early
		// and consumption is already complete under whatever cause the consumer recorded, so
		// the loop has nothing left to do but return.
		emit := func(value R) bool {
			if terminal != nil {
				terminal.cause = nil
			}
			if yield(value, nil) {
				return true
			}
			var cause error
			if terminal != nil {
				cause = terminal.cause
			}
			_ = owned.Finish(cause, true)
			return false
		}
		// A single-row policy holds its row back and reads once more before yielding anything, so
		// a second row raises ErrMultipleRows while the consumer has still seen no row at all.
		// Many yields each row as it decodes.
		singleRow := policy != Many
		var held R
		holding := false
		for owned.Next() {
			var value R
			if err := decoder.DecodeRow(source, &value); err != nil {
				finished := owned.Finish(err, true)
				yield(zero, finished)
				return
			}
			if !singleRow {
				owned.RecordRow()
				if !emit(value) {
					return
				}
				continue
			}
			if holding {
				finished := owned.Finish(ErrMultipleRows, true)
				yield(zero, finished)
				return
			}
			owned.RecordRow()
			held, holding = value, true
		}
		if err := owned.Err(); err != nil {
			finished := owned.Finish(err, false)
			yield(zero, finished)
			return
		}
		if policy == ExactlyOne && !holding {
			cause := prepared.emptyErr
			if cause == nil {
				cause = ErrNoRows
			}
			finished := owned.Finish(cause, false)
			yield(zero, finished)
			return
		}
		if holding && !emit(held) {
			return
		}
		var terminalErr error
		if terminal != nil {
			terminalErr = terminal.cause
		}
		if err := owned.Finish(terminalErr, false); err != nil {
			yield(zero, err)
		}
	}, nil
}

// compileAndPrepare compiles q and prepares it against executor, validating and
// lowering q once for both steps, and returns the compiler it resolved
// alongside the compiled and prepared forms. A caller that compiles one query
// and prepares a different one, as the graph loader does, calls compileQuery
// and prepareRows separately instead.
func compileAndPrepare[R any](executor Executor, q Query[R]) (compiledQuery, preparedRows[R], *querycompile.Compiler, error) {
	var prepared preparedRows[R]
	compiler, err := executorCompiler(executor)
	if err != nil {
		return compiledQuery{}, prepared, nil, err
	}
	if err := q.Validate(); err != nil {
		return compiledQuery{}, prepared, nil, mapCompileError(err)
	}
	composed, err := lowerQuery(q)
	if err != nil {
		return compiledQuery{}, prepared, nil, err
	}
	compiled, err := compileQueryLowered(compiler, q, composed)
	if err != nil {
		return compiledQuery{}, prepared, nil, err
	}
	prepared, err = prepareRowsLowered(executor, q, compiled, composed)
	if err != nil {
		return compiledQuery{}, prepared, nil, err
	}
	return compiled, prepared, compiler, nil
}

// Prepared holds a query that has already been validated, lowered, rendered
// to SQL and resolved against a codec registry, so that Rows, All, One and
// Maybe below repeat none of that work across many runs of the same query.
// Prepare captures the argument VALUES bound into q at the moment it is
// called: nothing in rasql's query API names a placeholder, so a Prepared
// cannot be re-bound to different arguments before a later run. Call Prepare
// again to change the arguments; what a Prepared amortizes is validation,
// lowering, rendering and codec lookup, not the bound values themselves.
//
// A Prepared's SQL is rendered for the dialect the executor passed to Prepare
// spoke through its compiler, and its bind values are already encoded, and
// its rows already decode, through the codec registry that executor carried
// at that moment. Its Rows, All, One and Maybe methods take an executor
// argument again on every call, so the wrapper around it can change (entering
// a transaction scope, for instance) without re-preparing, but each checks
// that the executor it is given still carries the same compiler AND the same
// codec registry Prepare captured, and reports a prepared_executor_mismatch
// PlanError instead of silently running with a stale registry or SQL rendered
// for a different dialect. Wrapping the executor with WithCodecs after
// Prepare, in particular, means calling Prepare again rather than reusing the
// old Prepared: nothing re-reads the executor's registry per run, so the new
// codecs would otherwise never take effect.
//
// A Prepared is never mutated after Prepare returns, so it is safe to store
// and execute from multiple goroutines, and every call to Rows returns a
// fresh sequence: the "may only be consumed once" rule applies to each
// sequence Rows hands back, not to the Prepared it came from.
type Prepared[R any] struct {
	compiler *querycompile.Compiler
	codecs   CodecRegistry
	prepared preparedRows[R]
}

// Prepare validates, lowers, renders and resolves codecs for q against
// executor once, returning a Prepared[R] whose Rows, All, One and Maybe
// methods run that same work repeatedly with none of the cost. See the
// [Prepared] doc comment for what stays fixed and what a later call may vary.
//
// `executor` must not be nil.
func Prepare[R any](executor Executor, q Query[R]) (Prepared[R], error) {
	_, prepared, compiler, err := compileAndPrepare(executor, q)
	if err != nil {
		return Prepared[R]{}, err
	}
	return Prepared[R]{compiler: compiler, codecs: prepared.codecs, prepared: prepared}, nil
}

// checkExecutor reports whether executor still carries the compiler AND the
// codec registry Prepare captured, so a Prepared[R] run against a different
// executor, or the same executor rewrapped with WithCodecs, fails with a
// clear error instead of sending SQL rendered for a different dialect or
// silently decoding through a registry the caller no longer intends.
func (p Prepared[R]) checkExecutor(executor Executor) error {
	compiler, err := executorCompiler(executor)
	if err != nil {
		return err
	}
	if compiler != p.compiler {
		return &PlanError{Code: "prepared_executor_mismatch", Detail: "executor does not match the executor Prepare was called with"}
	}
	codecs, err := executorCodecs(executor)
	if err != nil {
		return err
	}
	if !codecRegistrySame(p.codecs, codecs) {
		return &PlanError{Code: "prepared_executor_mismatch", Detail: "executor carries a different codec registry than Prepare captured"}
	}
	return nil
}

// Rows runs p against executor and returns a fresh row sequence, consumable
// once like the sequence Rows(ctx, executor, q) returns.
func (p Prepared[R]) Rows(ctx context.Context, executor Executor) (iter.Seq2[R, error], error) {
	if err := p.checkExecutor(executor); err != nil {
		return nil, err
	}
	return rowsPrepared(ctx, executor, p.prepared)
}

// All runs p against executor and collects every row.
func (p Prepared[R]) All(ctx context.Context, executor Executor) ([]R, error) {
	rows, err := p.Rows(ctx, executor)
	if err != nil {
		return nil, err
	}
	values := make([]R, 0)
	for value, err := range rows {
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}

// One runs p against executor and returns its single row, reporting
// ErrNoRows or ErrMultipleRows when the run does not produce exactly one.
func (p Prepared[R]) One(ctx context.Context, executor Executor) (R, error) {
	var zero R
	if err := p.checkExecutor(executor); err != nil {
		return zero, err
	}
	rows, err := rowsPreparedRequired(ctx, executor, p.prepared, ExactlyOne)
	if err != nil {
		return zero, err
	}
	var result R
	for value, err := range rows {
		if err != nil {
			return zero, err
		}
		result = value
	}
	return result, nil
}

// Maybe runs p against executor and returns its row and true when the run
// produces exactly one, or the zero value and false when it produces none.
// It reports ErrMultipleRows when the run produces more than one.
func (p Prepared[R]) Maybe(ctx context.Context, executor Executor) (R, bool, error) {
	var zero R
	if err := p.checkExecutor(executor); err != nil {
		return zero, false, err
	}
	rows, err := rowsPreparedRequired(ctx, executor, p.prepared, AtMostOne)
	if err != nil {
		return zero, false, err
	}
	count := 0
	var result R
	for value, err := range rows {
		if err != nil {
			return zero, false, err
		}
		count++
		result = value
	}
	return result, count == 1, nil
}

func Rows[R any](ctx context.Context, executor Executor, q Query[R]) (iter.Seq2[R, error], error) {
	prepared, err := Prepare(executor, q)
	if err != nil {
		return nil, err
	}
	return rowsPrepared(ctx, executor, prepared.prepared)
}
func All[R any](ctx context.Context, executor Executor, q Query[R]) ([]R, error) {
	rows, err := Rows(ctx, executor, q)
	if err != nil {
		return nil, err
	}
	values := make([]R, 0)
	for value, err := range rows {
		if err != nil {
			return nil, err
		}
		values = append(values, value)
	}
	return values, nil
}
func One[R any](ctx context.Context, executor Executor, q Query[R]) (R, error) {
	var zero R
	rows, err := rowsFor(ctx, executor, q, ExactlyOne)
	if err != nil {
		return zero, err
	}
	var result R
	for value, err := range rows {
		if err != nil {
			return zero, err
		}
		result = value
	}
	return result, nil
}
func Maybe[R any](ctx context.Context, executor Executor, q Query[R]) (R, bool, error) {
	var zero R
	rows, err := rowsFor(ctx, executor, q, AtMostOne)
	if err != nil {
		return zero, false, err
	}
	count := 0
	var result R
	for value, err := range rows {
		if err != nil {
			return zero, false, err
		}
		count++
		result = value
	}
	return result, count == 1, nil
}

func rowsFor[R any](ctx context.Context, executor Executor, q Query[R], consumer Cardinality) (iter.Seq2[R, error], error) {
	prepared, err := Prepare(executor, q)
	if err != nil {
		return nil, err
	}
	return rowsPreparedRequired(ctx, executor, prepared.prepared, consumer)
}
