package rasql

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"
)

type PageDirection uint8

const (
	PageAscending PageDirection = iota + 1
	PageDescending
)

type Cursor string

var ErrInvalidCursor = errors.New("rasql: invalid cursor")

type CursorValueCodec interface {
	EncodeCursor(any) ([]byte, error)
	DecodeCursor([]byte) (any, error)
}

type pageKey[R any] struct {
	term      OrderTerm
	direction PageDirection
	nullable  bool
	codec     string
	typ       reflect.Type
	extract   func(R) (bool, any, error)
}

type PageKey[R any] interface{ pageKeyMarker() }

func (*pageKey[R]) pageKeyMarker() {}

func AscKey[R, T comparable](value Expr[T], extract func(R) T) PageKey[R] {
	return &pageKey[R]{term: AscExpr(value), direction: PageAscending, codec: value.codec,
		typ: reflect.TypeOf((*T)(nil)).Elem(),
		extract: func(row R) (bool, any, error) {
			owned, err := copyPageValue(extract(row))
			return true, owned, err
		}}
}

func DescKey[R, T comparable](value Expr[T], extract func(R) T) PageKey[R] {
	return &pageKey[R]{term: DescExpr(value), direction: PageDescending, codec: value.codec,
		typ: reflect.TypeOf((*T)(nil)).Elem(),
		extract: func(row R) (bool, any, error) {
			owned, err := copyPageValue(extract(row))
			return true, owned, err
		}}
}

func AscNullKey[R, T comparable](value NullExpr[T], extract func(R) Nullable[T], nulls NullOrder) PageKey[R] {
	return newNullPageKey(value, extract, PageAscending, nulls)
}

func DescNullKey[R, T comparable](value NullExpr[T], extract func(R) Nullable[T], nulls NullOrder) PageKey[R] {
	return newNullPageKey(value, extract, PageDescending, nulls)
}

func newNullPageKey[R, T comparable](value NullExpr[T], extract func(R) Nullable[T], direction PageDirection, nulls NullOrder) PageKey[R] {
	term := AscNull(value, nulls)
	if direction == PageDescending {
		term = DescNull(value, nulls)
	}
	return &pageKey[R]{term: term, direction: direction, nullable: true, codec: value.codec,
		typ: reflect.TypeOf((*T)(nil)).Elem(),
		extract: func(row R) (bool, any, error) {
			value := extract(row)
			if !value.Valid {
				return false, nil, nil
			}
			owned, err := copyPageValue(value.Value)
			return true, owned, err
		}}
}

type PageSpec[R any] struct {
	keys         []*pageKey[R]
	uniqueSuffix []*pageKey[R]
}

func NewPageSpec[R any](order []PageKey[R], uniqueSuffix ...PageKey[R]) (PageSpec[R], error) {
	if len(order) == 0 {
		return PageSpec[R]{}, planError("invalid_page_spec", "order", "must not be empty")
	}
	if len(uniqueSuffix) == 0 {
		return PageSpec[R]{}, planError("order_not_unique", "uniqueSuffix", "must not be empty")
	}
	if len(order) > 255 {
		return PageSpec[R]{}, planError("invalid_page_spec", "order", "must contain at most 255 keys")
	}
	keys := make([]*pageKey[R], len(order))
	for i, item := range order {
		key, ok := item.(*pageKey[R])
		if !ok || key == nil || key.term.node == nil {
			return PageSpec[R]{}, planError("invalid_page_spec", fmt.Sprintf("order[%d]", i), "must be a valid page key")
		}
		if key.nullable && key.term.nulls == NullOrderDefault {
			return PageSpec[R]{}, planError("invalid_page_spec", fmt.Sprintf("order[%d]", i), "nullable keys require explicit NULL order")
		}
		keys[i] = key
	}
	suffix := make([]*pageKey[R], len(uniqueSuffix))
	for i, item := range uniqueSuffix {
		key, ok := item.(*pageKey[R])
		if !ok || key == nil {
			return PageSpec[R]{}, planError("invalid_page_spec", fmt.Sprintf("uniqueSuffix[%d]", i), "must be a valid page key")
		}
		suffix[i] = key
		at := len(keys) - len(suffix) + i
		if at < 0 || keys[at] != key {
			return PageSpec[R]{}, planError("order_not_unique", "uniqueSuffix", "must match the terminal page keys")
		}
	}
	return PageSpec[R]{keys: append([]*pageKey[R](nil), keys...), uniqueSuffix: append([]*pageKey[R](nil), suffix...)}, nil
}

type PagePolicy struct{ DefaultLimit, MaxLimit int }

var DefaultPagePolicy = PagePolicy{DefaultLimit: 50, MaxLimit: 1000}

type PageRequest struct {
	Limit int
	After Cursor
}

type Page[R any] struct {
	Values  []R
	Next    Cursor
	HasMore bool
}

func copyPageValue[T any](value T) (any, error) {
	owned, _, err := adoptBind(value, false)
	if err != nil {
		return nil, err
	}
	return owned, nil
}

func encodeBuiltinCursor(value any) ([]byte, error) {
	if value == nil {
		return nil, errors.New("nil cursor value")
	}
	if t, ok := value.(time.Time); ok {
		var out [12]byte
		utc := t.UTC()
		binary.BigEndian.PutUint64(out[:8], uint64(utc.Unix()))
		binary.BigEndian.PutUint32(out[8:], uint32(utc.Nanosecond()))
		return out[:], nil
	}
	v := reflect.ValueOf(value)
	if v.Kind() == reflect.Float32 || v.Kind() == reflect.Float64 {
		f := v.Float()
		if math.IsNaN(f) {
			return nil, errors.New("NaN is not a cursor value")
		}
		var out [8]byte
		bits := math.Float64bits(f)
		if f >= 0 {
			bits ^= 1 << 63
		} else {
			bits = ^bits
		}
		binary.BigEndian.PutUint64(out[:], bits)
		return out[:], nil
	}
	switch v.Kind() {
	case reflect.Bool:
		if v.Bool() {
			return []byte{1}, nil
		}
		return []byte{0}, nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		var out [8]byte
		binary.BigEndian.PutUint64(out[:], uint64(v.Int())^(1<<63))
		return out[:], nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		var out [8]byte
		binary.BigEndian.PutUint64(out[:], v.Uint())
		return out[:], nil
	case reflect.String:
		return append([]byte(nil), v.String()...), nil
	case reflect.Slice:
		if v.Type().Elem().Kind() == reflect.Uint8 {
			return append([]byte(nil), v.Bytes()...), nil
		}
	}
	return nil, fmt.Errorf("unsupported cursor value %T", value)
}

func decodeBuiltinCursor(data []byte, typ reflect.Type) (any, error) {
	if typ == reflect.TypeOf(time.Time{}) {
		if len(data) != 12 {
			return nil, errors.New("invalid time cursor")
		}
		seconds := int64(binary.BigEndian.Uint64(data[:8]))
		nanos := binary.BigEndian.Uint32(data[8:])
		if nanos >= 1e9 {
			return nil, errors.New("invalid time cursor")
		}
		return time.Unix(seconds, int64(nanos)).UTC(), nil
	}
	if typ.Kind() == reflect.Float32 || typ.Kind() == reflect.Float64 {
		if len(data) != 8 {
			return nil, errors.New("invalid float cursor")
		}
		bits := binary.BigEndian.Uint64(data)
		if bits&(1<<63) != 0 {
			bits ^= 1 << 63
		} else {
			bits = ^bits
		}
		value := math.Float64frombits(bits)
		if math.IsNaN(value) {
			return nil, errors.New("NaN is not a cursor value")
		}
		if typ.Kind() == reflect.Float32 {
			narrow := float32(value)
			if float64(narrow) != value {
				return nil, errors.New("float cursor overflows destination type")
			}
		}
		out := reflect.New(typ).Elem()
		out.SetFloat(value)
		return out.Interface(), nil
	}
	switch typ.Kind() {
	case reflect.Bool:
		if len(data) != 1 || data[0] > 1 {
			return nil, errors.New("invalid bool cursor")
		}
		out := reflect.New(typ).Elem()
		out.SetBool(data[0] == 1)
		return out.Interface(), nil
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		if len(data) != 8 {
			return nil, errors.New("invalid integer cursor")
		}
		v := int64(binary.BigEndian.Uint64(data) ^ (1 << 63))
		bits := typ.Bits()
		if bits < 64 && (v < -(int64(1)<<(bits-1)) || v > (int64(1)<<(bits-1))-1) {
			return nil, errors.New("integer cursor overflows destination type")
		}
		out := reflect.New(typ).Elem()
		out.SetInt(v)
		return out.Interface(), nil
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
		if len(data) != 8 {
			return nil, errors.New("invalid integer cursor")
		}
		value := binary.BigEndian.Uint64(data)
		if bits := typ.Bits(); bits < 64 && value > (uint64(1)<<bits)-1 {
			return nil, errors.New("unsigned cursor overflows destination type")
		}
		out := reflect.New(typ).Elem()
		out.SetUint(value)
		return out.Interface(), nil
	case reflect.String:
		out := reflect.New(typ).Elem()
		out.SetString(string(data))
		return out.Interface(), nil
	case reflect.Slice:
		if typ.Elem().Kind() == reflect.Uint8 {
			return append([]byte(nil), data...), nil
		}
	}
	return nil, fmt.Errorf("unsupported cursor type %s", typ)
}

const cursorVersion byte = 1

func encodeCursorEnvelope[R any](fingerprint [32]byte, keys []*pageKey[R], values []cursorValue) (Cursor, error) {
	if len(keys) > 255 || len(values) != len(keys) {
		return "", errors.New("cursor envelope key count is out of range")
	}
	var b bytes.Buffer
	b.WriteByte(cursorVersion)
	b.Write(fingerprint[:])
	b.WriteByte(byte(len(keys)))
	for i, key := range keys {
		if len(key.codec) > 255 || len(values[i].data) > math.MaxUint16 {
			return "", errors.New("cursor envelope field is too large")
		}
		if !values[i].present && len(values[i].data) != 0 {
			return "", errors.New("absent cursor value has a payload")
		}
		b.WriteByte(byte(key.direction))
		if key.nullable {
			b.WriteByte(1)
		} else {
			b.WriteByte(0)
		}
		b.WriteByte(byte(key.term.nulls))
		b.WriteByte(byte(len(key.codec)))
		b.WriteString(key.codec)
		if values[i].present {
			b.WriteByte(1)
		} else {
			b.WriteByte(0)
		}
		_ = binary.Write(&b, binary.BigEndian, uint32(len(values[i].data)))
		b.Write(values[i].data)
	}
	if b.Len() > 64*1024 {
		return "", errors.New("cursor envelope exceeds 64 KiB")
	}
	return Cursor(base64.RawURLEncoding.EncodeToString(b.Bytes())), nil
}

type cursorValue struct {
	present bool
	data    []byte
}
