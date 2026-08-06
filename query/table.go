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
	"fmt"

	"github.com/lestrrat-go/rasql/schema"
)

// Table identifies a table used by a statement.
type Table struct {
	definition schema.Table
	alias      string
}

// NewTable validates definition and returns a table for it.
func NewTable(definition schema.Table) (Table, error) {
	if err := definition.Validate(); err != nil {
		return Table{}, fmt.Errorf("query table: %w", err)
	}
	return Table{definition: definition.Clone()}, nil
}

// MustNewTable returns a table for definition or panics when definition is invalid.
// It is intended for generated or otherwise static schema descriptors.
func MustNewTable(definition schema.Table) Table {
	table, err := NewTable(definition)
	if err != nil {
		panic(fmt.Sprintf("query table: %s", err))
	}
	return table
}

// As returns a copy of t with alias as its SQL alias.
func (t Table) As(alias string) (Table, error) {
	if err := t.validate(); err != nil {
		return Table{}, err
	}
	if err := schema.ValidateIdentifier(alias); err != nil {
		return Table{}, fmt.Errorf("query table alias: %w", err)
	}
	aliased := t
	aliased.alias = alias
	return aliased, nil
}

// Name returns the underlying table name.
func (t Table) Name() string {
	return t.definition.Name
}

// Alias returns the SQL alias, or an empty string when the table is unaliased.
func (t Table) Alias() string {
	return t.alias
}

// Qualifier returns the identifier used to qualify a column.
func (t Table) Qualifier() string {
	if t.alias != "" {
		return t.alias
	}
	return t.definition.Name
}

// Schema returns the schema qualifying the table, or an empty string when the
// table is unqualified.
func (t Table) Schema() string {
	return t.definition.Schema
}

// Qualified reports whether the underlying descriptor names a schema. It
// describes the table itself, so an alias does not change it: an aliased
// qualified table is still qualified even though its columns render under the
// alias alone. Read QualifierSchema instead to learn what, if anything,
// qualifies a rendered column reference.
func (t Table) Qualified() bool {
	return t.definition.Qualified()
}

// QualifierSchema returns the schema that qualifies Qualifier, or an empty
// string when the table is aliased or unqualified. An alias replaces a
// table's whole qualified name, so an aliased table's columns are never
// schema-qualified.
func (t Table) QualifierSchema() string {
	if t.alias != "" {
		return ""
	}
	return t.definition.Schema
}

// QualifiedName returns the table's name for an error message: the alias
// when the table is aliased, "schema.name" when it is qualified, and "name"
// otherwise. It is never a SQL identifier.
func (t Table) QualifiedName() string {
	if t.alias != "" {
		return t.alias
	}
	return t.definition.QualifiedName()
}

// Definition returns a copy of the underlying schema descriptor.
func (t Table) Definition() schema.Table {
	return t.definition.Clone()
}

// Column returns a reference to a named column in t.
func (t Table) Column(name string) (Column, error) {
	if err := t.validate(); err != nil {
		return Column{}, err
	}
	if _, ok := t.definition.Column(name); !ok {
		return Column{}, fmt.Errorf("query column: table %q has no column %q", t.QualifiedName(), name)
	}
	return Column{source: t, name: name}, nil
}

func (t Table) validate() error {
	if err := t.definition.Validate(); err != nil {
		return fmt.Errorf("query table: %w", err)
	}
	if t.alias == "" {
		return nil
	}
	if err := schema.ValidateIdentifier(t.alias); err != nil {
		return fmt.Errorf("query table alias: %w", err)
	}
	return nil
}

func (t Table) key() string {
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

func (t Table) reference() sourceReference {
	return sourceReference{
		qualifier:  t.Qualifier(),
		schema:     t.QualifierSchema(),
		descriptor: t.definition.QualifiedName(),
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
