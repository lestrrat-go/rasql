package rasql

import (
	"context"
	"fmt"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// ReadTable associates a queryable SQL object with the Go type of one of its rows.
type ReadTable[T any] interface {
	Ref() query.TableRef
	Column(name string) ColumnRef
	tableRow() T
}

// Table associates a writable SQL table with the Go type of one of its rows.
// Only this package implements it; generated table types embed it.
//
// Every function taking a Table[T] reports "table must not be nil" for the nil
// interface. A wrapper whose embedded Table[T] is nil, such as the zero wrapper
// a failed generated As returns beside its error, is not the nil interface:
// passing that wrapper to one of those functions dereferences the nil embedded
// field and panics, and the panic names the caller that built it.
type Table[T any] interface {
	ReadTable[T]
	writableTable()
}

// typedTable is the only implementation of Table.
type typedTable[T any] struct {
	source query.TableRef
}

type readTable[T any] struct {
	source query.TableRef
}

func (readTable[T]) tableRow() T                    { var zero T; return zero }
func (t readTable[T]) Ref() query.TableRef          { return t.source }
func (t readTable[T]) Column(name string) ColumnRef { return t.source.Column(name) }
func (typedTable[T]) writableTable()                {}

// ReadTableOf creates a queryable typed object from a validated schema definition.
func ReadTableOf[T any](definition schema.TableDef) (ReadTable[T], error) {
	source, err := query.NewTableRef(definition)
	if err != nil {
		return nil, fmt.Errorf("rasql: table definition: %w", err)
	}
	return readTable[T]{source: source}, nil
}

// MustReadTableOf creates a queryable typed object or panics when definition is invalid.
func MustReadTableOf[T any](definition schema.TableDef) ReadTable[T] {
	table, err := ReadTableOf[T](definition)
	if err != nil {
		panic(err)
	}
	return table
}

// TableOf creates a typed table from a validated schema definition.
func TableOf[T any](definition schema.TableDef) (Table[T], error) {
	if err := requireWritableDefinition(definition); err != nil {
		return nil, err
	}
	source, err := query.NewTableRef(definition)
	if err != nil {
		return nil, fmt.Errorf("rasql: table definition: %w", err)
	}
	return typedTable[T]{source: source}, nil
}

func requireWritableDefinition(definition schema.TableDef) error {
	for _, operation := range []schema.Operation{schema.OperationInsert, schema.OperationUpdate, schema.OperationDelete} {
		if !definition.Supports(operation) {
			return fmt.Errorf("rasql: object %q does not support operation %d", definition.QualifiedName(), operation)
		}
	}
	return nil
}

// MustTableOf creates a typed table or panics when definition is invalid.
// It is intended for generated or otherwise static schema descriptors.
func MustTableOf[T any](definition schema.TableDef) Table[T] {
	table, err := TableOf[T](definition)
	if err != nil {
		panic(err)
	}
	return table
}

// TableFrom creates a typed table from a descriptor that is already known to be
// valid, without validating or copying it. It is the entry point generated code
// uses: rasqlgen validates every descriptor it emits, so validating again at
// package initialization would re-derive a result that cannot change, and would
// cost one identifier check per column plus a copy of every slice in the
// descriptor.
//
// It is not free. Every entry point indexes the descriptor's column names once,
// which costs one map allocation and one pass over the columns per table, and
// buys a column lookup that costs the same at any table width. That is a cost
// this entry point pays too; what it skips is validation and cloning.
//
// It carries query.TableRefFrom's contract, including that the returned table
// reads the descriptor's slices in place. Read that doc before using it with a
// descriptor assembled at runtime; TableOf and MustTableOf are the validating
// entry points.
func TableFrom[T any](definition schema.TableDef) Table[T] {
	return typedTable[T]{source: query.TableRefFrom(definition)}
}

// ReadTableFrom creates a queryable typed object from a descriptor known to be valid.
func ReadTableFrom[T any](definition schema.TableDef) ReadTable[T] {
	return readTable[T]{source: query.TableRefFrom(definition)}
}

// As returns table under alias. Generated table types have their own As with
// the same fixed body; this one serves dynamic code and the generated
// implementation.
//
// `table` must not be nil.
func As[T any](table Table[T], alias string) (Table[T], error) {
	if table == nil {
		return nil, fmt.Errorf("rasql: table alias: %w", fmt.Errorf("table must not be nil"))
	}
	aliased, err := table.Ref().As(alias)
	if err != nil {
		return nil, fmt.Errorf("rasql: table alias: %w", err)
	}
	return typedTable[T]{source: aliased}, nil
}

// AsRead returns a queryable typed object under alias.
//
// `table` must not be nil.
func AsRead[T any](table ReadTable[T], alias string) (ReadTable[T], error) {
	if table == nil {
		return nil, fmt.Errorf("rasql: table alias: table must not be nil")
	}
	aliased, err := table.Ref().As(alias)
	if err != nil {
		return nil, fmt.Errorf("rasql: table alias: %w", err)
	}
	return readTable[T]{source: aliased}, nil
}

// ColumnRef is a reference to one column of one table. It is query.ColumnRef
// under a name generated code can reach without importing query.
type ColumnRef = query.ColumnRef

// Join is a table joined into a FROM clause. It is query.Join under a name
// generated code can reach without importing query.
type Join = query.Join

type BinaryOperator = query.BinaryOperator
type CaseWhen = query.CaseWhen
type Case = query.Case
type Cast = query.Cast
type Filter = query.Filter
type WindowFrame = query.WindowFrame
type WindowSpec = query.WindowSpec
type Over = query.Over
type Identifier = query.Identifier
type FragmentPart = query.FragmentPart
type TrustedFragment = query.TrustedFragment
type LockStrength = query.LockStrength
type LockWait = query.LockWait
type Lock = query.Lock

const (
	OperatorAdd        = query.OperatorAdd
	OperatorSubtract   = query.OperatorSubtract
	OperatorMultiply   = query.OperatorMultiply
	OperatorDivide     = query.OperatorDivide
	OperatorModulo     = query.OperatorModulo
	WindowRows         = query.WindowRows
	LockUpdate         = query.LockUpdate
	LockNoKeyUpdate    = query.LockNoKeyUpdate
	LockShare          = query.LockShare
	LockKeyShare       = query.LockKeyShare
	LockWaitDefault    = query.LockWaitDefault
	LockWaitNoWait     = query.LockWaitNoWait
	LockWaitSkipLocked = query.LockWaitSkipLocked
)

type RelationRef = query.RelationRef
type RelationSource = query.RelationSource
type ResultColumn = query.ResultColumn
type ResultQuery = query.ResultQuery
type QueryBody = query.QueryBody
type CTE = query.CTE
type Compound = query.Compound
type CompoundOperator = query.CompoundOperator

const (
	Union     = query.Union
	UnionAll  = query.UnionAll
	Intersect = query.Intersect
	Except    = query.Except
)

func Relation(table query.TableRef) query.RelationRef { return query.Relation(table) }

func Derived(result query.ResultQuery, alias string) (query.RelationRef, error) {
	return query.Derived(result, alias)
}

func ResultOf(body query.QueryBody, columns ...query.ResultColumn) (query.ResultQuery, error) {
	return query.ResultOf(body, columns...)
}

func CompoundQuery(left query.ResultQuery, operator query.CompoundOperator, right query.ResultQuery) (query.Compound, error) {
	return query.CompoundQuery(left, operator, right)
}

func CommonTable(name string, result query.ResultQuery) (query.CTE, error) {
	return query.CommonTable(name, result)
}

// Equal compares left and right for equality. It is query.Equal under a name
// generated code can reach without importing query.
func Equal(left any, right any) query.Binary {
	return query.Equal(left, right)
}

func Add(left any, right any) query.Binary      { return query.Add(left, right) }
func Subtract(left any, right any) query.Binary { return query.Subtract(left, right) }
func Multiply(left any, right any) query.Binary { return query.Multiply(left, right) }
func Divide(left any, right any) query.Binary   { return query.Divide(left, right) }
func Modulo(left any, right any) query.Binary   { return query.Modulo(left, right) }
func When(predicate query.Expression, result any) query.CaseWhen {
	return query.When(predicate, result)
}
func SearchedCase(branches ...query.CaseWhen) query.Case { return query.SearchedCase(branches...) }
func SimpleCase(operand any, branches ...query.CaseWhen) query.Case {
	return query.SimpleCase(operand, branches...)
}
func CastAs(expression any, target schema.Type) query.Cast { return query.CastAs(expression, target) }
func FilterWhere(aggregate query.Expression, predicate query.Expression) query.Filter {
	return query.FilterWhere(aggregate, predicate)
}
func Window(partition []query.Expression, order ...query.Order) query.WindowSpec {
	return query.Window(partition, order...)
}
func OverWindow(expression query.Expression, window query.WindowSpec) query.Over {
	return query.OverWindow(expression, window)
}
func Ident(name string) query.Identifier { return query.Ident(name) }
func Hole(value any) query.FragmentPart  { return query.Hole(value) }
func IdentifierHole(identifier query.Identifier) query.FragmentPart {
	return query.IdentifierHole(identifier)
}
func TrustedSQL(sql string, parts ...query.FragmentPart) query.TrustedFragment {
	return query.TrustedSQL(sql, parts...)
}
func RowLock(strength query.LockStrength) query.Lock { return query.RowLock(strength) }

// ColumnOf returns the named column of table. `table` must not be nil.
//
// A nil table returns the zero ColumnRef instead of an error, because a
// generated accessor on a zero wrapper passes that wrapper's nil embedded
// Table[T] here. The statement carrying the zero ColumnRef then reports
// query.ErrNilTable at Build rather than failing at the accessor call.
//
// A name the table does not hold is not that case: the returned ColumnRef keeps
// its source and its name, and the statement carrying it reports the name it
// could not find.
func ColumnOf[T any](table Table[T], name string) ColumnRef {
	if table == nil {
		return ColumnRef{}
	}
	return table.Column(name)
}

// Ref returns the dialect-neutral table backing the descriptor.
func (t typedTable[T]) Ref() query.TableRef {
	return t.source
}

// Column returns a reference to a named column of the table.
func (t typedTable[T]) Column(name string) ColumnRef {
	return t.source.Column(name)
}

func (t typedTable[T]) tableRow() T {
	var zero T
	return zero
}

// CreateTable renders and executes table's definition followed by its indexes.
// Callers that require atomic DDL pass a DB from Begin.
//
// `table` must not be nil.
func CreateTable[T any](ctx context.Context, db DB, table Table[T]) error {
	if table == nil {
		return fmt.Errorf("rasql: table must not be nil")
	}
	return createTableDef(ctx, db, table.Ref().Definition())
}

func createTableDef(ctx context.Context, db DB, table schema.TableDef) error {
	if !table.Supports(schema.OperationDDL) {
		return fmt.Errorf("rasql: object %q does not support DDL", table.QualifiedName())
	}
	if err := db.Validate(); err != nil {
		return err
	}
	statement, err := render.CreateTable(db.Dialect(), table)
	if err != nil {
		return fmt.Errorf("rasql: render CREATE TABLE: %w", err)
	}
	indexes, err := render.CreateIndexes(db.Dialect(), table)
	if err != nil {
		return fmt.Errorf("rasql: render CREATE INDEX: %w", err)
	}
	if _, err := db.ExecRendered(ctx, statement); err != nil {
		return fmt.Errorf("rasql: execute CREATE TABLE: %w", err)
	}
	for _, index := range indexes {
		if _, err := db.ExecRendered(ctx, index); err != nil {
			return fmt.Errorf("rasql: execute CREATE INDEX: %w", err)
		}
	}
	return nil
}

