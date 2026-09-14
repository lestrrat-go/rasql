package rasql

import (
	"context"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/bindplan"
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

// codecRegistry is stored and handed out as a pointer, not a value, purely so
// that two CodecRegistry interface values holding it are safe and meaningful
// to compare with ==: a map field makes the value type uncomparable (Go
// panics comparing two interface values whose dynamic type holds one), while
// a pointer is always comparable and, unlike a value copy, actually reports
// whether two registries are the same one. Prepared.checkExecutor relies on
// this to tell a caller's WithCodecs swap from a harmless executor rewrap;
// see codecRegistrySame.
type codecRegistry struct{ codecs map[CodecID]ValueCodec }

func (r *codecRegistry) Lookup(id CodecID) (ValueCodec, bool) { c, ok := r.codecs[id]; return c, ok }

var builtinCodecs CodecRegistry = &codecRegistry{codecs: map[CodecID]ValueCodec{}}

// NewCodecRegistry copies values into a registry that a codec lookup reads by
// ID. It reports an error for an empty ID.
//
// No value in `values` may be nil.
func NewCodecRegistry(values map[CodecID]ValueCodec) (CodecRegistry, error) {
	copyValues := make(map[CodecID]ValueCodec, len(values))
	for id, codec := range values {
		if id == "" {
			return nil, fmt.Errorf("codec ID must not be empty")
		}
		if codec == nil {
			return nil, fmt.Errorf("codec %q must not be nil", id)
		}
		if _, ok := copyValues[id]; ok {
			return nil, fmt.Errorf("duplicate codec %q", id)
		}
		copyValues[id] = codec
	}
	return &codecRegistry{codecs: copyValues}, nil
}

// codecRegistrySame reports whether a and b are the same codec registry, the
// way Prepared.checkExecutor compares a compiler: by identity, not by
// decoding the same values. rasql's own CodecRegistry is a pointer for
// exactly this reason (see the comment on codecRegistry), so the common case
// resolves with a plain, panic-free ==. CodecRegistry is a public interface
// though, and a caller may implement it on a value type whose fields (a
// slice, another map) make it uncomparable; comparing two interface values
// of an identical uncomparable dynamic type panics, so reflect.Value.
// Comparable checks that before Equal ever runs. When the type turns out not
// to be comparable, this reports a match rather than risk the panic or
// refuse every run against that registry: it cannot prove the two differ,
// and it already cannot prove rasql's own registries differ any other way
// than by identity.
func codecRegistrySame(a, b CodecRegistry) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	av, bv := reflect.ValueOf(a), reflect.ValueOf(b)
	if av.Type() != bv.Type() {
		return false
	}
	if !av.Comparable() {
		return true
	}
	return av.Equal(bv)
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

func (e codecExec) Codecs() CodecRegistry    { return e.codecs }
func (e codecExec) unwrapExecutor() Executor { return e.Executor }

// The codec executor exposes a transaction scope only when the executor it
// wraps has one, and forwards a logical invocation only when the executor it
// wraps opens one, because each layer has to rewrap the child that invocation
// returns. The compiler and the durability evidence need no variant of their
// own: both are unexported, so executorCapability reaches them through
// unwrapExecutor.
type logicalCodecExec struct{ codecExec }

func (e logicalCodecExec) beginLogicalInvocation(ctx context.Context, kind EventKind) (context.Context, Executor, logicalInvocationCompletion) {
	callCtx, child, completion := beginLogicalFrom(e.Executor, ctx, kind)
	return callCtx, wrapCodecExecutor(child, e.codecs), completion
}

// WithCodecs wraps executor so every value it binds and every column it decodes
// passes through codecs.
//
// `executor` and `codecs` must not be nil.
func WithCodecs(executor Executor, codecs CodecRegistry) (Executor, error) {
	if executor == nil {
		return nil, fmt.Errorf("executor must not be nil")
	}
	if codecs == nil {
		return nil, fmt.Errorf("codec registry must not be nil")
	}
	return wrapCodecExecutor(executor, codecs), nil
}

func wrapCodecExecutor(executor Executor, codecs CodecRegistry) Executor {
	base := codecExec{Executor: executor, codecs: codecs}
	_, hasLogical := executor.(logicalInvocationProvider)
	_, scope := executor.(ScopeBeginner)
	if scope {
		if hasLogical {
			return logicalCodecScopedExecutor{codecScopedExecutor{base}}
		}
		return codecScopedExecutor{codecExec: base}
	}
	if hasLogical {
		return logicalCodecExec{base}
	}
	return base
}

// executorCodecs reports the codec registry an executor carries, and the
// builtin registry for an executor that carries none.
//
// An executor that implements CodecProvider and returns nil from it is an
// error rather than an executor without codecs. WithCodecs already refuses a
// nil registry, so nil never means "no codecs" anywhere a caller could have
// written it deliberately, and a nil registry answers no lookup: every caller
// that reached for one would either substitute the builtin registry and decode
// against the wrong codecs, or dereference nil.
func executorCodecs(executor Executor) (CodecRegistry, error) {
	provider, ok := executor.(CodecProvider)
	if !ok {
		return builtinCodecs, nil
	}
	registry := provider.Codecs()
	if registry == nil {
		return nil, &PlanError{Code: "codec_registry_unavailable", Detail: "executor returned a nil codec registry"}
	}
	return registry, nil
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

// bindStatementEncoder adapts the codec registry to what bindplan asks for,
// and builds the errors this package reports.
type bindStatementEncoder struct{ registry CodecRegistry }

func (e bindStatementEncoder) CheckBindCodec(codec string) error {
	_, err := codecFor(e.registry, codec)
	return err
}

func (e bindStatementEncoder) EncodeBind(index int, codec string, value any) (driver.Value, error) {
	found, err := codecFor(e.registry, codec)
	if err != nil {
		return nil, err
	}
	encoded, err := found.Encode(value)
	if err != nil {
		return nil, &EncodeError{Index: index, Codec: CodecID(codec), Err: err}
	}
	return encoded, nil
}

func encodeStatement(statement stmt.Statement, slots []bindSlot, reg CodecRegistry) (stmt.Statement, error) {
	return bindplan.EncodeStatement(statement, slots, bindStatementEncoder{registry: reg})
}
