// Package row provides typed access to database result values.
package row

import (
	"fmt"
	"reflect"
	"time"
)

// Dynamic contains one database result row whose columns are known only at run time.
type Dynamic struct {
	values map[string]any
}

// New validates column names and values and returns an independent row value.
func New(names []string, values []any) (Dynamic, error) {
	if len(names) != len(values) {
		return Dynamic{}, fmt.Errorf("row: %d column names for %d values", len(names), len(values))
	}
	result := Dynamic{values: make(map[string]any, len(names))}
	for i, name := range names {
		if name == "" {
			return Dynamic{}, fmt.Errorf("row: column name at index %d is empty", i)
		}
		if _, exists := result.values[name]; exists {
			return Dynamic{}, fmt.Errorf("row: duplicate column name %q", name)
		}
		result.values[name] = cloneValue(values[i])
	}
	return result, nil
}

// Column is a typed reference to a result column.
type Column[T any] struct {
	name    string
	decoder ColumnDecoder[T]
}

// NewColumn creates a typed result column using decoder.
func NewColumn[T any](name string, decoder ColumnDecoder[T]) (Column[T], error) {
	if name == "" {
		return Column[T]{}, fmt.Errorf("row: column name must not be empty")
	}
	if isNilDecoder(decoder) {
		return Column[T]{}, fmt.Errorf("row: decoder for column %q must not be nil", name)
	}
	return Column[T]{name: name, decoder: decoder}, nil
}

// Name returns the result column name.
func (c Column[T]) Name() string {
	return c.name
}

// Get decodes c from r.
func (c Column[T]) Get(r Dynamic) (T, error) {
	var zero T
	if c.name == "" || c.decoder == nil {
		return zero, fmt.Errorf("row: invalid typed column")
	}
	value, ok := r.values[c.name]
	if !ok {
		return zero, fmt.Errorf("row: column %q is not present", c.name)
	}
	decoded, err := c.decoder.Decode(value)
	if err != nil {
		return zero, fmt.Errorf("row: decode column %q: %w", c.name, err)
	}
	return decoded, nil
}

// ColumnDecoder converts one database result value into T.
// NewColumn is what takes it; a whole row is decoded by Decoder instead.
type ColumnDecoder[T any] interface {
	Decode(any) (T, error)
}

// ColumnDecoderFunc adapts a function into a ColumnDecoder.
type ColumnDecoderFunc[T any] func(any) (T, error)

// Decode converts value with f.
func (f ColumnDecoderFunc[T]) Decode(value any) (T, error) {
	return f(value)
}

// Null preserves whether a database value was NULL.
type Null[T any] struct {
	Value T
	Valid bool
}

// Nullable wraps decoder so it accepts a NULL result value.
func Nullable[T any](decoder ColumnDecoder[T]) ColumnDecoder[Null[T]] {
	return ColumnDecoderFunc[Null[T]](func(value any) (Null[T], error) {
		if value == nil {
			return Null[T]{}, nil
		}
		if isNilDecoder(decoder) {
			return Null[T]{}, fmt.Errorf("row: nullable decoder must not be nil")
		}
		decoded, err := decoder.Decode(value)
		if err != nil {
			return Null[T]{}, err
		}
		return Null[T]{Value: decoded, Valid: true}, nil
	})
}

func isNilDecoder(decoder any) bool {
	if decoder == nil {
		return true
	}
	value := reflect.ValueOf(decoder)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return value.IsNil()
	default:
		return false
	}
}

// Bool decodes a boolean result value.
func Bool(name string) (Column[bool], error) {
	return NewColumn(name, ColumnDecoderFunc[bool](decodeBool))
}

// Int64 decodes an integer result value.
func Int64(name string) (Column[int64], error) {
	return NewColumn(name, ColumnDecoderFunc[int64](func(value any) (int64, error) {
		decoded, ok := value.(int64)
		if !ok {
			return 0, typeError("int64", value)
		}
		return decoded, nil
	}))
}

// Float64 decodes a floating-point result value.
func Float64(name string) (Column[float64], error) {
	return NewColumn(name, ColumnDecoderFunc[float64](func(value any) (float64, error) {
		decoded, ok := value.(float64)
		if !ok {
			return 0, typeError("float64", value)
		}
		return decoded, nil
	}))
}

// String decodes a text result value.
func String(name string) (Column[string], error) {
	return NewColumn(name, ColumnDecoderFunc[string](func(value any) (string, error) {
		switch decoded := value.(type) {
		case string:
			return decoded, nil
		case []byte:
			return string(decoded), nil
		default:
			return "", typeError("string", value)
		}
	}))
}

// Bytes decodes a byte-slice result value and returns an independent slice.
func Bytes(name string) (Column[[]byte], error) {
	return NewColumn(name, ColumnDecoderFunc[[]byte](func(value any) ([]byte, error) {
		decoded, ok := value.([]byte)
		if !ok {
			return nil, typeError("[]byte", value)
		}
		return append([]byte(nil), decoded...), nil
	}))
}

// Time decodes a timestamp result value.
func Time(name string) (Column[time.Time], error) {
	return NewColumn(name, ColumnDecoderFunc[time.Time](decodeTime))
}

func cloneValue(value any) any {
	bytes, ok := value.([]byte)
	if !ok {
		return value
	}
	return append([]byte(nil), bytes...)
}

func typeError(expected string, value any) error {
	if value == nil {
		return fmt.Errorf("expected %s, got NULL", expected)
	}
	return fmt.Errorf("expected %s, got %T", expected, value)
}
