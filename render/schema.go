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
	name, err := r.quoteQualified(table.Schema, table.Name)
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
	if len(table.PrimaryKey) > 0 {
		columns, err := r.quotedNames(table.PrimaryKey)
		if err != nil {
			return err
		}
		definitions = append(definitions, "PRIMARY KEY ("+strings.Join(columns, ", ")+")")
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
		definition, err := r.foreignKeyDefinition(table, key)
		if err != nil {
			return err
		}
		definitions = append(definitions, definition)
	}
	r.builder.WriteString(strings.Join(definitions, ", "))
	r.builder.WriteByte(')')
	return nil
}

func (r *renderer) writeCreateIndex(table schema.Table, index schema.Index) error {
	indexName, tableName, err := r.qualifiedIndexNames(table, index)
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

// qualifiedIndexNames returns the quoted index name and quoted table name for
// a CREATE INDEX statement. An unqualified table.Schema always yields today's
// unqualified pair, with no capability consulted. A qualified table.Schema
// needs one of two capabilities, because which identifier carries the
// qualifier is positional: dialect.CapabilityQualifiedIndexTarget qualifies
// the indexed table and leaves the index name bare, and
// dialect.CapabilityQualifiedIndexName qualifies the index name and leaves
// the indexed table bare, which is SQLite's form, since it cannot qualify the
// table in "ON table" at all. A dialect with neither capability is refused
// rather than silently dropping the qualifier.
func (r *renderer) qualifiedIndexNames(table schema.Table, index schema.Index) (string, string, error) {
	if table.Schema == "" {
		indexName, err := r.quoteIdentifier(index.Name)
		if err != nil {
			return "", "", err
		}
		tableName, err := r.quoteIdentifier(table.Name)
		if err != nil {
			return "", "", err
		}
		return indexName, tableName, nil
	}
	switch {
	case r.dialect.Supports(dialect.CapabilityQualifiedIndexTarget):
		indexName, err := r.quoteIdentifier(index.Name)
		if err != nil {
			return "", "", err
		}
		tableName, err := r.quoteQualified(table.Schema, table.Name)
		if err != nil {
			return "", "", err
		}
		return indexName, tableName, nil
	case r.dialect.Supports(dialect.CapabilityQualifiedIndexName):
		indexName, err := r.quoteQualified(table.Schema, index.Name)
		if err != nil {
			return "", "", err
		}
		tableName, err := r.quoteIdentifier(table.Name)
		if err != nil {
			return "", "", err
		}
		return indexName, tableName, nil
	default:
		return "", "", fmt.Errorf("dialect %s: cannot create an index on table %q in schema %q: this dialect lacks both dialect.CapabilityQualifiedIndexTarget and dialect.CapabilityQualifiedIndexName", r.dialect.Name(), table.Name, table.Schema)
	}
}

func (r *renderer) columnDefinition(column schema.Column) (string, error) {
	name, err := r.quoteIdentifier(column.Name)
	if err != nil {
		return "", err
	}
	typeName, err := r.dialect.TypeName(column)
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

// qualifiedReferencedTable returns the quoted REFERENCES target for key,
// owned by table. An empty key.ReferencedSchema renders exactly what an
// unqualified reference always has, with no capability consulted.
// dialect.CapabilityQualifiedReference renders the reference qualified, on
// any dialect that has it. Without that capability, a same-schema reference
// still renders unqualified, because dropping a same-schema qualifier changes
// nothing about what the reference means; this is also the only form SQLite
// can render, since it rejects a schema-qualified REFERENCES clause outright,
// even for its own schema. A cross-schema reference on a dialect with neither
// path is refused rather than silently rendered as same-schema or dropped.
func (r *renderer) qualifiedReferencedTable(table schema.Table, key schema.ForeignKey) (string, error) {
	if key.ReferencedSchema == "" {
		return r.quoteIdentifier(key.ReferencedTable)
	}
	if r.dialect.Supports(dialect.CapabilityQualifiedReference) {
		return r.quoteQualified(key.ReferencedSchema, key.ReferencedTable)
	}
	if key.ReferencedSchema == table.Schema {
		return r.quoteIdentifier(key.ReferencedTable)
	}
	return "", fmt.Errorf("dialect %s: foreign key on table %q references table %q in schema %q: this dialect lacks dialect.CapabilityQualifiedReference and can only reference table %q's own schema %q", r.dialect.Name(), table.Name, key.ReferencedTable, key.ReferencedSchema, table.Name, table.Schema)
}

func (r *renderer) foreignKeyDefinition(table schema.Table, key schema.ForeignKey) (string, error) {
	columns, err := r.quotedNames(key.Columns)
	if err != nil {
		return "", err
	}
	referencedTable, err := r.qualifiedReferencedTable(table, key)
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
