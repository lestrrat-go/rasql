package schema

import (
	"encoding/json"
	"fmt"
	"slices"
)

// DecimalScale is the number of digits a DecimalType column keeps to the right
// of the decimal point. Its zero value states no scale at all, which is a
// different thing from a stated scale of zero: DECIMAL(19,0) is a legitimate
// column and a descriptor that simply forgot to say so is not. Use
// NewDecimalScale to state one, including a scale of zero.
type DecimalScale struct {
	value int
	set   bool
}

// NewDecimalScale returns a DecimalScale that states value.
func NewDecimalScale(value int) DecimalScale {
	return DecimalScale{value: value, set: true}
}

// Value returns the stated scale and reports whether a scale was stated at all.
// The returned scale is meaningless when the second result is false.
func (s DecimalScale) Value() (int, bool) {
	return s.value, s.set
}

// MarshalJSON encodes a stated scale as a JSON number and an unstated one as
// null, so that a snapshot of a schema.Table keeps the plain-number form a
// tool such as rasqlgen reads back.
func (s DecimalScale) MarshalJSON() ([]byte, error) {
	if !s.set {
		return []byte("null"), nil
	}
	return json.Marshal(s.value)
}

// UnmarshalJSON decodes a JSON number as a stated scale and null as an
// unstated one. A snapshot written before a column had a scale therefore
// decodes as unstated and is refused by Table.Validate rather than read as 0.
func (s *DecimalScale) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = DecimalScale{}
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("schema: decode decimal scale: %w", err)
	}
	*s = NewDecimalScale(value)
	return nil
}

// Column describes a table column.
type Column struct {
	Name     string
	Type     ColumnType
	Nullable bool
	Default  string
}

// MarshalJSON encodes a column type as a tagged object so type-specific
// options cannot appear as fields on unrelated column types.
func (c Column) MarshalJSON() ([]byte, error) {
	type wireColumn struct {
		Name     string          `json:"Name"`
		Type     json.RawMessage `json:"Type"`
		Nullable bool            `json:"Nullable"`
		Default  string          `json:"Default"`
	}
	typeData, err := marshalColumnType(c.Type)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wireColumn{Name: c.Name, Type: typeData, Nullable: c.Nullable, Default: c.Default})
}

// UnmarshalJSON decodes the tagged column type representation.
func (c *Column) UnmarshalJSON(data []byte) error {
	type wireColumn struct {
		Name     string          `json:"Name"`
		Type     json.RawMessage `json:"Type"`
		Nullable bool            `json:"Nullable"`
		Default  string          `json:"Default"`
	}
	var wire wireColumn
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	columnType, err := unmarshalColumnType(wire.Type)
	if err != nil {
		return err
	}
	*c = Column{Name: wire.Name, Type: columnType, Nullable: wire.Nullable, Default: wire.Default}
	return nil
}

// UniqueConstraint requires the listed columns to be unique together.
type UniqueConstraint struct {
	Name    string
	Columns []string
}

// CheckConstraint requires Expression to evaluate to true for each row.
// Expression is a schema-level SQL expression and is rendered only by a DDL-capable dialect.
type CheckConstraint struct {
	Name       string
	Expression string
}

// IndexDef describes an index owned by a table.
type IndexDef struct {
	Name    string
	Columns []string
	Unique  bool
}

// ForeignKeyDef describes a foreign-key constraint.
type ForeignKeyDef struct {
	Name    string
	Columns []string

	// ReferencedSchema names the schema holding ReferencedTable. An empty
	// ReferencedSchema leaves the reference unqualified, which each server
	// then resolves by its own rule: SQLite in the referencing table's own
	// database, PostgreSQL through search_path. A dialect without
	// dialect.CapabilityQualifiedReference rejects a ReferencedSchema that
	// names any schema other than the referencing table's own.
	ReferencedSchema  string
	ReferencedTable   string
	ReferencedColumns []string
	OnDelete          ReferenceAction
	OnUpdate          ReferenceAction
}

// Table describes a database table and its constraints.
type Table struct {
	// Schema names the namespace holding the table: a PostgreSQL schema, a
	// MySQL database, or a SQLite attached-database name. A renderer quotes it
	// as an identifier separate from Name and never interprets it further, so
	// rasql takes no position on what a namespace means to a server and never
	// creates one. An empty Schema leaves the table unqualified, which
	// resolves through the connection's own default and is what every
	// descriptor written before this field existed does.
	//
	// Qualification reaches DML, column references and DDL. A SELECT,
	// INSERT, UPDATE or DELETE built from this descriptor renders
	// "audit"."events" as its target, a column reached through the unaliased
	// table renders "audit"."events"."id", and render.CreateTable,
	// render.CreateIndexes and rasql.Create render the table and its indexes
	// into the named namespace on every dialect that can express it. rasql
	// never creates, drops or connects to the namespace itself: an
	// application that needs "audit" to exist creates it with a reviewed
	// native migration, the same way every other piece of DDL this library
	// does not synthesize gets created. inspect never reports a Schema, so a
	// qualified table returned by inspection is re-read through a hand-written
	// descriptor.
	Schema            string
	Name              string
	Columns           []Column
	PrimaryKey        []string
	UniqueConstraints []UniqueConstraint
	Checks            []CheckConstraint
	Indexes           []IndexDef
	ForeignKeys       []ForeignKeyDef
	Relationships     []Relationship
}

// Qualified reports whether t names a schema.
func (t Table) Qualified() bool {
	return t.Schema != ""
}

// QualifiedName returns the table's name for display: "schema.name" when t
// names a schema and "name" otherwise. It is for error messages, log output
// and map keys only. It is never a SQL identifier: a renderer quotes Schema
// and Name as two identifiers, and dialect.QuoteIdentifier rejects the
// dotted string this returns.
func (t Table) QualifiedName() string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// Clone returns a copy of t that does not share slices with t.
func (t Table) Clone() Table {
	clone := t
	clone.Columns = append([]Column(nil), t.Columns...)
	clone.PrimaryKey = append([]string(nil), t.PrimaryKey...)
	clone.UniqueConstraints = make([]UniqueConstraint, len(t.UniqueConstraints))
	for i, constraint := range t.UniqueConstraints {
		clone.UniqueConstraints[i] = constraint
		clone.UniqueConstraints[i].Columns = append([]string(nil), constraint.Columns...)
	}
	clone.Checks = append([]CheckConstraint(nil), t.Checks...)
	clone.Indexes = make([]IndexDef, len(t.Indexes))
	for i, index := range t.Indexes {
		clone.Indexes[i] = index
		clone.Indexes[i].Columns = append([]string(nil), index.Columns...)
	}
	clone.ForeignKeys = make([]ForeignKeyDef, len(t.ForeignKeys))
	for i, key := range t.ForeignKeys {
		clone.ForeignKeys[i] = key
		clone.ForeignKeys[i].Columns = append([]string(nil), key.Columns...)
		clone.ForeignKeys[i].ReferencedColumns = append([]string(nil), key.ReferencedColumns...)
	}
	clone.Relationships = make([]Relationship, len(t.Relationships))
	for i, relationship := range t.Relationships {
		clone.Relationships[i] = relationship.Clone()
	}
	return clone
}

// Column returns the column named name.
func (t Table) Column(name string) (Column, bool) {
	for _, column := range t.Columns {
		if column.Name == name {
			return column, true
		}
	}
	return Column{}, false
}

// Validate reports whether t has a valid, internally consistent descriptor.
func (t Table) Validate() error {
	if t.Schema != "" {
		if err := ValidateIdentifier(t.Schema); err != nil {
			return validationError("table.schema", "%s", err)
		}
	}
	if err := ValidateIdentifier(t.Name); err != nil {
		return validationError("table", "%s", err)
	}
	if len(t.Columns) == 0 {
		return validationError("table", "must have at least one column")
	}

	columns := make(map[string]struct{}, len(t.Columns))
	for i, column := range t.Columns {
		path := fmt.Sprintf("columns[%d]", i)
		if err := ValidateIdentifier(column.Name); err != nil {
			return validationError(path+".name", "%s", err)
		}
		if !validColumnType(column.Type) {
			return validationError(path+".type", "unsupported column type %T", column.Type)
		}
		switch typed := column.Type.(type) {
		case DecimalType:
			if typed.Precision < 1 {
				return validationError(path+".type.precision", "decimal column must state a precision of at least 1")
			}
			scale, stated := typed.Scale.Value()
			if !stated {
				return validationError(path+".type.scale", "decimal column must state a scale: use schema.NewDecimalScale, which can state a scale of 0")
			}
			if scale < 0 {
				return validationError(path+".type.scale", "decimal scale must not be negative")
			}
			if scale > typed.Precision {
				return validationError(path+".type.scale", "decimal scale %d exceeds precision %d", scale, typed.Precision)
			}
		}
		if _, exists := columns[column.Name]; exists {
			return validationError(path+".name", "duplicates column %q", column.Name)
		}
		columns[column.Name] = struct{}{}
	}

	if err := validateColumnList("primary_key", t.PrimaryKey, columns, false); err != nil {
		return err
	}
	constraintNames := make(map[string]string)
	if err := validateNamedColumnLists("unique_constraints", t.UniqueConstraints, columns, constraintNames); err != nil {
		return err
	}
	if err := validateChecks(t.Checks, constraintNames); err != nil {
		return err
	}
	if err := validateIndexes(t.Indexes, columns); err != nil {
		return err
	}
	if err := validateForeignKeys(t.ForeignKeys, columns, constraintNames); err != nil {
		return err
	}
	return validateRelationships(t.Relationships, t.ForeignKeys, columns)
}

func validateRelationships(relationships []Relationship, foreignKeys []ForeignKeyDef, columns map[string]struct{}) error {
	for i, relationship := range relationships {
		path := fmt.Sprintf("relationships[%d]", i)
		if relationship.Name == "" {
			return validationError(path+".name", "must not be empty")
		}
		if err := ValidateIdentifier(relationship.Name); err != nil {
			return validationError(path+".name", "%s", err)
		}
		if relationship.Kind != RelationshipBelongsTo {
			return validationError(path+".kind", "unsupported relationship kind %q", relationship.Kind)
		}
		if err := validateColumnList(path+".columns", relationship.Columns, columns, true); err != nil {
			return err
		}
		if relationship.ReferencedSchema != "" {
			if err := ValidateIdentifier(relationship.ReferencedSchema); err != nil {
				return validationError(path+".referenced_schema", "%s", err)
			}
		}
		if err := ValidateIdentifier(relationship.ReferencedTable); err != nil {
			return validationError(path+".referenced_table", "%s", err)
		}
		if err := validateIdentifierList(path+".referenced_columns", relationship.ReferencedColumns, true); err != nil {
			return err
		}
		if len(relationship.Columns) != len(relationship.ReferencedColumns) {
			return validationError(path, "has %d local columns and %d referenced columns", len(relationship.Columns), len(relationship.ReferencedColumns))
		}
		matched := false
		for _, foreignKey := range foreignKeys {
			if foreignKey.ReferencedSchema == relationship.ReferencedSchema &&
				foreignKey.ReferencedTable == relationship.ReferencedTable &&
				slices.Equal(foreignKey.Columns, relationship.Columns) &&
				slices.Equal(foreignKey.ReferencedColumns, relationship.ReferencedColumns) {
				matched = true
				break
			}
		}
		if !matched {
			return validationError(path, "does not match a declared foreign key")
		}
	}
	return nil
}

// validateNamedColumnLists validates constraints and records each non-empty
// name in constraintNames, keyed by the name and mapping to the descriptor
// path that first used it. This lets callers detect a name reused across
// different kinds of constraints (unique, check, foreign key), since the
// renderer emits all of them into a single CREATE TABLE constraint namespace.
func validateNamedColumnLists(path string, constraints []UniqueConstraint, columns map[string]struct{}, constraintNames map[string]string) error {
	for i, constraint := range constraints {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if constraint.Name != "" {
			if err := ValidateIdentifier(constraint.Name); err != nil {
				return validationError(itemPath+".name", "%s", err)
			}
			if owner, exists := constraintNames[constraint.Name]; exists {
				return validationError(itemPath+".name", "duplicates constraint %q declared at %s", constraint.Name, owner)
			}
			constraintNames[constraint.Name] = itemPath
		}
		if err := validateColumnList(itemPath+".columns", constraint.Columns, columns, true); err != nil {
			return err
		}
	}
	return nil
}

func validateChecks(checks []CheckConstraint, constraintNames map[string]string) error {
	for i, check := range checks {
		path := fmt.Sprintf("checks[%d]", i)
		if check.Name != "" {
			if err := ValidateIdentifier(check.Name); err != nil {
				return validationError(path+".name", "%s", err)
			}
			if owner, exists := constraintNames[check.Name]; exists {
				return validationError(path+".name", "duplicates constraint %q declared at %s", check.Name, owner)
			}
			constraintNames[check.Name] = path
		}
		if check.Expression == "" {
			return validationError(path+".expression", "must not be empty")
		}
	}
	return nil
}

func validateIndexes(indexes []IndexDef, columns map[string]struct{}) error {
	names := make(map[string]struct{}, len(indexes))
	for i, index := range indexes {
		path := fmt.Sprintf("indexes[%d]", i)
		if err := ValidateIdentifier(index.Name); err != nil {
			return validationError(path+".name", "%s", err)
		}
		if _, exists := names[index.Name]; exists {
			return validationError(path+".name", "duplicates index %q", index.Name)
		}
		names[index.Name] = struct{}{}
		if err := validateColumnList(path+".columns", index.Columns, columns, true); err != nil {
			return err
		}
	}
	return nil
}

func validateForeignKeys(keys []ForeignKeyDef, columns map[string]struct{}, constraintNames map[string]string) error {
	for i, key := range keys {
		path := fmt.Sprintf("foreign_keys[%d]", i)
		if key.Name != "" {
			if err := ValidateIdentifier(key.Name); err != nil {
				return validationError(path+".name", "%s", err)
			}
			if owner, exists := constraintNames[key.Name]; exists {
				return validationError(path+".name", "duplicates constraint %q declared at %s", key.Name, owner)
			}
			constraintNames[key.Name] = path
		}
		if err := validateColumnList(path+".columns", key.Columns, columns, true); err != nil {
			return err
		}
		if key.ReferencedSchema != "" {
			if err := ValidateIdentifier(key.ReferencedSchema); err != nil {
				return validationError(path+".referenced_schema", "%s", err)
			}
		}
		if err := ValidateIdentifier(key.ReferencedTable); err != nil {
			return validationError(path+".referenced_table", "%s", err)
		}
		if err := validateIdentifierList(path+".referenced_columns", key.ReferencedColumns, true); err != nil {
			return err
		}
		if len(key.Columns) != len(key.ReferencedColumns) {
			return validationError(path, "has %d local columns and %d referenced columns", len(key.Columns), len(key.ReferencedColumns))
		}
		if !key.OnDelete.valid() {
			return validationError(path+".on_delete", "unsupported reference action %q", key.OnDelete)
		}
		if !key.OnUpdate.valid() {
			return validationError(path+".on_update", "unsupported reference action %q", key.OnUpdate)
		}
	}
	return nil
}

func validateColumnList(path string, names []string, columns map[string]struct{}, required bool) error {
	if required && len(names) == 0 {
		return validationError(path, "must not be empty")
	}
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if _, exists := columns[name]; !exists {
			return validationError(itemPath, "references unknown column %q", name)
		}
		if _, exists := seen[name]; exists {
			return validationError(itemPath, "duplicates column %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateIdentifierList(path string, names []string, required bool) error {
	if required && len(names) == 0 {
		return validationError(path, "must not be empty")
	}
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if err := ValidateIdentifier(name); err != nil {
			return validationError(itemPath, "%s", err)
		}
		if _, exists := seen[name]; exists {
			return validationError(itemPath, "duplicates column %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}
