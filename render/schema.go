package render

import (
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
)

// CreateTable renders a CREATE TABLE statement for table.
func CreateTable(d dialect.Dialect, table schema.Table) (Statement, error) {
	if isNilDialect(d) {
		return Statement{}, &Error{Err: fmt.Errorf("dialect must not be nil")}
	}
	if err := table.Validate(); err != nil {
		return Statement{}, &Error{Dialect: d.Name(), Err: fmt.Errorf("invalid table: %w", err)}
	}
	renderer := renderer{dialect: d}
	if err := renderer.writeCreateTable(table); err != nil {
		return Statement{}, &Error{Dialect: d.Name(), Err: err}
	}
	return Statement{sql: renderer.builder.String()}, nil
}

// CreateIndexes renders the CREATE INDEX statements for table.
func CreateIndexes(d dialect.Dialect, table schema.Table) ([]Statement, error) {
	if isNilDialect(d) {
		return nil, &Error{Err: fmt.Errorf("dialect must not be nil")}
	}
	if err := table.Validate(); err != nil {
		return nil, &Error{Dialect: d.Name(), Err: fmt.Errorf("invalid table: %w", err)}
	}
	statements := make([]Statement, len(table.Indexes))
	for i, index := range table.Indexes {
		renderer := renderer{dialect: d}
		if err := renderer.writeCreateIndex(table, index); err != nil {
			return nil, &Error{Dialect: d.Name(), Err: err}
		}
		statements[i] = Statement{sql: renderer.builder.String()}
	}
	return statements, nil
}

func (r *renderer) writeCreateTable(table schema.Table) error {
	name, err := r.quoteIdentifier(table.Name)
	if err != nil {
		return err
	}
	r.builder.WriteString("CREATE TABLE ")
	r.builder.WriteString(name)
	r.builder.WriteString(" (")

	definitions := make([]string, 0, len(table.Columns)+len(table.UniqueConstraints)+len(table.Checks)+len(table.ForeignKeys)+1)
	for _, column := range table.Columns {
		definition, err := r.columnDefinition(column)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	primaryKeySuffix := ""
	if len(table.PrimaryKey) > 0 {
		columns, err := r.quotedNames(table.PrimaryKey)
		if err != nil {
			return err
		}
		primaryKey := "PRIMARY KEY (" + strings.Join(columns, ", ") + ")"
		if r.dialect.TablePrimaryKeyStyle() == dialect.PrimaryKeySuffix {
			primaryKeySuffix = " " + primaryKey
		} else {
			definitions = append(definitions, primaryKey)
		}
	}
	for _, constraint := range table.UniqueConstraints {
		columns, err := r.quotedNames(constraint.Columns)
		if err != nil {
			return err
		}
		definition := "UNIQUE (" + strings.Join(columns, ", ") + ")"
		if constraint.Name != "" {
			name, err := r.quoteIdentifier(constraint.Name)
			if err != nil {
				return err
			}
			definition = "CONSTRAINT " + name + " " + definition
		}
		definitions = append(definitions, definition)
	}
	for _, check := range table.Checks {
		definition := "CHECK (" + check.Expression + ")"
		if check.Name != "" {
			name, err := r.quoteIdentifier(check.Name)
			if err != nil {
				return err
			}
			definition = "CONSTRAINT " + name + " " + definition
		}
		definitions = append(definitions, definition)
	}
	for _, key := range table.ForeignKeys {
		definition, err := r.foreignKeyDefinition(key)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	r.builder.WriteString(strings.Join(definitions, ", "))
	r.builder.WriteByte(')')
	r.builder.WriteString(primaryKeySuffix)
	return nil
}

func (r *renderer) writeCreateIndex(table schema.Table, index schema.Index) error {
	indexName, err := r.quoteIdentifier(index.Name)
	if err != nil {
		return err
	}
	tableName, err := r.quoteIdentifier(table.Name)
	if err != nil {
		return err
	}
	columns, err := r.quotedNames(index.Columns)
	if err != nil {
		return err
	}
	r.builder.WriteString("CREATE ")
	if index.Unique {
		r.builder.WriteString("UNIQUE ")
	}
	r.builder.WriteString("INDEX ")
	r.builder.WriteString(indexName)
	r.builder.WriteString(" ON ")
	r.builder.WriteString(tableName)
	r.builder.WriteString(" (")
	r.builder.WriteString(strings.Join(columns, ", "))
	r.builder.WriteByte(')')
	return nil
}

func (r *renderer) columnDefinition(column schema.Column) (string, error) {
	name, err := r.quoteIdentifier(column.Name)
	if err != nil {
		return "", err
	}
	typeName, err := r.dialect.TypeName(column.Type)
	if err != nil {
		return "", fmt.Errorf("column %q: %w", column.Name, err)
	}
	definition := name + " " + typeName
	if !column.Nullable {
		definition += " NOT NULL"
	}
	if column.Default != "" {
		definition += " DEFAULT " + column.Default
	}
	return definition, nil
}

func (r *renderer) foreignKeyDefinition(key schema.ForeignKey) (string, error) {
	columns, err := r.quotedNames(key.Columns)
	if err != nil {
		return "", err
	}
	referencedTable, err := r.quoteIdentifier(key.ReferencedTable)
	if err != nil {
		return "", err
	}
	referencedColumns, err := r.quotedNames(key.ReferencedColumns)
	if err != nil {
		return "", err
	}
	definition := "FOREIGN KEY (" + strings.Join(columns, ", ") + ") REFERENCES " + referencedTable + " (" + strings.Join(referencedColumns, ", ") + ")"
	if key.Name != "" {
		name, err := r.quoteIdentifier(key.Name)
		if err != nil {
			return "", err
		}
		definition = "CONSTRAINT " + name + " " + definition
	}
	if key.OnDelete != "" {
		definition += " ON DELETE " + string(key.OnDelete)
	}
	if key.OnUpdate != "" {
		definition += " ON UPDATE " + string(key.OnUpdate)
	}
	return definition, nil
}

func (r *renderer) quotedNames(names []string) ([]string, error) {
	quoted := make([]string, len(names))
	for i, name := range names {
		value, err := r.quoteIdentifier(name)
		if err != nil {
			return nil, err
		}
		quoted[i] = value
	}
	return quoted, nil
}
