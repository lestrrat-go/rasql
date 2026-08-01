package rasql

import (
	"fmt"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// Table associates a reusable table reference with the Go type for one of its rows.
type Table[T any] struct {
	reference query.TableRef
}

// NewTable creates a typed table from a validated schema definition.
func NewTable[T any](definition schema.Table) (Table[T], error) {
	reference, err := query.NewTableRef(definition)
	if err != nil {
		return Table[T]{}, fmt.Errorf("rasql: table definition: %w", err)
	}
	return Table[T]{reference: reference}, nil
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

// Ref returns the underlying reusable table reference.
func (t Table[T]) Ref() query.TableRef {
	return t.reference
}
