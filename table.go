package rasql

import (
	"fmt"
	"reflect"
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
// candidates. So the rule asks a promoted method instead of inspecting fields.
//
// It probes the unexported tableRow method first rather than QueryTable, because
// tableRow cannot be intercepted: it is unexported to this package, so a type
// defined outside this package can never declare its own tableRow, and always
// reaches the one promoted from its embedded Table[T]. A nil pointer dereference
// from that call proves the embedded table is missing, with no caller code
// having run. Probing QueryTable first would not have that property: a caller
// type can declare its own QueryTable, and a nil dereference inside that
// caller's own method looks identical to one reached through a nil embedded
// field, wrongly relabelling the caller's own bug as a missing table.
//
// When tableRow does reach a real table, the value works and the probe stops
// there without calling QueryTable at all, so a panic from a caller's own
// QueryTable propagates out of the entry point unchanged instead of being
// caught by this guard.
//
// When tableRow nil-dereferences, the embedded table may still be genuinely
// missing, or the value may be the self-method shape: a type that supplies its
// own working QueryTable and Column and keeps the embedded Table[T] nil only to
// satisfy tableRow. QueryTable is probed next to tell those apart. This still
// cannot tell a genuinely missing table apart from a self-method table whose own
// QueryTable happens to nil-dereference for an unrelated reason: both look like
// a nil-dereferencing QueryTable behind a nil-dereferencing tableRow, and Go
// gives no way to attribute a recovered nil dereference to the frame that raised
// it. That one shape is outside what this guard can promise.
func isNilTable[T any](table Table[T]) bool {
	if isNil(table) {
		return true
	}
	if !dereferencesNil(func() { table.tableRow() }) {
		return false
	}
	return dereferencesNil(func() { table.QueryTable() })
}

// dereferencesNil calls call and reports whether that call dereferenced a nil
// pointer, which is what reaching a Table method through a nil embedded field or
// a nil embedded pointer does.
//
// Any other panic is re-panicked unchanged, so a bug inside a caller's own
// method surfaces as itself instead of being relabelled a nil table.
func dereferencesNil(call func()) bool {
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
		call()
	}()
	return dereferencedNil
}

// nilDereferenceMessage is how the Go runtime describes a nil pointer
// dereference. The runtime exports no value to compare such a panic against, so
// the recovered runtime.Error is matched on this text, alongside the concrete
// type check nilPointerDereference also does.
const nilDereferenceMessage = "invalid memory address or nil pointer dereference"

// nilPointerDereference reports whether recovered is the runtime's nil pointer
// dereference rather than a panic the code under it raised itself.
//
// Matching the runtime.Error interface and the message text is not enough on
// its own: runtime.Error is a public interface, so a caller can declare its
// own type that implements it, return exactly nilDereferenceMessage from its
// Error method, and panic with that value from its own QueryTable. Without a
// further check, that fabricated value would be indistinguishable from a real
// nil dereference and would be swallowed as "table must not be nil" instead
// of propagating as the caller's own panic.
//
// The concrete type behind recovered is checked against package "runtime" to
// close that gap. Every nil pointer dereference on this Go toolchain panics
// with the runtime's own concrete error type (currently *runtime.errorString,
// reached by unwrapping any pointer indirection), and that type's PkgPath is
// "runtime". No type a caller declares outside the standard library can carry
// that package path, so the check cannot be satisfied by a fabricated
// look-alike. An interface-only check has no such property, which is why it
// is not enough by itself.
//
// This ties the classifier to how the current runtime happens to construct
// the panic value, which is a known, accepted risk: if a future Go release
// ever panicked with a nil-dereference value from a different package, this
// check would fail closed. It would treat that panic as unrelated to a
// missing table and re-panic it unchanged, which is the behavior this guard
// had before it existed, rather than risk misclassifying a caller's own panic
// as a missing table. TestTableGuardKeepsUnrelatedPanics and the guard's other
// panic-shape tests would then fail loudly instead of the guard silently
// drifting.
func nilPointerDereference(recovered any) bool {
	failure, ok := recovered.(runtime.Error)
	if !ok {
		return false
	}

	failureType := reflect.TypeOf(recovered)
	for failureType != nil && failureType.Kind() == reflect.Pointer {
		failureType = failureType.Elem()
	}
	if failureType == nil || failureType.PkgPath() != "runtime" {
		return false
	}

	return strings.Contains(failure.Error(), nilDereferenceMessage)
}
