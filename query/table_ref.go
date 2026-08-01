// Package query defines dialect-neutral SQL statements and expressions.
package query

import (
	"fmt"

	"github.com/lestrrat-go/rasql/schema"
)

// TableRef identifies a table used by a statement.
type TableRef struct {
	table schema.Table
	alias string
}

// NewTableRef validates table and returns a reference to it.
func NewTableRef(table schema.Table) (TableRef, error) {
	if err := table.Validate(); err != nil {
		return TableRef{}, fmt.Errorf("query table reference: %w", err)
	}
	return TableRef{table: table.Clone()}, nil
}

// MustNewTableRef returns a table reference for table or panics when table is invalid.
// It is intended for generated or otherwise static schema descriptors.
func MustNewTableRef(table schema.Table) TableRef {
	reference, err := NewTableRef(table)
	if err != nil {
		panic(fmt.Sprintf("query table reference: %s", err))
	}
	return reference
}

// As returns a copy of t with alias as its SQL alias.
func (t TableRef) As(alias string) (TableRef, error) {
	if err := t.validate(); err != nil {
		return TableRef{}, err
	}
	if err := schema.ValidateIdentifier(alias); err != nil {
		return TableRef{}, fmt.Errorf("query table alias: %w", err)
	}
	aliasRef := t
	aliasRef.alias = alias
	return aliasRef, nil
}

// Name returns the underlying table name.
func (t TableRef) Name() string {
	return t.table.Name
}

// Alias returns the SQL alias, or an empty string when the table is unaliased.
func (t TableRef) Alias() string {
	return t.alias
}

// Definition returns a copy of the underlying schema descriptor.
func (t TableRef) Definition() schema.Table {
	return t.table.Clone()
}

// Qualifier returns the identifier used to qualify a column.
func (t TableRef) Qualifier() string {
	if t.alias != "" {
		return t.alias
	}
	return t.table.Name
}

// Column returns a reference to a named column in t.
func (t TableRef) Column(name string) (Column, error) {
	if err := t.validate(); err != nil {
		return Column{}, err
	}
	if _, ok := t.table.Column(name); !ok {
		return Column{}, fmt.Errorf("query column: table %q has no column %q", t.table.Name, name)
	}
	return Column{source: t, name: name}, nil
}

func (t TableRef) validate() error {
	if err := t.table.Validate(); err != nil {
		return fmt.Errorf("query table reference: %w", err)
	}
	if t.alias == "" {
		return nil
	}
	if err := schema.ValidateIdentifier(t.alias); err != nil {
		return fmt.Errorf("query table alias: %w", err)
	}
	return nil
}

func (t TableRef) key() string {
	return t.table.Name + "\x00" + t.alias
}
