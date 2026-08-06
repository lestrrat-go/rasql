// Package query defines dialect-neutral SQL statements and expressions.
//
// # Parameter limits
//
// Every [Bind] value renders as its own placeholder and costs one bound
// parameter, so a statement's parameter count follows the bound values it
// carries: a list of N bound values given to [In] or [NotIn] costs N, and
// [NewInsertRows] over R rows of C columns costs R*C. The database caps that
// count, at 65535 for PostgreSQL and MySQL and at 32766 for SQLite through
// modernc.org/sqlite. This package neither counts parameters nor enforces the
// cap, so a statement over it builds and renders without complaint and fails
// when the database executes it. Keep a statement under the cap by splitting
// the work into several statements, such as inserting a large row set in
// chunks, or by replacing a large value list with [InSelect] or [NotInSelect],
// which cost no parameter per candidate value.
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
		return Column{}, fmt.Errorf("query column: table %q has no column %q", t.definition.Name, name)
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
	return t.definition.Name + "\x00" + t.alias
}
