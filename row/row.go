// Package row provides typed access to database result values.
package row

import (
	"fmt"
	"slices"
)

// Dynamic contains one database result row whose columns are known only at run
// time. Every row of one query shares the same columnHeader, so decoding a row
// costs one slice of values plus a lookup through the header's index, rather
// than a map[string]any allocated fresh for every row.
type Dynamic struct {
	columns *columnHeader
	values  []any
}

// columnHeader is the column-name-to-index mapping one query's rows share. It
// is built once and never written after construction, so concurrent readers
// need no lock.
type columnHeader struct {
	names []string       // cloned, never handed out
	index map[string]int // built once, never written after construction
}

// lookup returns the value named name in d, and whether it is present. A zero
// Dynamic has a nil columns and reports every name absent, the same way the
// old nil map did.
func (d Dynamic) lookup(name string) (any, bool) {
	if d.columns == nil {
		return nil, false
	}
	index, ok := d.columns.index[name]
	if !ok {
		return nil, false
	}
	return d.values[index], true
}

// newColumnHeader validates names and builds their index. The duplicate check
// is driven by the index map itself, so it costs one map lookup per name
// rather than a comparison against every earlier name.
func newColumnHeader(names []string) (*columnHeader, error) {
	index := make(map[string]int, len(names))
	for i, name := range names {
		if name == "" {
			return nil, fmt.Errorf("row: column name at index %d is empty", i)
		}
		if _, exists := index[name]; exists {
			return nil, fmt.Errorf("row: duplicate column name %q", name)
		}
		index[name] = i
	}
	return &columnHeader{names: slices.Clone(names), index: index}, nil
}

// NewDynamic validates column names and values and returns an independent row value.
func NewDynamic(names []string, values []any) (Dynamic, error) {
	if len(names) != len(values) {
		return Dynamic{}, fmt.Errorf("row: %d column names for %d values", len(names), len(values))
	}
	columns, err := newColumnHeader(names)
	if err != nil {
		return Dynamic{}, err
	}
	cloned := make([]any, len(values))
	for i, value := range values {
		cloned[i] = cloneValue(value)
	}
	return Dynamic{columns: columns, values: cloned}, nil
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
