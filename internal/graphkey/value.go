// Package graphkey turns a value read back from a database into the bytes a
// graph key compares on. Two rows belong to the same parent when their key
// bytes match, so the framing has to give one value one encoding whatever
// driver produced it, and has to refuse a value it cannot represent rather
// than produce bytes that would compare wrong.
package graphkey

import (
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// Tags label each framed value with the kind it came from, so two values that
// share a payload but not a type never frame alike.
const (
	tagNull byte = iota
	tagInt64
	tagFloat64
	tagBool
	tagBytes
	tagString
	tagTime
)

// Normalize converts value into a driver.Value, and reports a value the
// database/sql driver contract does not allow.
func Normalize(value any) (driver.Value, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := driver.DefaultParameterConverter.ConvertValue(value)
	if err != nil {
		return nil, err
	}
	if err := ValidateDriverValue(converted); err != nil {
		return nil, err
	}
	return converted, nil
}

// ValidateDriverValue reports a value database/sql would refuse to carry.
func ValidateDriverValue(value driver.Value) error {
	if value == nil || driver.IsValue(value) {
		return nil
	}
	return fmt.Errorf("value %T is not a legal driver value", value)
}

// Frame encodes value for use as a graph key. NaN is refused, because it
// compares false against itself and would silently drop a row.
func Frame(value driver.Value) ([]byte, error) {
	return frame(value, false)
}

// FrameAllowingNaN encodes value for a fingerprint, where the bytes identify a
// plan rather than match rows, so NaN carries no risk of a wrong comparison.
func FrameAllowingNaN(value driver.Value) ([]byte, error) {
	return frame(value, true)
}

func frame(value driver.Value, allowNaN bool) ([]byte, error) {
	var tag byte
	var payload []byte
	switch v := value.(type) {
	case nil:
		tag = tagNull
	case int64:
		tag = tagInt64
		payload = make([]byte, 8)
		binary.BigEndian.PutUint64(payload, uint64(v))
	case float64:
		if !allowNaN && math.IsNaN(v) {
			return nil, fmt.Errorf("NaN is not a graph key")
		}
		tag = tagFloat64
		payload = make([]byte, 8)
		binary.BigEndian.PutUint64(payload, math.Float64bits(v))
	case bool:
		tag = tagBool
		if v {
			payload = []byte{1}
		} else {
			payload = []byte{0}
		}
	case []byte:
		tag = tagBytes
		payload = append([]byte(nil), v...)
	case string:
		tag = tagString
		payload = []byte(v)
	case time.Time:
		tag = tagTime
		payload = []byte(v.UTC().Format(time.RFC3339Nano))
	default:
		return nil, fmt.Errorf("unsupported graph key value %T", value)
	}
	result := make([]byte, 9+len(payload))
	result[0] = tag
	binary.BigEndian.PutUint64(result[1:], uint64(len(payload)))
	copy(result[9:], payload)
	return result, nil
}
