package rasql

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/sqltext"
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

func (e *DecodeError) Error() string {
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
func (e codecExec) queryCompiler() *querycompile.Compiler {
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
	return codecExec{Executor: executor, codecs: codecs}, nil
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

func encodeCompiled(compiled compiledQuery, reg CodecRegistry) (stmt.Statement, error) {
	statement, err := compiled.statementCopy()
	if err != nil {
		return stmt.Statement{}, err
	}
	return encodeStatement(statement, compiled.BindSlots(), reg)
}
func encodeStatement(statement stmt.Statement, slots []bindSlot, reg CodecRegistry) (stmt.Statement, error) {
	args := statement.BoundArgs()
	if len(args) != len(slots) {
		return stmt.Statement{}, &PlanError{Code: "bind_mismatch", Detail: "statement arguments and bind slots differ"}
	}
	for i, slot := range slots {
		codec, err := codecFor(reg, slot.codec)
		if err != nil {
			return stmt.Statement{}, err
		}
		if codec == nil {
			continue
		}
		value := args[i]
		name := ""
		if named, ok := value.(sql.NamedArg); ok {
			name, value = named.Name, named.Value
		}
		encoded, err := codec.Encode(value)
		if err != nil {
			return stmt.Statement{}, &EncodeError{Index: i, Codec: CodecID(slot.codec), Err: err}
		}
		if name != "" {
			args[i] = sql.Named(name, encoded)
		} else {
			args[i] = encoded
		}
	}
	return stmt.New(sqltext.Text(statement.SQL()), args...), nil
}
