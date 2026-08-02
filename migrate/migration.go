package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// Migration is one ordered, forward-only DDL change.
type Migration struct {
	ID         string
	Operations []Operation
}

// Operation is one structured DDL action supported by a Migration.
type Operation interface {
	migrationOperation()
}

// CreateTable creates a complete table definition and its declared indexes.
type CreateTable struct {
	Table schema.Table
}

func (CreateTable) migrationOperation() {}

// AddColumn adds a nullable or defaulted column to an existing table.
// A non-null column without a default is rejected because existing rows could not satisfy it.
type AddColumn struct {
	Table  string
	Column schema.Column
}

func (AddColumn) migrationOperation() {}

// CreateIndex creates one index on an existing table.
type CreateIndex struct {
	Table string
	Index schema.Index
}

func (CreateIndex) migrationOperation() {}

// DropIndex removes one index from an existing table.
type DropIndex struct {
	Table string
	Name  string
}

func (DropIndex) migrationOperation() {}

// Render validates migration and returns its DDL statements for d.
func (m Migration) Render(d dialect.Dialect) ([]render.Statement, error) {
	if d == nil {
		return nil, fmt.Errorf("migrate: dialect must not be nil")
	}
	if err := m.validate(); err != nil {
		return nil, err
	}
	statements := make([]render.Statement, 0, len(m.Operations))
	for index, operation := range m.Operations {
		rendered, err := renderOperation(d, operation)
		if err != nil {
			return nil, fmt.Errorf("migrate: render migration %q operation %d: %w", m.ID, index+1, err)
		}
		statements = append(statements, rendered...)
	}
	return statements, nil
}

func (m Migration) validate() error {
	if err := validateMigrationID(m.ID); err != nil {
		return err
	}
	if len(m.Operations) == 0 {
		return fmt.Errorf("migrate: migration %q must contain at least one operation", m.ID)
	}
	for index, operation := range m.Operations {
		if operation == nil {
			return fmt.Errorf("migrate: migration %q operation %d must not be nil", m.ID, index+1)
		}
		switch operation := operation.(type) {
		case CreateTable:
			if err := operation.Table.Validate(); err != nil {
				return fmt.Errorf("migrate: migration %q create table: %w", m.ID, err)
			}
		case AddColumn:
			if err := validateAddColumn(operation); err != nil {
				return fmt.Errorf("migrate: migration %q add column: %w", m.ID, err)
			}
		case CreateIndex:
			if err := validateCreateIndex(operation); err != nil {
				return fmt.Errorf("migrate: migration %q create index: %w", m.ID, err)
			}
		case DropIndex:
			if err := schema.ValidateIdentifier(operation.Table); err != nil {
				return fmt.Errorf("migrate: migration %q drop index table: %w", m.ID, err)
			}
			if err := schema.ValidateIdentifier(operation.Name); err != nil {
				return fmt.Errorf("migrate: migration %q drop index name: %w", m.ID, err)
			}
		default:
			return fmt.Errorf("migrate: migration %q operation %d has unsupported type %T", m.ID, index+1, operation)
		}
	}
	return nil
}

func validateMigrationID(id string) error {
	if id == "" {
		return fmt.Errorf("migrate: migration ID must not be empty")
	}
	if !utf8.ValidString(id) || len(id) > 255 || strings.ContainsRune(id, '\x00') {
		return fmt.Errorf("migrate: migration ID %q is invalid", id)
	}
	return nil
}

func validateAddColumn(operation AddColumn) error {
	if err := schema.ValidateIdentifier(operation.Table); err != nil {
		return fmt.Errorf("table: %w", err)
	}
	table := schema.Table{Name: operation.Table, Columns: []schema.Column{operation.Column}}
	if err := table.Validate(); err != nil {
		return err
	}
	if !operation.Column.Nullable && operation.Column.Default == "" {
		return fmt.Errorf("column %q must be nullable or have a default", operation.Column.Name)
	}
	return nil
}

func validateCreateIndex(operation CreateIndex) error {
	if err := schema.ValidateIdentifier(operation.Table); err != nil {
		return fmt.Errorf("table: %w", err)
	}
	columns := make([]schema.Column, len(operation.Index.Columns))
	for index, name := range operation.Index.Columns {
		columns[index] = schema.Column{Name: name, Type: schema.TypeBoolean}
	}
	table := schema.Table{Name: operation.Table, Columns: columns, Indexes: []schema.Index{operation.Index}}
	return table.Validate()
}

func renderOperation(d dialect.Dialect, operation Operation) ([]render.Statement, error) {
	switch operation := operation.(type) {
	case CreateTable:
		table, err := render.CreateTable(d, operation.Table)
		if err != nil {
			return nil, err
		}
		indexes, err := render.CreateIndexes(d, operation.Table)
		if err != nil {
			return nil, err
		}
		return append([]render.Statement{table}, indexes...), nil
	case AddColumn:
		return renderAddColumn(d, operation)
	case CreateIndex:
		return renderCreateIndex(d, operation)
	case DropIndex:
		return renderDropIndex(d, operation)
	default:
		return nil, fmt.Errorf("unsupported operation type %T", operation)
	}
}

func renderAddColumn(d dialect.Dialect, operation AddColumn) ([]render.Statement, error) {
	table, err := d.QuoteIdentifier(operation.Table)
	if err != nil {
		return nil, err
	}
	name, err := d.QuoteIdentifier(operation.Column.Name)
	if err != nil {
		return nil, err
	}
	typeName, err := d.TypeName(operation.Column.Type)
	if err != nil {
		return nil, err
	}
	definition := name + " " + typeName
	if !operation.Column.Nullable {
		definition += " NOT NULL"
	}
	if operation.Column.Default != "" {
		definition += " DEFAULT " + operation.Column.Default
	}
	statement, err := render.Precompiled("ALTER TABLE " + table + " ADD COLUMN " + definition)
	if err != nil {
		return nil, err
	}
	return []render.Statement{statement}, nil
}

func renderCreateIndex(d dialect.Dialect, operation CreateIndex) ([]render.Statement, error) {
	name, err := d.QuoteIdentifier(operation.Index.Name)
	if err != nil {
		return nil, err
	}
	table, err := d.QuoteIdentifier(operation.Table)
	if err != nil {
		return nil, err
	}
	columns := make([]string, len(operation.Index.Columns))
	for index, column := range operation.Index.Columns {
		quoted, err := d.QuoteIdentifier(column)
		if err != nil {
			return nil, err
		}
		columns[index] = quoted
	}
	sql := "CREATE "
	if operation.Index.Unique {
		sql += "UNIQUE "
	}
	sql += "INDEX " + name + " ON " + table + " (" + strings.Join(columns, ", ") + ")"
	statement, err := render.Precompiled(sql)
	if err != nil {
		return nil, err
	}
	return []render.Statement{statement}, nil
}

func renderDropIndex(d dialect.Dialect, operation DropIndex) ([]render.Statement, error) {
	name, err := d.QuoteIdentifier(operation.Name)
	if err != nil {
		return nil, err
	}
	sql := "DROP INDEX " + name
	if d.Name() == "mysql" {
		table, err := d.QuoteIdentifier(operation.Table)
		if err != nil {
			return nil, err
		}
		sql += " ON " + table
	}
	statement, err := render.Precompiled(sql)
	if err != nil {
		return nil, err
	}
	return []render.Statement{statement}, nil
}

func checksum(statements []render.Statement) string {
	hash := sha256.New()
	for _, statement := range statements {
		hash.Write([]byte(statement.SQL()))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
