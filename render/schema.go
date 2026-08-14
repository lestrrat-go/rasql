package render

import (
	"errors"
	"fmt"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
)

// ErrUnsupportedIndexMethod is the sentinel wrapped by every
// [UnsupportedIndexMethodError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedIndexMethod = errors.New("render: unsupported index method")

// UnsupportedIndexMethodError reports that an IndexDef names a non-default
// [schema.IndexMethod]. inspect can describe such an index, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for anything other than a plain default index.
type UnsupportedIndexMethodError struct {
	// Index is the name of the index that named a non-default method.
	Index string
	// Method is the non-default method the index named.
	Method schema.IndexMethod
}

func (e *UnsupportedIndexMethodError) Error() string {
	return fmt.Sprintf("index %q uses method %q, which rasql can describe but not yet render", e.Index, e.Method)
}

// Unwrap exposes ErrUnsupportedIndexMethod so
// errors.Is(err, ErrUnsupportedIndexMethod) works alongside errors.As
// against *UnsupportedIndexMethodError.
func (e *UnsupportedIndexMethodError) Unwrap() error {
	return ErrUnsupportedIndexMethod
}

// ErrUnsupportedPartialIndex is the sentinel wrapped by every
// [UnsupportedPartialIndexError], so a caller that only needs a presence
// check can use errors.Is instead of errors.As.
var ErrUnsupportedPartialIndex = errors.New("render: unsupported partial index")

// UnsupportedPartialIndexError reports that an IndexDef names a
// [schema.IndexDef.Predicate], making it a partial index. inspect can
// describe such an index, and TableDef.Validate accepts it, but this
// package does not yet know how to build DDL for a WHERE clause on an
// index.
type UnsupportedPartialIndexError struct {
	// Index is the name of the index that named a predicate.
	Index string
	// Predicate is the WHERE-clause expression text the index named.
	Predicate string
}

func (e *UnsupportedPartialIndexError) Error() string {
	return fmt.Sprintf("index %q has predicate %q, which rasql can describe but not yet render", e.Index, e.Predicate)
}

// Unwrap exposes ErrUnsupportedPartialIndex so
// errors.Is(err, ErrUnsupportedPartialIndex) works alongside errors.As
// against *UnsupportedPartialIndexError.
func (e *UnsupportedPartialIndexError) Unwrap() error {
	return ErrUnsupportedPartialIndex
}

// ErrUnsupportedExpressionIndex is the sentinel wrapped by every
// [UnsupportedExpressionIndexError], so a caller that only needs a
// presence check can use errors.Is instead of errors.As.
var ErrUnsupportedExpressionIndex = errors.New("render: unsupported expression index")

// UnsupportedExpressionIndexError reports that an IndexDef names
// [schema.IndexDef.Expressions], meaning at least one of its keys is not a
// plain column reference. inspect can describe such an index, and
// TableDef.Validate accepts it, but this package does not yet know how to
// build DDL for an expression key.
type UnsupportedExpressionIndexError struct {
	// Index is the name of the index that named expression keys.
	Index string
	// Expressions is the ordered list of key expressions the index named.
	Expressions []string
}

func (e *UnsupportedExpressionIndexError) Error() string {
	return fmt.Sprintf("index %q has expression keys %q, which rasql can describe but not yet render", e.Index, e.Expressions)
}

// Unwrap exposes ErrUnsupportedExpressionIndex so
// errors.Is(err, ErrUnsupportedExpressionIndex) works alongside errors.As
// against *UnsupportedExpressionIndexError.
func (e *UnsupportedExpressionIndexError) Unwrap() error {
	return ErrUnsupportedExpressionIndex
}

// CreateTable renders a CREATE TABLE statement for table.
func CreateTable(d dialect.Dialect, table schema.TableDef) (Statement, error) {
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
func CreateIndexes(d dialect.Dialect, table schema.TableDef) ([]Statement, error) {
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

func (r *renderer) writeCreateTable(table schema.TableDef) error {
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
		if err := r.rejectUnboundedMySQLText(table, table.PrimaryKey, "a primary key"); err != nil {
			return err
		}
		columns, err := r.quotedNames(table.PrimaryKey)
		if err != nil {
			return err
		}
		definitions = append(definitions, "PRIMARY KEY ("+strings.Join(columns, ", ")+")")
	}
	for _, constraint := range table.UniqueConstraints {
		if err := r.rejectUnboundedMySQLText(table, constraint.Columns, "a unique constraint"); err != nil {
			return err
		}
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

func (r *renderer) writeCreateIndex(table schema.TableDef, index schema.IndexDef) error {
	if index.Method != "" {
		return &UnsupportedIndexMethodError{Index: index.Name, Method: index.Method}
	}
	if index.Predicate != "" {
		return &UnsupportedPartialIndexError{Index: index.Name, Predicate: index.Predicate}
	}
	if len(index.Expressions) > 0 {
		return &UnsupportedExpressionIndexError{Index: index.Name, Expressions: index.Expressions}
	}
	if err := r.rejectUnboundedMySQLText(table, index.Columns, "an index"); err != nil {
		return err
	}
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
func (r *renderer) qualifiedIndexNames(table schema.TableDef, index schema.IndexDef) (string, string, error) {
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

// rejectUnboundedMySQLText returns an error if names includes an unbounded
// schema.TextType column and the renderer's dialect is MySQL. MySQL raises
// error 1170 ("BLOB/TEXT column used in key specification without a key
// length") when it is asked to build a key over a TEXT column with no
// stated key length, and rasql has no way to state one on a PRIMARY KEY or
// UNIQUE clause the way MySQL's own DDL can with col(255): schema.IndexDef,
// schema.TableDef.PrimaryKey and schema.UniqueDef all name columns, not
// key-length-qualified expressions. schema.TextType.Width closes the gap
// instead, by letting the column itself state a bound that MySQL's key
// length requirement is then already satisfied by; a caller who hits this
// error states one with schema.Width and the column renders VARCHAR(width)
// rather than TEXT. PostgreSQL and SQLite index, and build a primary key or
// unique constraint over, an unbounded text column natively, so this check
// runs on MySQL only.
func (r *renderer) rejectUnboundedMySQLText(table schema.TableDef, names []string, context string) error {
	if r.dialect.Name() != "mysql" {
		return nil
	}
	for _, name := range names {
		column, ok := table.Column(name)
		if !ok {
			continue
		}
		text, ok := column.Type.(schema.TextType)
		if !ok {
			continue
		}
		if _, stated := text.Width.Value(); stated {
			continue
		}
		return fmt.Errorf("dialect %s: column %q has no stated width: state one with schema.Width to use it in %s, since MySQL cannot build a key over an unbounded text column", r.dialect.Name(), name, context)
	}
	return nil
}

func (r *renderer) columnDefinition(column schema.ColumnDef) (string, error) {
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
func (r *renderer) qualifiedReferencedTable(table schema.TableDef, key schema.ForeignKeyDef) (string, error) {
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

func (r *renderer) foreignKeyDefinition(table schema.TableDef, key schema.ForeignKeyDef) (string, error) {
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
	if key.OnDelete != "" && (key.OnDelete != schema.NoAction || r.dialect.Name() != "sqlite") {
		definition += " ON DELETE " + string(key.OnDelete)
	}
	if key.OnUpdate != "" && (key.OnUpdate != schema.NoAction || r.dialect.Name() != "sqlite") {
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
