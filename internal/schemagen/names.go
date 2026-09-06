package schemagen

import (
	"fmt"
	"go/token"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/schema"
)

type NameOverrides struct {
	Objects map[schema.ObjectName]ObjectNameOverrides
}
type ObjectNameOverrides struct {
	Accessor, TableType, RowType, FileBase string
	Columns                                map[string]ColumnNameOverrides
}
type ColumnNameOverrides struct{ Field, Accessor string }

type ResolvedNames struct {
	objects      map[schema.ObjectName]ResolvedObjectNames
	columns      map[schema.ObjectName]map[string]ResolvedColumnNames
	packageNames []string
}
type ResolvedObjectNames struct {
	Identity                                                                                              schema.ObjectName
	Accessor, TableType, RowType, DescriptorVar, DefinitionVar, DefinitionAccessor, TimeScanner, FileBase string
}
type ResolvedColumnNames struct{ Physical, Field, Accessor, ScanIndex string }

func ResolveNames(packageName string, tables []schema.TableDef, overrides NameOverrides) (*ResolvedNames, error) {
	if !isUsablePackageName(packageName) {
		return nil, packageNameError(packageName)
	}
	n := &ResolvedNames{objects: make(map[schema.ObjectName]ResolvedObjectNames), columns: make(map[schema.ObjectName]map[string]ResolvedColumnNames)}
	known := make(map[schema.ObjectName]schema.TableDef, len(tables))
	for _, table := range tables {
		known[table.ObjectName()] = table
	}
	for object, override := range overrides.Objects {
		table, ok := known[object]
		if !ok {
			return nil, fmt.Errorf("generate: names for %s refer to an unknown table", physicalIdentity(object))
		}
		for column := range override.Columns {
			if _, ok := table.Column(column); !ok {
				return nil, fmt.Errorf("generate: names for %s refer to unknown column %q", physicalIdentity(object), column)
			}
		}
	}
	ordered := append([]schema.TableDef(nil), tables...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].Schema != ordered[j].Schema {
			return ordered[i].Schema < ordered[j].Schema
		}
		return ordered[i].Name < ordered[j].Name
	})
	for _, table := range ordered {
		object := table.ObjectName()
		override := overrides.Objects[object]
		accessor := variableName(table.Name)
		if override.Accessor != "" {
			accessor = override.Accessor
		}
		row := rowTypeName(table)
		tableType := tableTypeName(table.Name)
		if override.Accessor != "" {
			row = accessor + "Row"
			tableType = accessor + "Table"
		}
		if override.RowType != "" {
			row = override.RowType
		}
		if table.RowName != "" && override.RowType != "" && table.RowName != override.RowType {
			return nil, fmt.Errorf("generate: names for %s RowType %q conflicts with legacy RowName %q", physicalIdentity(object), override.RowType, table.RowName)
		}
		descriptor := descriptorName(table.Name)
		definition := definitionName(table.Name)
		definitionAccessor := definitionAccessorName(table.Name)
		scanner := timeScannerTypeName(table.Name)
		if override.Accessor != "" {
			descriptor = lowerFirst(accessor) + "Table"
			definition = lowerFirst(accessor) + "Def"
			definitionAccessor = accessor + "Def"
			scanner = lowerFirst(accessor) + "TimeScanner"
		}
		fileBase := strings.ToLower(table.Name)
		if override.FileBase != "" {
			fileBase = override.FileBase
		}
		n.objects[object] = ResolvedObjectNames{object, accessor, tableType, row, descriptor, definition, definitionAccessor, scanner, fileBase}
		if override.TableType != "" {
			value := n.objects[object]
			value.TableType = override.TableType
			n.objects[object] = value
		}
		for label, value := range map[string]string{"Accessor": accessor, "TableType": n.objects[object].TableType, "RowType": row, "FileBase": fileBase} {
			if label == "FileBase" {
				continue
			}
			if !token.IsIdentifier(value) || value == "_" {
				return nil, fmt.Errorf("generate: table %s has invalid generated %s %q", physicalIdentity(object), label, value)
			}
		}
		columns := make(map[string]ResolvedColumnNames, len(table.Columns))
		for _, column := range table.Columns {
			columnOverride := override.Columns[column.Name]
			field := goName(column.Name)
			accessorName := field
			if columnOverride.Field != "" {
				field = columnOverride.Field
			}
			if columnOverride.Accessor != "" {
				accessorName = columnOverride.Accessor
			}
			columns[column.Name] = ResolvedColumnNames{column.Name, field, accessorName, scanIndexName(field)}
		}
		n.columns[object] = columns
	}
	n.packageNames = n.collectPackageNames()
	if err := n.validateCollisions(ordered); err != nil {
		return nil, err
	}
	return n, nil
}

func (n *ResolvedNames) validateCollisions(tables []schema.TableDef) error {
	owners := make(map[string]string)
	claim := func(name, owner string) error {
		if previous, exists := owners[name]; exists && previous != owner {
			return fmt.Errorf("generate: final name %q collides between %s and %s", name, previous, owner)
		}
		owners[name] = owner
		return nil
	}
	for _, fixed := range []string{"Tables", "TestRasqlgenGeneratedDefinitionsAreValid"} {
		owners[fixed] = "fixed generated declaration " + fixed
	}
	for _, table := range tables {
		object, _ := n.Object(table)
		identity := physicalIdentity(object.Identity)
		for _, symbol := range []string{object.Accessor, object.TableType, object.RowType, object.DescriptorVar, object.DefinitionVar, object.DefinitionAccessor} {
			if err := claim(symbol, identity); err != nil {
				return err
			}
		}
		if object.TimeScanner != "" {
			if err := claim(object.TimeScanner, identity); err != nil {
				return err
			}
		}
		methods := map[string]struct{}{"As": {}, "Column": {}, "ColumnValue": {}, "Ref": {}, "ScanDestinations": {}, "ScanRow": {}, "Table": {}, "tableRow": {}}
		fields := make(map[string]string)
		accessors := make(map[string]string)
		for _, column := range table.Columns {
			resolved, _ := n.Column(table, column.Name)
			if _, reserved := methods[resolved.Accessor]; reserved {
				return fmt.Errorf("generate: final column accessor %q for %s is reserved", resolved.Accessor, identity)
			}
			if previous, exists := fields[resolved.Field]; exists {
				return fmt.Errorf("generate: final field %q on %s collides between columns %q and %q", resolved.Field, identity, previous, column.Name)
			}
			fields[resolved.Field] = column.Name
			if previous, exists := accessors[resolved.Accessor]; exists {
				return fmt.Errorf("generate: final accessor %q on %s collides between columns %q and %q", resolved.Accessor, identity, previous, column.Name)
			}
			accessors[resolved.Accessor] = column.Name
		}
		for _, relationship := range relationshipSpecs(table, tables, n) {
			if err := claim(relationship.typeName, identity+" relationship "+relationship.method); err != nil {
				return err
			}
			if previous, exists := accessors[relationship.method]; exists {
				return fmt.Errorf("generate: final relationship method %q on %s collides with column %q", relationship.method, identity, previous)
			}
			accessors[relationship.method] = "relationship " + relationship.method
		}
	}
	files := make(map[string]string)
	for _, table := range tables {
		object, _ := n.Object(table)
		file := strings.ToLower(object.FileBase)
		if previous, exists := files[file]; exists && previous != physicalIdentity(object.Identity) {
			return fmt.Errorf("generate: file base %q collides between %s and %s", object.FileBase, previous, physicalIdentity(object.Identity))
		}
		files[file] = physicalIdentity(object.Identity)
	}
	return nil
}

func physicalIdentity(object schema.ObjectName) string {
	if object.Schema == "" {
		return object.Name
	}
	return object.Schema + "." + object.Name
}
func lowerFirst(value string) string {
	if value == "" {
		return value
	}
	return strings.ToLower(value[:1]) + value[1:]
}
func (n *ResolvedNames) Object(table schema.TableDef) (ResolvedObjectNames, bool) {
	value, ok := n.objects[table.ObjectName()]
	return value, ok
}
func (n *ResolvedNames) Column(table schema.TableDef, physical string) (ResolvedColumnNames, bool) {
	value, ok := n.columns[table.ObjectName()][physical]
	return value, ok
}
func (n *ResolvedNames) Filename(table schema.TableDef) string {
	value, _ := n.Object(table)
	return value.FileBase + "_gen.go"
}

func (n *ResolvedNames) validateCoverage(tables, allTables []schema.TableDef) error {
	known := make(map[schema.ObjectName]schema.TableDef, len(allTables))
	for _, table := range allTables {
		known[table.ObjectName()] = table
	}
	for _, table := range tables {
		if _, ok := n.Object(table); !ok {
			return fmt.Errorf("generate: resolved names missing table %s", physicalIdentity(table.ObjectName()))
		}
		for _, column := range table.Columns {
			if _, ok := n.Column(table, column.Name); !ok {
				return fmt.Errorf("generate: resolved names missing column %s.%q", physicalIdentity(table.ObjectName()), column.Name)
			}
		}
		for _, relationship := range table.Relationships {
			target, ok := known[schema.ObjectName{Schema: relationship.ReferencedSchema, Name: relationship.ReferencedTable}]
			if !ok {
				continue
			}
			if _, ok := n.Object(target); !ok {
				return fmt.Errorf("generate: resolved names missing relationship target %s", physicalIdentity(target.ObjectName()))
			}
			for _, column := range relationship.Columns {
				if _, ok := n.Column(table, column); !ok {
					return fmt.Errorf("generate: resolved names missing relationship column %s.%q", physicalIdentity(table.ObjectName()), column)
				}
			}
			for _, column := range relationship.ReferencedColumns {
				if _, ok := n.Column(target, column); !ok {
					return fmt.Errorf("generate: resolved names missing relationship target column %s.%q", physicalIdentity(target.ObjectName()), column)
				}
			}
		}
	}
	return nil
}
func (n *ResolvedNames) PackageLevelNames() []string { return append([]string(nil), n.packageNames...) }
func (n *ResolvedNames) collectPackageNames() []string {
	set := make(map[string]struct{})
	for name := range reservedPackageNames {
		set[name] = struct{}{}
	}
	for _, object := range n.objects {
		for _, name := range []string{object.Accessor, object.TableType, object.RowType, object.DescriptorVar, object.DefinitionVar, object.DefinitionAccessor} {
			set[name] = struct{}{}
		}
		if object.TimeScanner != "" {
			set[object.TimeScanner] = struct{}{}
		}
	}
	result := make([]string, 0, len(set))
	for name := range set {
		result = append(result, name)
	}
	sort.Strings(result)
	return result
}
