// Package query defines dialect-neutral SQL statements and expressions.
//
// # Parameter limits
//
// A rendered statement's parameter count is the number of bound values it
// carries, however they were written, plus one for a LIMIT and one for an
// OFFSET. A plain Go value passed to a comparison, a membership test, [Set],
// an insert row, [Call], [Func], or [Coalesce] becomes one, the same as an
// explicit [Bind]; each renders as its own placeholder and costs one
// parameter, while an expression that binds nothing, such as a column
// reference, costs none. A LIMIT and an OFFSET each bind a placeholder of
// their own, so [Select.WithLimit] and [Select.WithOffset] add one parameter
// each even when the statement holds no bound value at all.
//
// A list of N bound values given to [In] or [NotIn] therefore costs N.
// [NewInsertRows] over R rows of C columns costs R*C only when every row
// value is a plain Go value or a single [Bind]. A row value may be any
// [Expression], and one that is not a plain value or a single [Bind] costs
// however many bound values are nested inside it: a column reference costs
// none, and Equal(Bind(x), Bind(y)) costs two.
//
// The database caps that count, at 65535 for PostgreSQL and MySQL and at 32766
// for SQLite through modernc.org/sqlite. This package neither counts parameters
// nor enforces the cap, so a statement over it builds and renders without
// complaint and fails when the database executes it. Keep a statement under the
// cap by splitting the work into several statements, such as inserting a large
// row set in chunks, or by replacing a large value list with [InSelect] or
// [NotInSelect], which cost no parameter per candidate value.
package query

import (
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/schema"
)

// TableRef identifies a table used by a statement.
//
// A TableRef built by NewTableRef, MustTableRef or TableRefFrom holds a pointer
// to its descriptor, so the zero TableRef is the only invalid one. It is
// comparable, but == compares that pointer and not the table: two refs built
// from the same descriptor are never ==. Compare tables through key() instead.
type TableRef struct {
	descriptor *tableDescriptor
	alias      string
}

// tableDescriptor is what a TableRef points at: the schema descriptor plus the
// column index built for it once, when the ref is constructed. Everything a ref
// needs beyond its alias lives here rather than on TableRef itself, so that
// TableRef keeps one pointer field and stays comparable.
type tableDescriptor struct {
	definition schema.TableDef
	// columns maps a column name to its position in definition.Columns, so that
	// a lookup costs the same at any table width and at any position in the
	// table. A descriptor may repeat a column name, because TableRefFrom
	// validates nothing, so an entry keeps the first position schema.TableDef's
	// own scan would have found.
	columns map[string]int
	// owned reports whether definition.Columns is private to this descriptor,
	// which it is for the clone NewTableRef indexes. An owned descriptor cannot
	// change under the index, so the index answers a lookup outright, including
	// a lookup for a name the table does not have. TableRefFrom reads the
	// caller's slices in place, so its columns can be renamed after the index is
	// built; see column for what that costs.
	owned bool
}

// newTableDescriptor indexes definition's columns and returns the descriptor a
// TableRef points at. Set owned when definition's slices are private to the new
// descriptor, which is to say when the caller cloned it first.
func newTableDescriptor(definition schema.TableDef, owned bool) *tableDescriptor {
	columns := make(map[string]int, len(definition.Columns))
	for i, column := range definition.Columns {
		if _, seen := columns[column.Name]; seen {
			continue
		}
		columns[column.Name] = i
	}
	return &tableDescriptor{definition: definition, columns: columns, owned: owned}
}

// column returns the column named name and whether d has it.
//
// An owned descriptor is answered by the index alone, hit or miss. An unowned
// one is the descriptor TableRefFrom took from the caller, whose columns the
// caller can still rename in place, so the index is only a hint there: a hit
// counts when the indexed column still carries name, and anything else falls
// back to the scan schema.TableDef.Column does over the live columns. A hit on
// an unowned descriptor therefore still costs one map lookup, while a name the
// index does not know costs the scan, because a column renamed to that name
// since and a column that was never there look alike until something walks the
// columns.
func (d *tableDescriptor) column(name string) (schema.ColumnDef, bool) {
	if i, indexed := d.columns[name]; indexed {
		column := d.definition.Columns[i]
		if d.owned || column.Name == name {
			return column, true
		}
	}
	if d.owned {
		return schema.ColumnDef{}, false
	}
	return d.definition.Column(name)
}

// ErrNilTable is what a zero TableRef reports. A TableRef is constructed by
// NewTableRef, MustTableRef or TableRefFrom; the zero value is the only one
// that carries no table.
var ErrNilTable = errors.New("query table: table must not be nil")

// NewTableRef validates definition and returns a table for it.
//
// It clones definition and indexes the clone's columns once, so that Column
// costs one map lookup however wide the table is and wherever the column sits
// in it. Nothing outside the returned ref can reach that clone, so the index
// answers a lookup for an absent column too.
func NewTableRef(definition schema.TableDef) (TableRef, error) {
	if err := definition.Validate(); err != nil {
		return TableRef{}, fmt.Errorf("query table: %w", err)
	}
	return TableRef{descriptor: newTableDescriptor(definition.Clone(), true)}, nil
}

// MustTableRef returns a table for definition or panics when definition is invalid.
// It is intended for generated or otherwise static schema descriptors.
func MustTableRef(definition schema.TableDef) TableRef {
	table, err := NewTableRef(definition)
	if err != nil {
		panic(fmt.Sprintf("query table: %s", err))
	}
	return table
}

// TableRefFrom returns a table for definition without validating it and without
// copying it.
//
// It is the trusting counterpart to NewTableRef, for a descriptor whose validity
// is already established: rasqlgen runs generate.Validate over every descriptor
// it emits, so a generated table is validated before it is ever compiled.
//
// It validates nothing. Every dialect this module ships re-validates each
// identifier at render time through Dialect.QuoteIdentifier and returns an error
// rather than escaping it, so an unvalidated descriptor cannot smuggle SQL
// through a rendered identifier. It can still describe a table the server will
// reject, and it can describe one the server accepts but the caller did not mean,
// such as a descriptor repeating a column name. A caller assembling a descriptor
// at runtime, or reading one from configuration, wants NewTableRef.
//
// It clones nothing. The returned TableRef reads the descriptor's slices in
// place, so mutating definition.Columns, definition.PrimaryKey or any other
// slice after this call changes what the ref reports. Assigning to a scalar field
// of the caller's own variable does not, because definition is passed by value.
// Treat the descriptor as frozen once it is handed over.
//
// It does index the descriptor's column names once, so that Column costs one
// map lookup however wide the table is and wherever the column sits in it. That
// index is a hint rather than the answer here, precisely because the columns are
// still the caller's to mutate: a lookup it cannot answer falls back to a scan
// of the live columns, so a column renamed after this call is still found under
// its new name and no longer found under its old one.
func TableRefFrom(definition schema.TableDef) TableRef {
	return TableRef{descriptor: newTableDescriptor(definition, false)}
}

// def returns the descriptor behind t, or a zero descriptor when t is the zero
// TableRef. The cold string accessors read through it; Column, key and
// Qualifier test the pointer directly rather than copy the whole descriptor.
func (t TableRef) def() schema.TableDef {
	if t.descriptor == nil {
		return schema.TableDef{}
	}
	return t.descriptor.definition
}

// As returns a copy of t with alias as its SQL alias.
func (t TableRef) As(alias string) (TableRef, error) {
	if err := t.validate(); err != nil {
		return TableRef{}, err
	}
	if err := schema.ValidateIdentifier(alias); err != nil {
		return TableRef{}, fmt.Errorf("query table alias: %w", err)
	}
	aliased := t
	aliased.alias = alias
	return aliased, nil
}

// Name returns the underlying table name.
func (t TableRef) Name() string {
	return t.def().Name
}

// Alias returns the SQL alias, or an empty string when the table is unaliased.
func (t TableRef) Alias() string {
	return t.alias
}

// Qualifier returns the identifier used to qualify a column.
func (t TableRef) Qualifier() string {
	if t.alias != "" {
		return t.alias
	}
	if t.descriptor == nil {
		return ""
	}
	return t.descriptor.definition.Name
}

// Schema returns the schema qualifying the table, or an empty string when the
// table is unqualified.
func (t TableRef) Schema() string {
	return t.def().Schema
}

// Qualified reports whether the underlying descriptor names a schema. It
// describes the table itself, so an alias does not change it: an aliased
// qualified table is still qualified even though its columns render under the
// alias alone. Read QualifierSchema instead to learn what, if anything,
// qualifies a rendered column reference.
func (t TableRef) Qualified() bool {
	return t.def().Qualified()
}

// QualifierSchema returns the schema that qualifies Qualifier, or an empty
// string when the table is aliased or unqualified. An alias replaces a
// table's whole qualified name, so an aliased table's columns are never
// schema-qualified.
func (t TableRef) QualifierSchema() string {
	if t.alias != "" {
		return ""
	}
	return t.def().Schema
}

// QualifiedName returns the table's name for an error message: the alias
// when the table is aliased, "schema.name" when it is qualified, and "name"
// otherwise. It is never a SQL identifier.
func (t TableRef) QualifiedName() string {
	if t.alias != "" {
		return t.alias
	}
	return t.def().QualifiedName()
}

// Definition returns a copy of the underlying schema descriptor.
func (t TableRef) Definition() schema.TableDef {
	return t.def().Clone()
}

// Column returns a reference to a named column in t.
//
// It reports no error. Every statement validates the columns it carries against
// the tables it selects from, so a name t does not hold is caught there, together
// with the separate check that t is one of those tables. The returned ColumnRef
// keeps t as its source and name as its name whether or not t holds the column,
// so the statement's error names both.
//
// ColumnRef.Validate runs the same existence check on its own, for a caller
// holding a name it only learns while the program runs.
func (t TableRef) Column(name string) ColumnRef {
	return ColumnRef{source: t, name: name}
}

// Identifier returns an expression for t's own name, rendered as a bare
// quoted identifier with no schema and no column. It is meaningless in
// ordinary SQL; build one only for a construct that specifically reads a
// table's own name in expression position, such as query.Match or
// query.BM25 for SQLite's FTS5 module.
func (t TableRef) Identifier() TableIdentifier {
	return TableIdentifier{table: t}
}

// column looks a column up on t's descriptor. It exists so the package's other
// files can read the descriptor without dereferencing a possibly-nil pointer.
func (t TableRef) column(name string) (schema.ColumnDef, bool) {
	if t.descriptor == nil {
		return schema.ColumnDef{}, false
	}
	return t.descriptor.column(name)
}

func (t TableRef) validate() error {
	if t.descriptor == nil {
		return ErrNilTable
	}
	return nil
}

func (t TableRef) key() string {
	if t.descriptor == nil {
		return "\x00\x00" + t.alias
	}
	return t.descriptor.definition.Schema + "\x00" + t.descriptor.definition.Name + "\x00" + t.alias
}

// sourceReference describes how a statement addresses one of its tables, which
// is a different thing from the identity key() carries. Two descriptors that
// differ still collide when a server resolves a column reference to both of
// them, and the key cannot see that because it is built from the parts that
// make the two descriptors different in the first place.
type sourceReference struct {
	// qualifier is the leading identifier a column reference carries: the
	// alias when the table is aliased, and the bare table name otherwise.
	qualifier string
	// schema qualifies qualifier in rendered SQL, and is empty when the table
	// is aliased or unqualified.
	schema string
	// descriptor names the table for an error message, ignoring any alias, so
	// that a message about two tables sharing one alias can still tell them
	// apart.
	descriptor string
}

func (t TableRef) reference() sourceReference {
	return sourceReference{
		qualifier:  t.Qualifier(),
		schema:     t.QualifierSchema(),
		descriptor: t.def().QualifiedName(),
	}
}

// conflicts reports whether a server could resolve one column reference to both
// sources.
//
// Two sources that render their columns under different leading identifiers are
// always distinguishable, so they never conflict. When the leading identifier is
// the same, the only thing that can still tell them apart is a schema rendered
// in front of it, and that works only when both sources carry one: rasql renders
// every column of an unaliased qualified table as "schema"."table"."column", so
// tenant_a.users and tenant_b.users each answer to their own prefix. If either
// side renders a bare identifier, the reference is ambiguous, because a bare
// users.id names the unqualified users table and the qualified tenant_a.users
// table equally, and an alias is bare by construction.
func (r sourceReference) conflicts(other sourceReference) bool {
	if r.qualifier != other.qualifier {
		return false
	}
	if r.schema != "" && other.schema != "" {
		return r.schema == other.schema
	}
	return true
}
