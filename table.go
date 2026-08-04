package rasql

import (
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// Table associates a SQL table with the Go type of one of its rows.
// Only this package implements it; generated table types embed it.
//
// Some values satisfy Table[T] with no typed table behind them: a nil interface,
// a nil pointer, and a wrapper whose embedded Table[T] is nil. Every function
// taking a Table[T] rejects such a value, as an error from the call, as an error
// from the Build of the statement the call feeds, or as a panic where the name
// says it panics.
type Table[T any] interface {
	// QueryTable returns the dialect-neutral table backing the descriptor.
	QueryTable() query.Table
	// Column returns a reference to a named column of the table.
	Column(name string) (query.Column, error)
	// tableRow keeps T inferable and stops implementations outside this package.
	tableRow() T
}

// typedTable is the only implementation of Table.
type typedTable[T any] struct {
	source query.Table
}

// NewTable creates a typed table from a validated schema definition.
func NewTable[T any](definition schema.Table) (Table[T], error) {
	source, err := query.NewTable(definition)
	if err != nil {
		return nil, fmt.Errorf("rasql: table definition: %w", err)
	}
	return typedTable[T]{source: source}, nil
}

// MustTable creates a typed table or panics when definition is invalid.
// It is intended for generated or otherwise static schema descriptors.
func MustTable[T any](definition schema.Table) Table[T] {
	table, err := NewTable[T](definition)
	if err != nil {
		panic(err)
	}
	return table
}

// As returns table under alias. Generated table types have their own As that
// also rebinds their column fields; this one serves dynamic code and the
// generated implementation.
func As[T any](table Table[T], alias string) (Table[T], error) {
	if isNilTable(table) {
		return nil, fmt.Errorf("rasql: table alias: %w", fmt.Errorf("table must not be nil"))
	}
	aliased, err := table.QueryTable().As(alias)
	if err != nil {
		return nil, fmt.Errorf("rasql: table alias: %w", err)
	}
	return typedTable[T]{source: aliased}, nil
}

// MustColumn looks up name on table and panics when it is absent.
// It exists for generated code, where the name comes from the descriptor itself.
func MustColumn[T any](table Table[T], name string) query.Column {
	if isNilTable(table) {
		panic("rasql: table column: table must not be nil")
	}
	column, err := table.Column(name)
	if err != nil {
		panic(fmt.Sprintf("rasql: table column: %s", err))
	}
	return column
}

// QueryTable returns the dialect-neutral table backing the descriptor.
func (t typedTable[T]) QueryTable() query.Table {
	return t.source
}

// Column returns a reference to a named column of the table.
func (t typedTable[T]) Column(name string) (query.Column, error) {
	return t.source.Column(name)
}

func (t typedTable[T]) tableRow() T {
	var zero T
	return zero
}

// isNilTable reports whether table has no typed table behind it, so every entry
// point taking a Table[T] can reject it instead of panicking.
//
// It catches more than the nil interface and the typed nil pointer isNil covers.
// A table wrapper embeds Table[T] and reaches every Table method through that
// embedded field, so a zero wrapper satisfies Table[T] while the field behind
// each promoted method is nil. That value is a struct, which isNil reports as
// not nil, and it needs no hand-written type to exist: rasqlgen emits an
// exported wrapper struct that embeds Table[T] under the exported field name
// Table, and a generated As returns a zero wrapper along with its error.
func isNilTable[T any](table Table[T]) bool {
	if isNil(table) {
		return true
	}
	return nilEmbeddedTable(reflect.ValueOf(table), reflect.TypeFor[Table[T]]())
}

// nilEmbeddedTable reports whether the embedded Table[T] behind value's promoted
// methods is nil. It follows one wrapper into the next, so a wrapper that wraps a
// wrapper is caught as well.
//
// A struct satisfying Table[T] from outside this package promotes its methods
// from exactly one anonymous field whenever that promotion is unambiguous, so
// the walk follows that field alone. No such field means the struct carries its
// own table, and several mean the field Go promotes from is not this walk's to
// guess; either way nothing nil was found. That also ends the walk, because a
// wrapper reachable from itself would need a second such field to satisfy
// Table[T] at all.
func nilEmbeddedTable(value reflect.Value, tableType reflect.Type) bool {
	for value.Kind() == reflect.Interface || value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return true
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return false
	}
	embedded, ok := embeddedTable(value, tableType)
	if !ok {
		return false
	}
	return nilEmbeddedTable(embedded, tableType)
}

// embeddedTable returns the one anonymous field of value that Table[T] methods
// can be promoted from, and reports whether there is exactly one.
func embeddedTable(value reflect.Value, tableType reflect.Type) (reflect.Value, bool) {
	valueType := value.Type()
	var embedded reflect.Value
	found := false
	for index := range valueType.NumField() {
		field := valueType.Field(index)
		if !field.Anonymous || !implementsTable(field.Type, tableType) {
			continue
		}
		if found {
			return reflect.Value{}, false
		}
		embedded = value.Field(index)
		found = true
	}
	return embedded, found
}

// implementsTable reports whether fieldType or a pointer to it satisfies
// tableType, so an embedded value and an embedded pointer both count.
func implementsTable(fieldType reflect.Type, tableType reflect.Type) bool {
	if fieldType.Implements(tableType) {
		return true
	}
	return reflect.PointerTo(fieldType).Implements(tableType)
}
