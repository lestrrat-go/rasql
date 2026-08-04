package rasql

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// Table associates a SQL table with the Go type of one of its rows.
// Only this package implements it; generated table types embed it.
//
// Some values satisfy Table[T] with no typed table behind them, such as a nil
// interface or a zero wrapper whose embedded Table[T] is nil. Every function
// taking a Table[T] rejects a value whose QueryTable cannot reach a table, as an
// error from the call, as an error from the Build of the statement the call
// feeds, or as a panic where the name says it panics.
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
// point taking a Table[T] can reject it instead of panicking. It is the one
// place this rule lives; every such entry point calls it.
//
// It catches more than the nil interface and the typed nil pointer isNil covers.
// A table wrapper embeds Table[T] and reaches every Table method through that
// embedded field, so a zero wrapper satisfies Table[T] while the field behind
// each promoted method is nil. That value is a struct, which isNil reports as
// not nil, and it needs no hand-written type to exist: rasqlgen emits an
// exported wrapper struct that embeds Table[T] under the exported field name
// Table, and a generated As returns a zero wrapper along with its error.
//
// The nil field is not what makes such a value unusable, and the fields cannot
// decide the question either way. A type can supply its own QueryTable and
// Column and keep a nil embedded Table[T] only for the unexported tableRow
// method: that value works, yet it has the same fields as a zero wrapper. A
// struct can also embed two fields that satisfy Table[T], where Go promotes the
// methods from the shallower one while an inspection of the fields sees only two
// candidates. So the rule asks the promoted method instead: call QueryTable, and
// treat a nil pointer dereference from that call as the missing table.
func isNilTable[T any](table Table[T]) bool {
	if isNil(table) {
		return true
	}
	return queryTableDereferencesNil(table)
}

// queryTableDereferencesNil calls table.QueryTable and reports whether that call
// dereferenced a nil pointer, which is what reaching a Table method through a nil
// embedded field or a nil embedded pointer does.
//
// Any other panic is re-panicked unchanged, so a bug inside a caller's own
// QueryTable surfaces as itself instead of being relabelled a nil table.
func queryTableDereferencesNil[T any](table Table[T]) bool {
	dereferencedNil := false
	func() {
		defer func() {
			recovered := recover()
			if recovered == nil {
				return
			}
			if !nilPointerDereference(recovered) {
				panic(recovered)
			}
			dereferencedNil = true
		}()
		table.QueryTable()
	}()
	return dereferencedNil
}

// nilDereferenceMessage is how the Go runtime describes a nil pointer
// dereference. The runtime exports no value to compare such a panic against, so
// the recovered runtime.Error is matched on this text.
const nilDereferenceMessage = "invalid memory address or nil pointer dereference"

// nilPointerDereference reports whether recovered is the runtime's nil pointer
// dereference rather than a panic the code under it raised itself.
func nilPointerDereference(recovered any) bool {
	failure, ok := recovered.(runtime.Error)
	if !ok {
		return false
	}
	return strings.Contains(failure.Error(), nilDereferenceMessage)
}
