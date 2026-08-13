// Package query defines dialect-neutral SQL statements and expressions.
//
// # Parameter limits
//
// A rendered statement's parameter count is the number of [Bind] values it
// carries, plus one for a LIMIT and one for an OFFSET. Every [Bind] value
// renders as its own placeholder and costs one parameter, while an expression
// that binds nothing, such as a column reference, costs none. A LIMIT and an
// OFFSET each bind a placeholder of their own, so [Select.WithLimit] and
// [Select.WithOffset] add one parameter each even when the statement holds no
// [Bind] at all.
//
// A list of N bound values given to [In] or [NotIn] therefore costs N.
// [NewInsertRows] over R rows of C columns costs R*C only when every row value
// is a single [Bind]. A row value may be any [Expression], and one that is not
// a single [Bind] costs however many [Bind] values are nested inside it: a
// column reference costs none, and Equal(Bind(x), Bind(y)) costs two.
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
	definition *schema.TableDef
	alias      string
}

// ErrNilTable is what a zero TableRef reports. A TableRef is constructed by
// NewTableRef, MustTableRef or TableRefFrom; the zero value is the only one
// that carries no table.
var ErrNilTable = errors.New("query table: table must not be nil")

// NewTableRef validates definition and returns a table for it.
func NewTableRef(definition schema.TableDef) (TableRef, error) {
	if err := definition.Validate(); err != nil {
		return TableRef{}, fmt.Errorf("query table: %w", err)
	}
	clone := definition.Clone()
	return TableRef{definition: &clone}, nil
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
func TableRefFrom(definition schema.TableDef) TableRef {
	return TableRef{definition: &definition}
}

// def returns the descriptor behind t, or a zero descriptor when t is the zero
// TableRef. The cold string accessors read through it; Column, key and
// Qualifier test the pointer directly rather than copy the whole descriptor.
func (t TableRef) def() schema.TableDef {
	if t.definition == nil {
		return schema.TableDef{}
	}
	return *t.definition
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
	if t.definition == nil {
		return ""
	}
	return t.definition.Name
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
func (t TableRef) Column(name string) (ColumnRef, error) {
	if t.definition == nil {
		return ColumnRef{}, ErrNilTable
	}
	if _, ok := t.definition.Column(name); !ok {
		return ColumnRef{}, fmt.Errorf("query column: table %q has no column %q", t.QualifiedName(), name)
	}
	return ColumnRef{source: t, name: name}, nil
}

// column looks a column up on t's descriptor. It exists so the package's other
// files can read the descriptor without dereferencing a possibly-nil pointer.
func (t TableRef) column(name string) (schema.ColumnDef, bool) {
	if t.definition == nil {
		return schema.ColumnDef{}, false
	}
	return t.definition.Column(name)
}

func (t TableRef) validate() error {
	if t.definition == nil {
		return ErrNilTable
	}
	return nil
}

func (t TableRef) key() string {
	if t.definition == nil {
		return "\x00\x00" + t.alias
	}
	return t.definition.Schema + "\x00" + t.definition.Name + "\x00" + t.alias
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
