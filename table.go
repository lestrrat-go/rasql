package rasql

import (
	"context"
	"fmt"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// Table names one catalog object and the Go type of one of its rows. A view is
// a Table whose descriptor states Kind view, the same way schema.TableDef and
// query.TableRef describe a view; which operations the object permits is a
// run-time property of the descriptor, and every entry point that performs one
// checks its own bit.
//
// Nothing outside this package can build a Table that carries a table: the
// descriptor lives in an unexported field, and TableOf, MustTableOf and
// TableFrom are the only ways to fill it.
//
// The zero Table carries no descriptor. Every method and every entry point
// taking one reports an error wrapping query.ErrNilTable for it, so a generated
// wrapper holding a zero Table -- the wrapper a failed generated As returns
// beside its error -- reports that error rather than panicking.
type Table[T any] struct {
	ref query.TableRef
}

// TableOf creates a typed table from a validated schema definition. It reports
// an error when definition is not a valid descriptor, and checks nothing about
// which operations the descriptor permits: NewCreatePlan, NewPatchPlan,
// NewDeletePlan, CreateTable and Table.Source each check the one bit they need
// when the statement is built.
func TableOf[T any](definition schema.TableDef) (Table[T], error) {
	source, err := query.NewTableRef(definition)
	if err != nil {
		return Table[T]{}, fmt.Errorf("rasql: table definition: %w", err)
	}
	return Table[T]{ref: source}, nil
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
// valid, without validating or copying it. rasqlgen validates every descriptor
// it emits, so a store that validated again at package initialization would
// re-derive a result that cannot change, and would pay one identifier check per
// column plus a copy of every slice in the descriptor to do it.
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
	return Table[T]{ref: query.TableRefFrom(definition)}
}

// Ref returns the dialect-neutral table behind t. It is the one exported way
// out of a generated wrapper that holds its Table in an unexported field, and
// it reaches query.TableRef's own descriptor accessors, CreateTable and the
// dynamic builders.
func (t Table[T]) Ref() query.TableRef { return t.ref }

// Column returns a reference to the named column of t.
//
// It reports no error, because a generated accessor returns a ColumnRef alone.
// A zero Table gives back a ColumnRef over a source that carries no table, and
// the statement carrying it reports query.ErrNilTable at Build rather than
// failing at the accessor call; that is the path a generated accessor on a zero
// wrapper takes.
//
// A name the table does not hold is a different case: the returned ColumnRef
// keeps its source and its name, and the statement carrying it reports the name
// it could not find.
func (t Table[T]) Column(name string) ColumnRef { return t.ref.Column(name) }

// As returns t under alias. Generated table types have their own As returning
// the generated wrapper; this one is what that method calls and what dynamic
// code calls directly.
func (t Table[T]) As(alias string) (Table[T], error) {
	aliased, err := t.ref.As(alias)
	if err != nil {
		return Table[T]{}, fmt.Errorf("rasql: table alias: %w", err)
	}
	return Table[T]{ref: aliased}, nil
}

// InSchema returns t in namespace: a PostgreSQL schema, a MySQL database, or a
// SQLite attached-database name. It is what a caller reaches for when the
// namespace a store was generated against is not the one the application runs
// against, such as a MySQL deployment giving each tenant its own database.
//
// It carries query.TableRef.InSchema's contract, including that an empty
// namespace is an error and that no foreign key's ReferencedSchema moves with
// the table.
func (t Table[T]) InSchema(namespace string) (Table[T], error) {
	moved, err := t.ref.InSchema(namespace)
	if err != nil {
		return Table[T]{}, fmt.Errorf("rasql: table schema: %w", err)
	}
	return Table[T]{ref: moved}, nil
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

type ResultColumn = query.ResultColumn
type CompoundOperator = query.CompoundOperator

const (
	Union     = query.Union
	UnionAll  = query.UnionAll
	Intersect = query.Intersect
	Except    = query.Except
)

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

// CatalogObject names one object a server holds, and hands back the
// query.TableRef describing it. rasql.Table[T] is one, and so is a generated
// table wrapper, which promotes Ref from the handle it holds.
//
// The name says what the thing is: a table or a view the catalog already
// carries, which a server can be asked to create and a statement can read. A
// derived query and a CTE reference are Relations and not CatalogObjects,
// because each names a result the statement computes rather than an object the
// catalog holds; neither has a descriptor to create.
//
// Only this package implements it, through the unexported method Relation
// carries.
type CatalogObject interface {
	Relation
	Ref() query.TableRef
}

// CreateTable renders and executes table's definition followed by its indexes.
// Callers that require atomic DDL pass a DB from Begin.
//
// It takes the object itself, so rasql.CreateTable(ctx, db, store.Users())
// compiles and no caller reaches for the ref. A descriptor that does not permit
// schema.OperationDDL is refused here, and that check is the only guard.
func CreateTable(ctx context.Context, db DB, table CatalogObject) error {
	ref := table.Ref()
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("rasql: create table: %w", err)
	}
	return createTableDef(ctx, db, ref.Definition())
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
	if _, err := db.execRendered(ctx, statement); err != nil {
		return fmt.Errorf("rasql: execute CREATE TABLE: %w", err)
	}
	for _, index := range indexes {
		if _, err := db.execRendered(ctx, index); err != nil {
			return fmt.Errorf("rasql: execute CREATE INDEX: %w", err)
		}
	}
	return nil
}
