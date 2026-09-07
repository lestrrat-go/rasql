// Package rowvalue holds the representation of one database result row whose
// columns are known only at run time, and the machinery that reads values out
// of it. It is imported by both the rasql root package and rasql/dynamic;
// neither may import the other.
package rowvalue

import (
	"errors"
	"fmt"
	"slices"
)

// ErrConcurrentUse reports overlapping use of one Result cursor operation.
var ErrConcurrentUse = errors.New("rowvalue: result is in use concurrently")

// Header describes the immutable ordered columns of one result.
type Header struct {
	columns *columnHeader
}

// Len reports the number of result columns.
func (h Header) Len() int {
	if h.columns == nil {
		return 0
	}
	return len(h.columns.names)
}

// Names returns a copy of the ordered result column names.
func (h Header) Names() []string {
	if h.columns == nil {
		return nil
	}
	return slices.Clone(h.columns.names)
}

// Index returns the position of name in the result header.
func (h Header) Index(name string) (int, bool) {
	if h.columns == nil {
		return 0, false
	}
	index, ok := h.columns.index[name]
	return index, ok
}

// Row contains one database result row whose columns are known only at run
// time. Every row of one query shares the same columnHeader, so decoding a row
// costs one slice of values plus a lookup through the header's index, rather
// than a map[string]any allocated fresh for every row.
type Row struct {
	columns *columnHeader
	values  []any
}

// Header returns the immutable result header shared by this row.
func (d Row) Header() Header { return Header{columns: d.columns} }

// Value returns a copy of the value at index. SQL NULL is returned as nil with ok true.
func (d Row) Value(index int) (any, bool) {
	if index < 0 || index >= len(d.values) || d.columns == nil {
		return nil, false
	}
	return cloneValue(d.values[index]), true
}

// Values returns copies of all values in their result-column order.
func (d Row) Values() []any {
	if d.columns == nil {
		return nil
	}
	values := make([]any, len(d.values))
	for index, value := range d.values {
		values[index] = cloneValue(value)
	}
	return values
}

// columnHeader is the column-name-to-index mapping one query's rows share. It
// is built once and never written after construction, so concurrent readers
// need no lock.
type columnHeader struct {
	names []string       // cloned, never handed out
	index map[string]int // built once, never written after construction
}

// lookup returns the value named name in d, and whether it is present. A zero
// Row has a nil columns and reports every name absent, the same way the
// old nil map did.
func (d Row) lookup(name string) (any, bool) {
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

// NewRow validates column names and values and returns an independent row value.
func NewRow(names []string, values []any) (Row, error) {
	if len(names) != len(values) {
		return Row{}, fmt.Errorf("row: %d column names for %d values", len(names), len(values))
	}
	columns, err := newColumnHeader(names)
	if err != nil {
		return Row{}, err
	}
	cloned := make([]any, len(values))
	for i, value := range values {
		cloned[i] = cloneValue(value)
	}
	return Row{columns: columns, values: cloned}, nil
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
