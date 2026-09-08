package generate

import (
	"fmt"
	"go/token"
	"regexp"
	"unicode"

	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

func toNameOverrides(names map[schema.ObjectName]legacyObjectNames) schemagen.NameOverrides {
	overrides := schemagen.NameOverrides{Objects: make(map[schema.ObjectName]schemagen.ObjectNameOverrides, len(names))}
	for object, configured := range names {
		columns := make(map[string]schemagen.ColumnNameOverrides, len(configured.Columns))
		for column, value := range configured.Columns {
			columns[column] = schemagen.ColumnNameOverrides{Field: value.Field, Accessor: value.Accessor}
		}
		overrides.Objects[object] = schemagen.ObjectNameOverrides{Accessor: configured.Accessor, TableType: configured.TableType, RowType: configured.RowType, FileBase: configured.FileBase, Columns: columns}
	}
	return overrides
}

type legacyObjectNames struct {
	Accessor  string                       `json:"accessor,omitempty"`
	TableType string                       `json:"table_type,omitempty"`
	RowType   string                       `json:"row_type,omitempty"`
	FileBase  string                       `json:"file_base,omitempty"`
	Columns   map[string]legacyColumnNames `json:"columns,omitempty"`
}

type legacyColumnNames struct {
	Field    string `json:"field,omitempty"`
	Accessor string `json:"accessor,omitempty"`
}

var fileBasePattern = regexp.MustCompile(`^[a-z][a-z0-9]*(?:_[a-z0-9]+)*$`)

var reservedGeneratedMethods = map[string]struct{}{
	"As": {}, "Column": {}, "ColumnValue": {}, "Ref": {}, "ScanDestinations": {}, "ScanRow": {}, "Table": {}, "tableRow": {},
}

func validExportedName(name string) bool {
	if !token.IsIdentifier(name) || name == "_" {
		return false
	}
	r := []rune(name)
	return len(r) > 0 && unicode.IsUpper(r[0])
}

func validateObjectNames(tables []schema.TableDef, names map[schema.ObjectName]legacyObjectNames) error {
	known := make(map[schema.ObjectName]schema.TableDef, len(tables))
	for _, table := range tables {
		known[table.ObjectName()] = table
	}
	for object, configured := range names {
		table, ok := known[object]
		if !ok {
			return fmt.Errorf("generate: names for %s.%s refer to an unknown table", object.Schema, object.Name)
		}
		if configured.Accessor != "" && !validExportedName(configured.Accessor) {
			return fmt.Errorf("generate: names for %s.%s have invalid Accessor %q", object.Schema, object.Name, configured.Accessor)
		}
		if configured.RowType != "" && table.RowName != "" && configured.RowType != table.RowName {
			return fmt.Errorf("generate: names for %s.%s RowType %q conflicts with legacy RowName %q", object.Schema, object.Name, configured.RowType, table.RowName)
		}
		for label, value := range map[string]string{"TableType": configured.TableType, "RowType": configured.RowType} {
			if value != "" && !validExportedName(value) {
				return fmt.Errorf("generate: names for %s.%s have invalid %s %q", object.Schema, object.Name, label, value)
			}
		}
		if configured.FileBase != "" && !fileBasePattern.MatchString(configured.FileBase) {
			return fmt.Errorf("generate: names for %s.%s have invalid FileBase %q", object.Schema, object.Name, configured.FileBase)
		}
		for column, columnNames := range configured.Columns {
			if _, ok := table.Column(column); !ok {
				return fmt.Errorf("generate: names for %s.%s refer to unknown column %q", object.Schema, object.Name, column)
			}
			if columnNames.Field != "" && !validExportedName(columnNames.Field) {
				return fmt.Errorf("generate: names for %s.%s column %q has invalid Field %q", object.Schema, object.Name, column, columnNames.Field)
			}
			if _, reserved := reservedGeneratedMethods[columnNames.Field]; reserved {
				return fmt.Errorf("generate: names for %s.%s column %q use reserved generated field %q", object.Schema, object.Name, column, columnNames.Field)
			}
			if columnNames.Accessor != "" && !validExportedName(columnNames.Accessor) {
				return fmt.Errorf("generate: names for %s.%s column %q has invalid Accessor %q", object.Schema, object.Name, column, columnNames.Accessor)
			}
			if _, reserved := reservedGeneratedMethods[columnNames.Accessor]; reserved {
				return fmt.Errorf("generate: names for %s.%s column %q use reserved generated accessor %q", object.Schema, object.Name, column, columnNames.Accessor)
			}
		}
		fields := make(map[string]string)
		accessors := make(map[string]string)
		for column, columnNames := range configured.Columns {
			if columnNames.Field != "" {
				if previous, exists := fields[columnNames.Field]; exists {
					return fmt.Errorf("generate: names for %s.%s columns %q and %q have duplicate final field %q", object.Schema, object.Name, previous, column, columnNames.Field)
				}
				fields[columnNames.Field] = column
			}
			if columnNames.Accessor != "" {
				if previous, exists := accessors[columnNames.Accessor]; exists {
					return fmt.Errorf("generate: names for %s.%s columns %q and %q have duplicate final accessor %q", object.Schema, object.Name, previous, column, columnNames.Accessor)
				}
				accessors[columnNames.Accessor] = column
			}
		}
	}
	return nil
}

func cloneObjectNames(names map[schema.ObjectName]legacyObjectNames) map[schema.ObjectName]legacyObjectNames {
	clone := make(map[schema.ObjectName]legacyObjectNames, len(names))
	for object, configured := range names {
		columns := make(map[string]legacyColumnNames, len(configured.Columns))
		for column, columnNames := range configured.Columns {
			columns[column] = columnNames
		}
		configured.Columns = columns
		clone[object] = configured
	}
	return clone
}
