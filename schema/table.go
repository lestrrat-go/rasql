package schema

import "fmt"

// Column describes a table column.
type Column struct {
	Name     string
	Type     LogicalType
	Nullable bool
	Default  string
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
		if _, exists := columns[column.Name]; exists {
			return validationError(path+".name", "duplicates column %q", column.Name)
		}
		columns[column.Name] = struct{}{}
	}

	if err := validateColumnList("primary_key", t.PrimaryKey, columns, false); err != nil {
		return err
	}
	if err := validateNamedColumnLists("unique_constraints", t.UniqueConstraints, columns); err != nil {
		return err
	}
	if err := validateChecks(t.Checks); err != nil {
		return err
	}
	if err := validateIndexes(t.Indexes, columns); err != nil {
		return err
	}
	return validateForeignKeys(t.ForeignKeys, columns)
}

func validateNamedColumnLists(path string, constraints []UniqueConstraint, columns map[string]struct{}) error {
	names := make(map[string]struct{}, len(constraints))
	for i, constraint := range constraints {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if constraint.Name != "" {
			if err := ValidateIdentifier(constraint.Name); err != nil {
				return validationError(itemPath+".name", "%s", err)
			}
			if _, exists := names[constraint.Name]; exists {
				return validationError(itemPath+".name", "duplicates constraint %q", constraint.Name)
			}
			names[constraint.Name] = struct{}{}
		}
		if err := validateColumnList(itemPath+".columns", constraint.Columns, columns, true); err != nil {
			return err
		}
	}
	return nil
}

func validateChecks(checks []CheckConstraint) error {
	names := make(map[string]struct{}, len(checks))
	for i, check := range checks {
		path := fmt.Sprintf("checks[%d]", i)
		if check.Name != "" {
			if err := ValidateIdentifier(check.Name); err != nil {
				return validationError(path+".name", "%s", err)
			}
			if _, exists := names[check.Name]; exists {
				return validationError(path+".name", "duplicates constraint %q", check.Name)
			}
			names[check.Name] = struct{}{}
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

func validateForeignKeys(keys []ForeignKey, columns map[string]struct{}) error {
	names := make(map[string]struct{}, len(keys))
	for i, key := range keys {
		path := fmt.Sprintf("foreign_keys[%d]", i)
		if key.Name != "" {
			if err := ValidateIdentifier(key.Name); err != nil {
				return validationError(path+".name", "%s", err)
			}
			if _, exists := names[key.Name]; exists {
				return validationError(path+".name", "duplicates foreign key %q", key.Name)
			}
			names[key.Name] = struct{}{}
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
