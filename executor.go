package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"reflect"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querycompile"
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

type dbExecutor struct {
	db       DB
	compiler *querycompile.Compiler
}

func (e dbExecutor) Dialect() dialect.Dialect { return e.db.Dialect() }
func (e dbExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	return e.db.QueryOwned(ctx, statement)
}
func (e dbExecutor) Exec(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	return e.db.ExecRendered(ctx, statement)
}
func (e dbExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }

func AsExecutor(db DB, profile EngineProfile) (Executor, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	c, err := profile.queryCompiler(db.Dialect())
	if err != nil {
		return nil, err
	}
	return dbExecutor{db: db, compiler: c}, nil
}

type profiledExecutor struct {
	Executor
	compiler *querycompile.Compiler
}

func (e profiledExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }
func (e profiledExecutor) Codecs() CodecRegistry {
	provider, _ := e.Executor.(CodecProvider)
	if provider == nil {
		return builtinCodecs
	}
	registry := provider.Codecs()
	if isNilRegistry(registry) {
		return builtinCodecs
	}
	return registry
}
func WithEngineProfile(executor Executor, profile EngineProfile) (Executor, error) {
	if isNilExecutor(executor) {
		return nil, fmt.Errorf("executor must not be nil")
	}
	c, err := profile.queryCompiler(executor.Dialect())
	if err != nil {
		return nil, err
	}
	return profiledExecutor{Executor: executor, compiler: c}, nil
}
func isNilExecutor(e Executor) bool {
	if e == nil {
		return true
	}
	v := reflect.ValueOf(e)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
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

func prepareRows[R any](executor Executor, q Query[R], compiled compiledQuery) (preparedRows[R], error) {
	var result preparedRows[R]
	if isNilExecutor(executor) {
		return result, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	if err := q.Validate(); err != nil {
		return result, err
	}
	provider, ok := executor.(compilerProvider)
	if !ok {
		return result, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	compiler := provider.queryCompiler()
	if compiler == nil {
		return result, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	engine, cardinality, native := q.nativeInfo()
	if native {
		dialect := executor.Dialect()
		if dialect == nil || dialect.Name() != engine {
			return result, planError("engine_mismatch", "native.engine", "executor dialect does not match native SQL")
		}
	}
	registry := builtinCodecs
	if cp, ok := executor.(CodecProvider); ok {
		provided := cp.Codecs()
		if isNilRegistry(provided) {
			return result, &PlanError{Code: "codec_registry_unavailable", Detail: "executor returned a nil codec registry"}
		}
		registry = provided
	}
	columns := q.Schema().Columns()
	slots := compiled.BindSlots()
	statementCopy, err := compiled.statementCopy()
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
		if _, err := codecFor(registry, slot.codec); err != nil {
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
	if isNilExecutor(executor) {
		return nil, errors.New("executor must not be nil")
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
		source := codecScanSource{source: owned, columns: expected, codecs: codecs}
		policy := prepared.cardinality
		if consumer > policy {
			policy = consumer
		}
		if policy != Many {
			values := make([]R, 0, 2)
			for len(values) < 2 && owned.Next() {
				var value R
				if err := decoder.DecodeRow(source, &value); err != nil {
					finished := owned.Finish(err, true)
					yield(zero, finished)
					return
				}
				owned.RecordRow()
				values = append(values, value)
			}
			if err := owned.Err(); err != nil {
				finished := owned.Finish(err, false)
				yield(zero, finished)
				return
			}
			if len(values) > 1 {
				finished := owned.Finish(ErrMultipleRows, true)
				yield(zero, finished)
				return
			}
			if policy == ExactlyOne && len(values) == 0 {
				cause := prepared.emptyErr
				if cause == nil {
					cause = ErrNoRows
				}
				finished := owned.Finish(cause, false)
				yield(zero, finished)
				return
			}
			terminal, _ := ctx.Value(rowTerminalKey{}).(*rowTerminal)
			for _, value := range values {
				if terminal != nil {
					terminal.cause = nil
				}
				if !yield(value, nil) {
					cause := error(nil)
					if terminal != nil {
						cause = terminal.cause
					}
					_ = owned.Finish(cause, true)
					return
				}
			}
			if terminal != nil {
				terminal.cause = nil
			}
			if err := owned.Finish(nil, false); err != nil {
				yield(zero, err)
			}
			return
		}
		count := 0
		for owned.Next() {
			var value R
			if err := decoder.DecodeRow(source, &value); err != nil {
				finished := owned.Finish(err, true)
				yield(zero, finished)
				return
			}
			count++
			if policy != Many && count > 1 {
				finished := owned.Finish(ErrMultipleRows, true)
				yield(zero, finished)
				return
			}
			owned.RecordRow()
			terminal, _ := ctx.Value(rowTerminalKey{}).(*rowTerminal)
			if terminal != nil {
				terminal.cause = nil
			}
			if !yield(value, nil) {
				cause := error(nil)
				if terminal != nil {
					cause = terminal.cause
				}
				_ = owned.Finish(cause, true)
				return
			}
		}
		if err := owned.Err(); err != nil {
			finished := owned.Finish(err, false)
			yield(zero, finished)
			return
		}
		if policy == ExactlyOne && count == 0 {
			cause := prepared.emptyErr
			if cause == nil {
				cause = ErrNoRows
			}
			finished := owned.Finish(cause, false)
			yield(zero, finished)
			return
		}
		terminal, _ := ctx.Value(rowTerminalKey{}).(*rowTerminal)
		var terminalErr error
		if terminal != nil {
			terminalErr = terminal.cause
		}
		if err := owned.Finish(terminalErr, false); err != nil {
			yield(zero, err)
		}
	}, nil
}

func Rows[R any](ctx context.Context, executor Executor, q Query[R]) (iter.Seq2[R, error], error) {
	provider, ok := executor.(compilerProvider)
	if !ok {
		return nil, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	compiler := provider.queryCompiler()
	if compiler == nil {
		return nil, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	compiled, err := compileQuery(compiler, q)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareRows(executor, q, compiled)
	if err != nil {
		return nil, err
	}
	return rowsPrepared(ctx, executor, prepared)
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
	provider, ok := executor.(compilerProvider)
	if !ok {
		return nil, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	compiler := provider.queryCompiler()
	if compiler == nil {
		return nil, &PlanError{Code: "engine_profile_unavailable", Detail: "executor has no retained compiler"}
	}
	compiled, err := compileQuery(compiler, q)
	if err != nil {
		return nil, err
	}
	prepared, err := prepareRows(executor, q, compiled)
	if err != nil {
		return nil, err
	}
	return rowsPreparedRequired(ctx, executor, prepared, consumer)
}
