package schema

import (
	"encoding/json"
	"fmt"
)

// DecimalScale is the number of digits a TypeDecimal column keeps to the right
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
	Type     LogicalType
	Nullable bool
	Default  string

	// Precision is the total number of significant digits a TypeDecimal column
	// stores, counting those on both sides of the decimal point. It must be at
	// least 1, and must be zero for every other logical type. Each dialect
	// enforces its own upper bound when it renders DDL.
	Precision int

	// Scale is the number of those digits that fall to the right of the
	// decimal point. A TypeDecimal column must state one, and the stated scale
	// must not be negative and must not exceed Precision. Every other logical
	// type must leave it unstated.
	Scale DecimalScale
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

// Index describes an index owned by a table.
type Index struct {
	Name    string
	Columns []string
	Unique  bool
}

// ForeignKey describes a foreign-key constraint.
type ForeignKey struct {
	Name              string
	Columns           []string
	ReferencedTable   string
	ReferencedColumns []string
	OnDelete          ReferenceAction
	OnUpdate          ReferenceAction
}

// Table describes a database table and its constraints.
type Table struct {
	Name              string
	Columns           []Column
	PrimaryKey        []string
	UniqueConstraints []UniqueConstraint
	Checks            []CheckConstraint
	Indexes           []Index
	ForeignKeys       []ForeignKey
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
	clone.Indexes = make([]Index, len(t.Indexes))
	for i, index := range t.Indexes {
		clone.Indexes[i] = index
		clone.Indexes[i].Columns = append([]string(nil), index.Columns...)
	}
	clone.ForeignKeys = make([]ForeignKey, len(t.ForeignKeys))
	for i, key := range t.ForeignKeys {
		clone.ForeignKeys[i] = key
		clone.ForeignKeys[i].Columns = append([]string(nil), key.Columns...)
		clone.ForeignKeys[i].ReferencedColumns = append([]string(nil), key.ReferencedColumns...)
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
		if !column.Type.Valid() {
			return validationError(path+".type", "unsupported logical type %q", column.Type)
		}
		if column.Type == TypeDecimal {
			if column.Precision < 1 {
				return validationError(path+".precision", "decimal column must state a precision of at least 1")
			}
			scale, stated := column.Scale.Value()
			if !stated {
				return validationError(path+".scale", "decimal column must state a scale: use schema.NewDecimalScale, which can state a scale of 0")
			}
			if scale < 0 {
				return validationError(path+".scale", "decimal scale must not be negative")
			}
			if scale > column.Precision {
				return validationError(path+".scale", "decimal scale %d exceeds precision %d", scale, column.Precision)
			}
		} else if _, stated := column.Scale.Value(); column.Precision != 0 || stated {
			return validationError(path+".precision", "precision and scale apply only to a decimal column, not %q", column.Type)
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
	return validateForeignKeys(t.ForeignKeys, columns, constraintNames)
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

func validateIndexes(indexes []Index, columns map[string]struct{}) error {
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

func validateForeignKeys(keys []ForeignKey, columns map[string]struct{}, constraintNames map[string]string) error {
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
