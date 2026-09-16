package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/stmt"
)

type Executor interface {
	Dialect() dialect.Dialect
	Query(context.Context, stmt.Statement) (ResultRows, error)
	Exec(context.Context, stmt.Statement) (sql.Result, error)
}

// ResultRows is what Executor.Query hands back, and it is also the interface
// a hand-written Executor must satisfy to run through Rows, All, One, Maybe,
// and Prepared. It owns exactly one query result and completes consumption
// of it exactly once, through Finish; Columns, Next, Close, Err, and
// RecordRow exist to drive that consumption, and Finish is what ends it
// regardless of how it ends.
type ResultRows interface {
	// ScanSource decodes the row Next most recently advanced to, in
	// database/sql's own Scan convention. An implementation that wants a
	// Scan failure folded into Finish's reported error must record it itself
	// when Scan returns one; nothing else does that on its behalf.
	ScanSource

	// Columns returns the result column names. An implementation that
	// reports its own errors through Finish should fold a Columns failure
	// into that accounting too, the same way it does for Scan and iteration
	// failures, rather than let it vanish once the caller has already moved
	// on to reading rows.
	Columns() ([]string, error)

	// Next advances to the next row and reports whether one is available. A
	// false result means rows are exhausted or iteration failed; either way
	// Next itself does not end consumption or close anything the caller can
	// rely on staying open, and Err distinguishes the two causes. Once Next
	// reports false it must keep reporting false, never reviving on a later
	// call.
	Next() bool

	// Close ends consumption early, before Next has reported exhaustion. It
	// must be safe to call more than once and after Next has already
	// exhausted the rows, and it must leave the row source in the same state
	// a Finish(nil, true) call would: an implementation typically defines one
	// in terms of the other so an early Close and a caller's own Finish call
	// never race to close the same result twice or report completion twice.
	Close() error

	// Err reports the error iteration recorded, or nil when the rows read so
	// far, and any exhaustion that ended them, produced none. It must answer
	// the same way before and after Finish, so a caller that already
	// finished consumption can still ask what stopped it.
	Err() error

	// RecordRow marks the row most recently read as successfully consumed,
	// separately from Scan succeeding: a caller that scans a row and then
	// rejects it, for a cardinality or a decoding failure only discovered
	// after Scan returns, must not call RecordRow for that row. An
	// implementation that reports a row count through its own completion
	// event counts only what RecordRow marked, so a caller that never calls
	// it reports a count of zero rather than one derived from Next or Scan.
	RecordRow()

	// Finish ends consumption exactly once; every call after the first
	// leaves the row source untouched and returns the same error the first
	// call computed. Call it whether rows were drained to exhaustion, closed
	// early, or abandoned because a decoder failed partway through: it is
	// the one place that closes the underlying result if nothing has closed
	// it yet, and the one place that reports this source's outcome to
	// whatever observes its completion.
	//
	// err is the terminal cause a caller already knows about, typically a
	// conversion or cardinality failure discovered after Scan already
	// returned successfully. earlyClose reports whether the caller stopped
	// before Next reported exhaustion; pass its true value, not always true
	// or always false, since a completion observer reports whichever value
	// it is given. An implementation returns err joined with whatever error
	// it accumulated internally while iterating, scanning, or closing, not
	// err alone, so an errors.Is or errors.As check against the err a caller
	// passed in still succeeds after the join.
	Finish(err error, earlyClose bool) error
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

// AsExecutor validates db and attaches profile's compiler to it, returning
// the same DB as an Executor. A DB bound to a transaction gets a fresh busy
// token, so a statement run on it while another is still in flight is
// rejected rather than racing the same *sql.Tx.
func AsExecutor(db DB, profile EngineProfile) (Executor, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	c, err := profile.queryCompiler(db.Dialect())
	if err != nil {
		return nil, err
	}
	db.profile = profile
	db.compiler = c
	db.busy = nil
	if db.IsTransaction() {
		db.busy = newExecutorBusy()
	}
	return db, nil
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
	// parameters lists every slot a Parameter fills, in placeholder order.
	// bound reports whether Prepared.Bind has supplied a value for it.
	// rowsPreparedRequired refuses to run while any is false.
	parameters []parameterSlot
}

type parameterSlot struct {
	index int // position in the statement's arguments
	id    bindplan.ID
	codec string
	bound bool
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
	var parameters []parameterSlot
	for i, slot := range slots {
		if _, err := codecFor(registry, slot.Codec); err != nil {
			if planErr, ok := err.(*PlanError); ok {
				planErr.Path = fmt.Sprintf("binds[%d].codec", i)
			}
			return result, err
		}
		if slot.Parameter {
			parameters = append(parameters, parameterSlot{index: i, id: slot.ID, codec: slot.Codec})
		}
	}
	statement, err := encodeStatement(statementCopy, slots, registry)
	if err != nil {
		return result, err
	}
	result.statement, result.schema, result.decoder, result.codecs, result.cardinality, result.emptyErr = statement, q.Schema(), q.Projection().Decoder(), registry, cardinality, q.resultRequirement.emptyErr
	result.parameters = parameters
	return result, nil
}

func rowsPrepared[R any](ctx context.Context, executor Executor, prepared preparedRows[R]) (iter.Seq2[R, error], error) {
	return rowsPreparedRequired(ctx, executor, prepared, Many)
}

func rowsPreparedRequired[R any](ctx context.Context, executor Executor, prepared preparedRows[R], consumer Cardinality) (iter.Seq2[R, error], error) {
	for _, slot := range prepared.parameters {
		if !slot.bound {
			return nil, &PlanError{Code: "parameter_unbound", Path: fmt.Sprintf("binds[%d]", slot.index), Detail: "parameter has no value; call Prepared.Bind before running"}
		}
	}
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
		source := &codecScanSource{source: owned, columns: expected, codecs: codecs}
		// Converted to the ScanSource interface once here rather than at each
		// DecodeRow call: DecodeRow's parameter is an interface, so passing the
		// concrete *codecScanSource directly would convert it to that interface
		// fresh on every row.
		var rowSource ScanSource = source
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
			if err := decoder.DecodeRow(rowSource, &value); err != nil {
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
// Prepare captures the argument values bound into q at the moment it is
// called, except for a value placed through Parameter: a Prepared carrying
// one or more unbound parameters refuses to run until Bind supplies a value
// for each, which is what lets one Prepare serve many different runs. What a
// Prepared amortizes either way is validation, lowering, rendering and codec
// lookup, not a Value-bound argument's value; call Prepare again to change
// one of those.
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
	// A plain comparison is sound here because CodecRegistry is sealed: every
	// registry is the one pointer type this package builds, which is always
	// comparable.
	if p.codecs != codecs {
		return &PlanError{Code: "prepared_executor_mismatch", Detail: "executor carries a different codec registry than Prepare captured"}
	}
	return nil
}

// Bind returns a copy of p in which each named parameter carries its value,
// encoded through the codec registry Prepare captured. p itself is
// unchanged, so a Prepared with unbound parameters can be shared and bound
// differently by every caller.
//
// It reports invalid_parameter for a value built from a zero Parameter,
// unknown_parameter for a value naming a parameter the query does not carry,
// and duplicate_parameter for two values naming the same parameter in one
// call. A value whose Parameter.Value could not snapshot its argument is
// returned as that unsnapshotable_bind error.
func (p Prepared[R]) Bind(values ...ParameterValue) (Prepared[R], error) {
	snapshots := make(map[bindplan.ID]any, len(values))
	for i, value := range values {
		if value.id == 0 {
			return Prepared[R]{}, planError("invalid_parameter", fmt.Sprintf("params[%d]", i), "parameter must be built with NewParameter or NewParameterWithCodec")
		}
		if value.err != nil {
			return Prepared[R]{}, value.err
		}
		known := false
		for _, slot := range p.prepared.parameters {
			if slot.id == value.id {
				known = true
				break
			}
		}
		if !known {
			return Prepared[R]{}, planError("unknown_parameter", fmt.Sprintf("params[%d]", i), "query has no parameter with this identity")
		}
		if _, duplicate := snapshots[value.id]; duplicate {
			return Prepared[R]{}, planError("duplicate_parameter", fmt.Sprintf("params[%d]", i), "parameter already given a value in this call")
		}
		snapshots[value.id] = value.snapshot
	}

	args := p.prepared.statement.Args()
	parameters := append([]parameterSlot(nil), p.prepared.parameters...)
	encoder := bindStatementEncoder{registry: p.codecs}
	for i, slot := range parameters {
		snapshot, ok := snapshots[slot.id]
		if !ok {
			continue
		}
		bound := snapshot
		if slot.codec != "" && snapshot != nil {
			encoded, err := encoder.EncodeBind(slot.index, slot.codec, snapshot)
			if err != nil {
				return Prepared[R]{}, err
			}
			bound = encoded
		}
		args[slot.index] = bound
		parameters[i].bound = true
	}

	prepared := p.prepared
	prepared.statement = stmt.New(p.prepared.statement.Text(), args...)
	prepared.parameters = parameters
	return Prepared[R]{compiler: p.compiler, codecs: p.codecs, prepared: prepared}, nil
}

// Rows runs p against executor and returns a fresh row sequence, consumable
// once like the sequence Rows(ctx, executor, q) returns.
func (p Prepared[R]) Rows(ctx context.Context, executor Executor) (iter.Seq2[R, error], error) {
	if err := p.checkExecutor(executor); err != nil {
		return nil, err
	}
	return rowsPrepared(ctx, executor, p.prepared)
}

// All runs p against executor and collects every row into a freshly
// allocated slice. A zero-row run returns a non-nil, empty slice, not nil.
// It returns nil on error.
//
// A caller that runs the same Prepared repeatedly and wants to reuse the
// slice's storage across calls should use AppendAll instead.
func (p Prepared[R]) All(ctx context.Context, executor Executor) ([]R, error) {
	values, err := p.AppendAll(ctx, make([]R, 0), executor)
	if err != nil {
		return nil, err
	}
	return values, nil
}

// AppendAll runs p against executor and appends every row to dst, returning
// the possibly-reallocated result, in the manner of the built-in append. A
// zero-row run returns dst unchanged. A failed run returns dst holding
// exactly the elements it held before the call, dropping any rows appended
// during the run, so the caller's own contents and capacity survive an
// error instead of being discarded.
//
// Passing a slice returned by an earlier AppendAll call, reset to length
// zero with dst[:0], lets repeated calls against the same Prepared reuse
// its storage instead of allocating a fresh slice each time, including
// after a call that failed.
func (p Prepared[R]) AppendAll(ctx context.Context, dst []R, executor Executor) ([]R, error) {
	rows, err := p.Rows(ctx, executor)
	if err != nil {
		return dst, err
	}
	return appendRows(dst, rows)
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

// All runs q against executor and collects every row into a freshly
// allocated slice. A zero-row run returns a non-nil, empty slice, not nil.
// It returns nil on error.
//
// A caller that runs the same query repeatedly and wants to reuse the
// slice's storage across calls should use AppendAll instead.
func All[R any](ctx context.Context, executor Executor, q Query[R]) ([]R, error) {
	values, err := AppendAll(ctx, make([]R, 0), executor, q)
	if err != nil {
		return nil, err
	}
	return values, nil
}

// AppendAll runs q against executor and appends every row to dst, returning
// the possibly-reallocated result, in the manner of the built-in append. A
// zero-row run returns dst unchanged. A failed run returns dst holding
// exactly the elements it held before the call, dropping any rows appended
// during the run, so the caller's own contents and capacity survive an
// error instead of being discarded.
//
// Passing a slice returned by an earlier AppendAll call, reset to length
// zero with dst[:0], lets repeated calls against the same query reuse its
// storage instead of allocating a fresh slice each time, including after a
// call that failed.
func AppendAll[R any](ctx context.Context, dst []R, executor Executor, q Query[R]) ([]R, error) {
	rows, err := Rows(ctx, executor, q)
	if err != nil {
		return dst, err
	}
	return appendRows(dst, rows)
}

// appendRows drains rows into dst and returns the possibly-reallocated
// result. On error it returns dst truncated back to the length it had on
// entry: append may already have reallocated dst's backing array, but
// growth always copies the caller's original elements into the new array
// first, so slicing back to the starting length recovers exactly those
// elements (and, incidentally, the larger capacity). All and AppendAll
// never hand back a partial result.
func appendRows[R any](dst []R, rows iter.Seq2[R, error]) ([]R, error) {
	start := len(dst)
	for value, err := range rows {
		if err != nil {
			return dst[:start], err
		}
		dst = append(dst, value)
	}
	return dst, nil
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
