// Package cursorcodec owns the byte format keyset pagination cursors are
// written in. It encodes and decodes the built-in value types, and it frames
// those values into the envelope a cursor string carries.
//
// The package knows nothing about queries, keys or codec registries. A caller
// supplies the per-field metadata it wants written and checks that metadata
// back itself, so the rules enforced here are only the ones the wire format
// itself imposes.
package cursorcodec

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"reflect"
	"time"
)

// EncodeValue writes value in the order-preserving form the built-in cursor
// types use. Integers and floats are biased so that comparing the encoded
// bytes orders them the same way comparing the values would.
func EncodeValue(value any) ([]byte, error) {
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

// DecodeValue reads data back as typ, which must be the type EncodeValue was
// given. A value that does not fit typ is rejected rather than truncated, so a
// cursor written for a wider column cannot silently narrow.
func DecodeValue(data []byte, typ reflect.Type) (any, error) {
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
