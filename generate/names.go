package generate

import (
	"fmt"
	"go/token"
	"regexp"
	"strings"
	"unicode"

	"github.com/lestrrat-go/rasql/schema"
)

// ObjectNames assigns generated Go names to one physical table.
type ObjectNames struct {
	Accessor  string
	TableType string
	RowType   string
	FileBase  string
	Columns   map[string]ColumnNames
}

// ColumnNames assigns generated Go names to one physical column.
type ColumnNames struct {
	Field    string
	Accessor string
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

func validateObjectNames(tables []schema.TableDef, names map[schema.ObjectName]ObjectNames) error {
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

func cloneObjectNames(names map[schema.ObjectName]ObjectNames) map[schema.ObjectName]ObjectNames {
	clone := make(map[schema.ObjectName]ObjectNames, len(names))
	for object, configured := range names {
		columns := make(map[string]ColumnNames, len(configured.Columns))
		for column, columnNames := range configured.Columns {
			columns[column] = columnNames
		}
		configured.Columns = columns
		clone[object] = configured
	}
	return clone
}

func configuredNames(table schema.TableDef, names map[schema.ObjectName]ObjectNames) ObjectNames {
	if configured, ok := names[table.ObjectName()]; ok {
		return configured
	}
	return ObjectNames{}
}

func findTableByName(tables []schema.TableDef, generatedName string, names map[schema.ObjectName]ObjectNames) schema.TableDef {
	for _, table := range tables {
		configured := configuredNames(table, names)
		if configured.Accessor != "" && configured.Accessor == generatedName {
			return table
		}
		if configured.Accessor == "" && table.Name == generatedName {
			return table
		}
	}
	return schema.TableDef{Name: generatedName}
}

func schemaOutputFilenameNamed(table schema.TableDef, names map[schema.ObjectName]ObjectNames) string {
	configured := configuredNames(table, names)
	if configured.FileBase != "" {
		return configured.FileBase + "_gen.go"
	}
	return schemaOutputFilename(table.Name)
}

func physicalClone(table schema.TableDef, configured ObjectNames) schema.TableDef {
	clone := table.Clone()
	if configured.RowType != "" {
		clone.RowName = ""
	}
	if configured.Accessor != "" {
		clone.Name = configured.Accessor
	}
	for index, column := range clone.Columns {
		if names, ok := configured.Columns[column.Name]; ok && names.Accessor != "" {
			clone.Columns[index].Name = names.Accessor
		}
	}
	for index := range clone.PrimaryKey {
		if names, ok := configured.Columns[clone.PrimaryKey[index]]; ok && names.Accessor != "" {
			clone.PrimaryKey[index] = names.Accessor
		}
	}
	return clone
}

func rewriteReferences(tables []schema.TableDef, names map[schema.ObjectName]ObjectNames) {
	for index := range tables {
		for foreignKey := range tables[index].ForeignKeys {
			object := schema.ObjectName{Schema: tables[index].ForeignKeys[foreignKey].ReferencedSchema, Name: tables[index].ForeignKeys[foreignKey].ReferencedTable}
			if configured, ok := names[object]; ok && configured.Accessor != "" {
				tables[index].ForeignKeys[foreignKey].ReferencedTable = configured.Accessor
			}
		}
		for relationship := range tables[index].Relationships {
			object := schema.ObjectName{Schema: tables[index].Relationships[relationship].ReferencedSchema, Name: tables[index].Relationships[relationship].ReferencedTable}
			if configured, ok := names[object]; ok && configured.Accessor != "" {
				tables[index].Relationships[relationship].ReferencedTable = configured.Accessor
			}
		}
	}
}

func nameRewrite(source []byte, original, generated schema.TableDef, configured ObjectNames) []byte {
	text := string(source)
	// Generated descriptors keep physical names as quoted literals. Restore
	// those literals after generation from the safe synthetic descriptors.
	text = strings.ReplaceAll(text, fmt.Sprintf("%q", generated.Name), fmt.Sprintf("%q", original.Name))
	for index, column := range generated.Columns {
		text = strings.ReplaceAll(text, fmt.Sprintf("%q", column.Name), fmt.Sprintf("%q", original.Columns[index].Name))
	}
	accessor := generatedAccessor(generated)
	if configured.TableType != "" {
		text = strings.ReplaceAll(text, accessor+"Table", configured.TableType)
	}
	if configured.RowType != "" {
		text = strings.ReplaceAll(text, accessor+"Row", configured.RowType)
	}
	for index, generatedColumn := range generated.Columns {
		columnNames := configured.Columns[original.Columns[index].Name]
		generatedName := generatedAccessor(schema.TableDef{Name: generatedColumn.Name})
		desiredAccessor := generatedAccessor(schema.TableDef{Name: original.Columns[index].Name})
		if columnNames.Accessor != "" {
			desiredAccessor = columnNames.Accessor
		}
		desiredField := generatedAccessor(schema.TableDef{Name: original.Columns[index].Name})
		if columnNames.Field != "" {
			desiredField = columnNames.Field
		}
		if generatedName != desiredAccessor {
			text = strings.ReplaceAll(text, ") "+generatedName+"() rasql.ColumnRef", ") "+desiredAccessor+"() rasql.ColumnRef")
			text = strings.ReplaceAll(text, "."+generatedName+"()", "."+desiredAccessor+"()")
		}
		if generatedName != desiredField {
			text = strings.ReplaceAll(text, "\t"+generatedName+" ", "\t"+desiredField+" ")
			text = strings.ReplaceAll(text, "r."+generatedName, "r."+desiredField)
			text = strings.ReplaceAll(text, "row."+generatedName, "row."+desiredField)
			text = strings.ReplaceAll(text, "&r."+generatedName, "&r."+desiredField)
		}
	}
	return []byte(text)
}

func generatedAccessor(table schema.TableDef) string {
	parts := strings.FieldsFunc(table.Name, func(r rune) bool { return r == '_' })
	var result strings.Builder
	for _, part := range parts {
		switch strings.ToLower(part) {
		case "api":
			result.WriteString("API")
		case "id":
			result.WriteString("ID")
		case "json":
			result.WriteString("JSON")
		case "url":
			result.WriteString("URL")
		case "uuid":
			result.WriteString("UUID")
		default:
			for index, r := range part {
				if index == 0 {
					result.WriteRune(unicode.ToUpper(r))
				} else {
					result.WriteRune(r)
				}
			}
		}
	}
	return result.String()
}
