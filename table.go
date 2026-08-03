package rasql

import (
	"fmt"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// Table associates a SQL table with the Go type of one of its rows.
// Only this package implements it; generated table types embed it.
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
	if isNil(table) {
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
	if isNil(table) {
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
