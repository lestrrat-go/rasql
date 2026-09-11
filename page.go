package rasql

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/lestrrat-go/rasql/internal/cursorcodec"
	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

type preparedPage[R any] struct {
	prepared    preparedRows[R]
	spec        PageSpec[R]
	limit       int
	fingerprint [32]byte
	impossible  bool
}

func PageAfter[R any](ctx context.Context, executor Executor, q Query[R], spec PageSpec[R], policy PagePolicy, request PageRequest) (Page[R], error) {
	prepared, err := preparePageAfter(executor, q, spec, policy, request)
	if err != nil {
		return Page[R]{}, err
	}
	return consumePreparedPage(ctx, executor, prepared, nil)
}

func preparePageAfter[R any](executor Executor, q Query[R], spec PageSpec[R], policy PagePolicy, request PageRequest) (preparedPage[R], error) {
	var result preparedPage[R]
	if isNilExecutor(executor) {
		return result, planError("engine_profile_unavailable", "executor", "must not be nil")
	}
	if len(spec.keys) == 0 {
		return result, planError("invalid_page_spec", "order", "must not be empty")
	}
	if policy.DefaultLimit == 0 && policy.MaxLimit == 0 {
		policy = DefaultPagePolicy
	}
	if policy.DefaultLimit < 1 || policy.MaxLimit < policy.DefaultLimit || policy.MaxLimit > math.MaxInt-1 {
		return result, planError("invalid_page_policy", "policy", "limits are invalid")
	}
	limit := request.Limit
	if limit == 0 {
		limit = policy.DefaultLimit
	}
	if limit < 0 || limit > policy.MaxLimit {
		return result, planError("invalid_page_limit", "request.limit", "limit is outside policy")
	}
	for i, key := range spec.keys {
		if key.codec == "" {
			continue
		}
		if _, err := cursorCodec(executor, key.codec); err != nil {
			return result, planerr.Wrap("codec_unavailable", fmt.Sprintf("page.order[%d].codec", i), err.Error(), err)
		}
	}
	ordered, err := q.withKeysetOrder(pageTerms(spec.keys))
	if err != nil {
		return result, err
	}
	if err := ordered.Validate(); err != nil {
		return result, err
	}
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return result, planError("engine_profile_unavailable", "executor", "executor has no retained compiler")
	}
	base, err := compileQuery(provider.queryCompiler(), ordered)
	if err != nil {
		return result, err
	}
	var decoded []decodedCursor
	var cursorFP [32]byte
	if request.After != "" {
		decoded, cursorFP, err = decodePageCursor(request.After, spec, executor)
		if err != nil {
			return result, err
		}
	}
	predicate, impossible, err := keysetPredicate(spec.keys, decoded)
	if err != nil {
		return result, err
	}
	if impossible {
		result.spec = spec
		result.limit = limit
		result.impossible = true
		return result, nil
	}
	final := ordered
	if predicate.node != nil {
		final = final.Where(predicate)
	}
	final, err = final.Limit(limit + 1)
	if err != nil {
		return result, err
	}
	paged, err := compileQuery(provider.queryCompiler(), final)
	if err != nil {
		return result, err
	}
	indexes, err := matchBaseOccurrences(base, paged)
	if err != nil {
		return result, err
	}
	prepared, err := prepareRows(executor, final, paged)
	if err != nil {
		return result, err
	}
	fingerprint, err := pageFingerprint(executor.Dialect().Name(), base.statement.SQL(), final.Schema(), prepared.statement, spec.keys, indexes)
	if err != nil {
		return result, err
	}
	if request.After != "" && !bytesEqual(fingerprint[:], cursorFP[:]) {
		return result, invalidCursor(errors.New("cursor fingerprint does not match query"))
	}
	result.prepared = prepared
	result.spec = spec
	result.limit = limit
	result.fingerprint = fingerprint
	return result, nil
}

func consumePreparedPage[R any](ctx context.Context, executor Executor, page preparedPage[R], callback func(R, bool) error) (Page[R], error) {
	var result Page[R]
	if page.impossible {
		return result, nil
	}
	if callback == nil {
		callback = func(R, bool) error { return nil }
	}
	var terminal rowTerminal
	ctx = context.WithValue(ctx, rowTerminalKey{}, &terminal)
	rows, err := rowsPrepared(ctx, executor, page.prepared)
	if err != nil {
		return result, err
	}
	var callbackErr error
	for row, rowErr := range rows {
		if rowErr != nil {
			return result, rowErr
		}
		kept := len(result.Values) < page.limit
		if err := callback(row, kept); err != nil {
			callbackErr = err
			terminal.cause = err
			break
		}
		if kept {
			result.Values = append(result.Values, row)
			continue
		}
		if len(result.Values) == page.limit {
			result.HasMore = true
			break
		}
	}
	if callbackErr != nil {
		return result, callbackErr
	}
	if result.HasMore {
		last := result.Values[len(result.Values)-1]
		values := make([]cursorcodec.Value, len(page.spec.keys))
		for i, key := range page.spec.keys {
			present, value, extractErr := key.extract(last)
			if extractErr != nil {
				return Page[R]{}, extractErr
			}
			var data []byte
			if present {
				var encodeErr error
				data, encodeErr = encodePageValue(key, value, executor)
				if encodeErr != nil {
					return Page[R]{}, encodeErr
				}
			}
			values[i] = cursorcodec.Value{Present: present, Data: data}
		}
		encoded, encodeErr := cursorcodec.EncodeEnvelope(page.fingerprint, pageKeyFields(page.spec.keys), values)
		if encodeErr != nil {
			return Page[R]{}, encodeErr
		}
		result.Next = Cursor(encoded)
	}
	return result, nil
}

func pageTerms[R any](keys []*pageKey[R]) []OrderTerm {
	terms := make([]OrderTerm, len(keys))
	for i, key := range keys {
		terms[i] = key.term
	}
	return terms
}

type decodedCursor struct {
	present bool
	value   any
}

func decodePageCursor[R any](cursor Cursor, spec PageSpec[R], executor Executor) ([]decodedCursor, [32]byte, error) {
	var fp [32]byte
	envelope, err := cursorcodec.DecodeEnvelope(string(cursor))
	if err != nil {
		return nil, fp, invalidCursor(err)
	}
	fp = envelope.Fingerprint
	if len(envelope.Fields) != len(spec.keys) {
		return nil, fp, invalidCursor(errors.New("cursor arity mismatch"))
	}
	expected := pageKeyFields(spec.keys)
	values := make([]decodedCursor, len(spec.keys))
	registry := builtinCodecs
	if cp, ok := executor.(CodecProvider); ok {
		registry = cp.Codecs()
	}
	for i, key := range spec.keys {
		if envelope.Fields[i] != expected[i] {
			return nil, fp, invalidCursor(errors.New("cursor metadata mismatch"))
		}
		value := envelope.Values[i]
		if !value.Present {
			if !key.nullable {
				return nil, fp, invalidCursor(errors.New("invalid absent cursor value"))
			}
			values[i] = decodedCursor{}
			continue
		}
		decoded, decodeErr := decodePageValue(key, value.Data, registry)
		if decodeErr != nil {
			return nil, fp, invalidCursor(decodeErr)
		}
		values[i] = decodedCursor{true, decoded}
	}
	return values, fp, nil
}

func invalidCursor(cause error) error { return fmt.Errorf("%w: %w", ErrInvalidCursor, cause) }

func encodePageValue[R any](key *pageKey[R], value any, executor Executor) ([]byte, error) {
	if !key.nullable && value == nil {
		return nil, errors.New("nil cursor value")
	}
	if key.codec == "" {
		return cursorcodec.EncodeValue(value)
	}
	codec, err := cursorCodec(executor, key.codec)
	if err != nil {
		return nil, err
	}
	return codec.EncodeCursor(value)
}
func decodePageValue[R any](key *pageKey[R], data []byte, registry CodecRegistry) (any, error) {
	if key.codec == "" {
		return cursorcodec.DecodeValue(data, key.typ)
	}
	codec, ok := registry.Lookup(CodecID(key.codec))
	if !ok {
		return nil, fmt.Errorf("codec %q unavailable", key.codec)
	}
	cursorCodec, ok := codec.(CursorValueCodec)
	if !ok {
		return nil, fmt.Errorf("codec %q does not support cursors", key.codec)
	}
	return cursorCodec.DecodeCursor(data)
}
func cursorCodec(executor Executor, id string) (CursorValueCodec, error) {
	registry := builtinCodecs
	if cp, ok := executor.(CodecProvider); ok {
		registry = cp.Codecs()
	}
	codec, ok := registry.Lookup(CodecID(id))
	if !ok {
		return nil, fmt.Errorf("codec %q unavailable", id)
	}
	cursor, ok := codec.(CursorValueCodec)
	if !ok {
		return nil, fmt.Errorf("codec %q does not support cursors", id)
	}
	return cursor, nil
}

func keysetPredicate[R any](keys []*pageKey[R], cursors []decodedCursor) (Predicate, bool, error) {
	if len(cursors) == 0 {
		return Predicate{}, false, nil
	}
	branches := make([]query.Expression, 0, len(keys))
	impossible := true
	for i, key := range keys {
		if !cursors[i].present && !key.nullable {
			return Predicate{}, false, invalidCursor(errors.New("missing non-null cursor value"))
		}
		parts := make([]query.Expression, 0, i+1)
		valid := true
		for j := 0; j < i; j++ {
			prior := keys[j]
			c := cursors[j]
			if !c.present {
				parts = append(parts, query.IsNull(prior.term.node))
				continue
			}
			parts = append(parts, query.And(query.IsNotNull(prior.term.node), query.Equal(prior.term.node, cursorBind(c.value, prior.codec))))
		}
		current := key.term.node
		c := cursors[i]
		if !c.present {
			if key.term.nulls == NullsFirst {
				parts = append(parts, query.IsNotNull(current))
			} else {
				valid = false
			}
		} else {
			comparison := query.GreaterThan(current, cursorBind(c.value, key.codec))
			if key.direction == PageDescending {
				comparison = query.LessThan(current, cursorBind(c.value, key.codec))
			}
			branch := query.And(query.IsNotNull(current), comparison)
			if key.term.nulls == NullsLast {
				branch = query.Or(query.IsNull(current), branch)
			}
			parts = append(parts, branch)
		}
		if valid {
			branch := parts[0]
			if len(parts) > 1 {
				branch = query.And(parts...)
			}
			branches = append(branches, branch)
			impossible = false
		}
	}
	if impossible {
		return Predicate{}, true, nil
	}
	if len(branches) == 1 {
		return Predicate{node: branches[0]}, false, nil
	}
	return Predicate{node: query.Or(branches...)}, false, nil
}

func cursorBind(value any, codec string) query.Expression {
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	snapshot, copier, err := adoptBind(value, false)
	return query.Bind(bindToken{id: id, value: snapshot, codec: codec, copy: copier, err: err})
}

func bytesEqual(a, b []byte) bool { return string(a) == string(b) }

func pageFingerprint[R any](engine, sqlText string, statementSchema ResultSchema, statement stmt.Statement, keys []*pageKey[R], indexes []int) ([32]byte, error) {
	h := sha256.New()
	writeField(h, "rasql-keyset-v1")
	writeField(h, engine)
	writeField(h, sqlText)
	columns := statementSchema.Columns()
	writeUintField(h, "column-count", uint64(len(columns)))
	for _, column := range columns {
		writeField(h, column.Name)
		if err := writeColumnType(h, column.Type); err != nil {
			return [32]byte{}, err
		}
		writeField(h, column.Codec)
		if column.Nullable {
			writeField(h, "nullable")
		} else {
			writeField(h, "required")
		}
	}
	writeUintField(h, "key-count", uint64(len(keys)))
	for _, key := range keys {
		writeField(h, string([]byte{byte(key.direction), byte(key.term.nulls)}))
		writeField(h, key.codec)
		writeField(h, key.term.source)
	}
	args := statement.Args()
	writeUintField(h, "argument-count", uint64(len(indexes)))
	for _, index := range indexes {
		if index < 0 || index >= len(args) {
			return [32]byte{}, planError("internal_plan", "binds", "matched argument index is invalid")
		}
		if err := writeFingerprintValue(h, args[index]); err != nil {
			return [32]byte{}, err
		}
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result, nil
}

func writeColumnType(h hashWriter, columnType schema.ColumnType) error {
	writeField(h, "column-type")
	writeField(h, string(columnType.Kind()))
	writeField(h, "parameters")
	switch typed := columnType.(type) {
	case schema.BooleanType, schema.FloatType, schema.BytesType, schema.TimeType, schema.JSONType, schema.UUIDType, schema.OpaqueType:
		return nil
	case schema.IntegerType:
		writeBoolField(h, "unsigned", typed.Unsigned)
		width, stated := typed.DisplayWidth.Value()
		writeBoolField(h, "display-width-stated", stated)
		if stated {
			writeIntField(h, "display-width", int64(width))
		}
		writeBoolField(h, "zerofill", typed.ZeroFill)
	case schema.TextType:
		width, stated := typed.Width.Value()
		writeBoolField(h, "width-stated", stated)
		if stated {
			writeIntField(h, "width", int64(width))
		}
		writeBoolField(h, "fixed", typed.Fixed)
	case schema.DecimalType:
		writeIntField(h, "precision", int64(typed.Precision))
		scale, stated := typed.Scale.Value()
		writeBoolField(h, "scale-stated", stated)
		if stated {
			writeIntField(h, "scale", int64(scale))
		}
		writeBoolField(h, "unsigned", typed.Unsigned)
		writeBoolField(h, "zerofill", typed.ZeroFill)
	default:
		return fmt.Errorf("unsupported column type %T", columnType)
	}
	return nil
}

func writeBoolField(h hashWriter, tag string, value bool) {
	writeField(h, tag)
	if value {
		writeField(h, "1")
	} else {
		writeField(h, "0")
	}
}

func writeIntField(h hashWriter, tag string, value int64) {
	writeField(h, tag)
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], uint64(value))
	writeField(h, string(data[:]))
}

func writeUintField(h hashWriter, tag string, value uint64) {
	writeField(h, tag)
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	writeField(h, string(data[:]))
}

func writeField(h hashWriter, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(value))
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeFingerprintValue(h hashWriter, value any) error {
	if named, ok := value.(sql.NamedArg); ok {
		writeField(h, "named")
		writeField(h, named.Name)
		return writeFingerprintValue(h, named.Value)
	}
	switch value := value.(type) {
	case nil:
		writeField(h, "nil")
	case int64:
		writeField(h, "int64")
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(value))
		_, _ = h.Write(b[:])
	case float64:
		writeField(h, "float64")
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(value))
		_, _ = h.Write(b[:])
	case bool:
		writeField(h, "bool")
		if value {
			_, _ = h.Write([]byte{1})
		} else {
			_, _ = h.Write([]byte{0})
		}
	case string:
		writeField(h, "string")
		writeField(h, value)
	case []byte:
		writeField(h, "bytes")
		writeField(h, string(value))
	case time.Time:
		writeField(h, "time")
		writeField(h, value.UTC().Format(time.RFC3339Nano))
	default:
		v := reflect.ValueOf(value)
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			writeField(h, v.Type().String())
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(v.Int()))
			_, _ = h.Write(b[:])
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			writeField(h, v.Type().String())
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], v.Uint())
			_, _ = h.Write(b[:])
		case reflect.Float32, reflect.Float64:
			writeField(h, v.Type().String())
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], math.Float64bits(v.Float()))
			_, _ = h.Write(b[:])
		default:
			return fmt.Errorf("unsupported driver value %T", value)
		}
	}
	return nil
}
