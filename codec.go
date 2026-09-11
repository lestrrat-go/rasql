package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/stmt"
)

type CodecID string

type ValueCodec interface {
	Encode(any) (driver.Value, error)
	Decode(any, any) error
}

type CodecRegistry interface {
	Lookup(CodecID) (ValueCodec, bool)
}
type CodecProvider interface{ Codecs() CodecRegistry }

type codecRegistry struct{ codecs map[CodecID]ValueCodec }

func (r codecRegistry) Lookup(id CodecID) (ValueCodec, bool) { c, ok := r.codecs[id]; return c, ok }

var builtinCodecs CodecRegistry = codecRegistry{codecs: map[CodecID]ValueCodec{}}

func NewCodecRegistry(values map[CodecID]ValueCodec) (CodecRegistry, error) {
	copyValues := make(map[CodecID]ValueCodec, len(values))
	for id, codec := range values {
		if id == "" {
			return nil, fmt.Errorf("codec ID must not be empty")
		}
		if isNilCodec(codec) {
			return nil, fmt.Errorf("codec %q must not be nil", id)
		}
		if _, ok := copyValues[id]; ok {
			return nil, fmt.Errorf("duplicate codec %q", id)
		}
		copyValues[id] = codec
	}
	return codecRegistry{codecs: copyValues}, nil
}
func isNilCodec(codec ValueCodec) bool {
	if codec == nil {
		return true
	}
	v := reflect.ValueOf(codec)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

var (
	ErrUnexpectedNull = errors.New("unexpected SQL NULL")
)

type DecodeError struct {
	Column string
	Codec  CodecID
	Err    error
}

// Error names the cause only for a column that no codec decoded, where the
// cause is rasql's own conversion error and rasql controls what it says.
// A codec's own error text stays out of the message, because a codec is
// third-party code that may print the value it failed on; that text is still
// reachable through Unwrap, and TestCodecErrors/"codec cause text stays hidden" holds the
// line. Without the first case a caller sees only `decode column "total" with
// codec "" failed`, which names neither the type wanted nor the type received.
func (e *DecodeError) Error() string {
	if e.Codec == "" {
		return fmt.Sprintf("decode column %q failed: %v", e.Column, e.Err)
	}
	return fmt.Sprintf("decode column %q with codec %q failed", e.Column, e.Codec)
}
func (e *DecodeError) Unwrap() error { return e.Err }

type EncodeError struct {
	Index int
	Codec CodecID
	Err   error
}

func (e *EncodeError) Error() string {
	return fmt.Sprintf("encode bind %d with codec %q failed", e.Index, e.Codec)
}
func (e *EncodeError) Unwrap() error { return e.Err }

type codecExec struct {
	Executor
	codecs CodecRegistry
}

func (e codecExec) Codecs() CodecRegistry { return e.codecs }

type codecCompilerExec struct{ codecExec }

type logicalCodecExec struct{ codecExec }
type logicalCodecCompilerExec struct{ codecCompilerExec }

func (e logicalCodecExec) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapCodecExecutor(child, e.codecs), completion
}
func (e logicalCodecCompilerExec) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapCodecExecutor(child, e.codecs), completion
}

func (e codecCompilerExec) queryCompiler() *querycompile.Compiler {
	provider, _ := e.Executor.(compilerProvider)
	if provider == nil {
		return nil
	}
	return provider.queryCompiler()
}
func WithCodecs(executor Executor, codecs CodecRegistry) (Executor, error) {
	if isNilExecutor(executor) {
		return nil, fmt.Errorf("executor must not be nil")
	}
	if isNilRegistry(codecs) {
		return nil, fmt.Errorf("codec registry must not be nil")
	}
	return wrapCodecExecutor(executor, codecs), nil
}

func wrapCodecExecutor(executor Executor, codecs CodecRegistry) Executor {
	base := codecExec{Executor: executor, codecs: codecs}
	hasLogical := false
	if _, ok := executor.(logicalInvocationProvider); ok {
		hasLogical = true
	}
	_, compiler := executor.(compilerProvider)
	_, scope := executor.(transactionBeginner)
	_, evidence := executor.(executionDurabilityProvider)
	if scope {
		if compiler {
			if evidence {
				if hasLogical {
					return logicalCodecCompilerScopedEvidenceExecutor{codecCompilerScopedEvidenceExecutor{codecCompilerScopedExecutor{codecScopedExecutor{base}}}}
				}
				return codecCompilerScopedEvidenceExecutor{codecCompilerScopedExecutor: codecCompilerScopedExecutor{codecScopedExecutor: codecScopedExecutor{codecExec: base}}}
			}
			if hasLogical {
				return logicalCodecCompilerScopedExecutor{codecCompilerScopedExecutor{codecScopedExecutor{base}}}
			}
			return codecCompilerScopedExecutor{codecScopedExecutor: codecScopedExecutor{codecExec: base}}
		}
		if evidence {
			if hasLogical {
				return logicalCodecScopedEvidenceExecutor{codecScopedEvidenceExecutor{codecScopedExecutor{base}}}
			}
			return codecScopedEvidenceExecutor{codecScopedExecutor: codecScopedExecutor{codecExec: base}}
		}
		if hasLogical {
			return logicalCodecScopedExecutor{codecScopedExecutor{base}}
		}
		return codecScopedExecutor{codecExec: base}
	}
	if compiler {
		if hasLogical {
			return logicalCodecCompilerExec{codecCompilerExec{base}}
		}
		return codecCompilerExec{codecExec: base}
	}
	if hasLogical {
		return logicalCodecExec{base}
	}
	return base
}
func isNilRegistry(registry CodecRegistry) bool {
	if registry == nil {
		return true
	}
	v := reflect.ValueOf(registry)
	switch v.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return v.IsNil()
	}
	return false
}

func codecFor(reg CodecRegistry, id string) (ValueCodec, error) {
	if id == "" {
		return nil, nil
	}
	codec, ok := reg.Lookup(CodecID(id))
	if !ok {
		return nil, &PlanError{Code: "codec_unavailable", Path: "codec", Detail: id}
	}
	return codec, nil
}

func encodeStatement(statement stmt.Statement, slots []bindSlot, reg CodecRegistry) (stmt.Statement, error) {
	args := statement.BoundArgs()
	if len(args) != len(slots) {
		return stmt.Statement{}, &PlanError{Code: "bind_mismatch", Detail: "statement arguments and bind slots differ"}
	}
	for i, slot := range slots {
		codec, err := codecFor(reg, slot.Codec)
		if err != nil {
			return stmt.Statement{}, err
		}
		value := args[i]
		name := ""
		if named, ok := value.(sql.NamedArg); ok {
			name, value = named.Name, named.Value
		}
		if slot.PreEncoded {
			if err := validateDriverValue(value); err != nil {
				return stmt.Statement{}, &PlanError{Code: "internal_plan", Path: fmt.Sprintf("binds[%d]", i), Detail: err.Error()}
			}
			if b, ok := value.([]byte); ok {
				value = append([]byte(nil), b...)
			}
			if name != "" {
				args[i] = sql.Named(name, value)
			} else {
				args[i] = value
			}
			continue
		}
		if codec == nil {
			continue
		}
		if value == nil {
			if name != "" {
				args[i] = sql.Named(name, nil)
			} else {
				args[i] = nil
			}
			continue
		}
		encoded, err := codec.Encode(value)
		if err != nil {
			return stmt.Statement{}, &EncodeError{Index: i, Codec: CodecID(slot.Codec), Err: err}
		}
		if name != "" {
			args[i] = sql.Named(name, encoded)
		} else {
			args[i] = encoded
		}
	}
	return stmt.New(statement.Text(), args...), nil
}
