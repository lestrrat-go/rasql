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
	statement stmt.Statement
	schema    ResultSchema
	decoder   RowDecoder[R]
	codecs    CodecRegistry
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
	args := compiled.statementCopy().BoundArgs()
	if len(slots) != len(args) {
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
	statement, err := encodeCompiled(compiled, registry)
	if err != nil {
		return result, err
	}
	result.statement, result.schema, result.decoder, result.codecs = statement, q.Schema(), q.Projection().Decoder(), registry
	return result, nil
}

func rowsPrepared[R any](ctx context.Context, executor Executor, prepared preparedRows[R]) (iter.Seq2[R, error], error) {
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
		if len(columns) != len(expected) {
			mismatch := &PlanError{Code: "result_columns_mismatch", Path: "result.columns", Detail: "column count differs from prepared schema"}
			finished := owned.Finish(mismatch, true)
			yield(zero, finished)
			return
		}
		for i, name := range columns {
			if name != expected[i].Name {
				mismatch := &PlanError{Code: "result_columns_mismatch", Path: fmt.Sprintf("result.columns[%d]", i), Detail: "column name differs from prepared schema"}
				finished := owned.Finish(mismatch, true)
				yield(zero, finished)
				return
			}
		}
		codecs := make([]ValueCodec, len(prepared.schema.Columns()))
		for i, column := range prepared.schema.Columns() {
			codecs[i], _ = codecFor(prepared.codecs, column.Codec)
		}
		source := codecScanSource{source: owned, columns: prepared.schema.Columns(), codecs: codecs}
		for owned.Next() {
			var value R
			if err := prepared.decoder.DecodeRow(source, &value); err != nil {
				finished := owned.Finish(err, true)
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
	terminal := &rowTerminal{cause: ErrNoRows}
	ctx = context.WithValue(ctx, rowTerminalKey{}, terminal)
	rows, err := Rows(ctx, executor, q)
	if err != nil {
		return zero, err
	}
	count := 0
	var result R
	for value, err := range rows {
		if err != nil {
			return zero, err
		}
		count++
		if count > 1 {
			terminal.cause = ErrMultipleRows
			return zero, ErrMultipleRows
		}
		result = value
	}
	if count == 0 {
		return zero, ErrNoRows
	}
	return result, nil
}
func Maybe[R any](ctx context.Context, executor Executor, q Query[R]) (R, bool, error) {
	var zero R
	terminal := &rowTerminal{}
	ctx = context.WithValue(ctx, rowTerminalKey{}, terminal)
	rows, err := Rows(ctx, executor, q)
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
		if count > 1 {
			terminal.cause = ErrMultipleRows
			return zero, false, ErrMultipleRows
		}
		result = value
	}
	return result, count == 1, nil
}
