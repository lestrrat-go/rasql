// Package schemagen creates deterministic Go source from schema descriptors.
//
// It is the implementation behind the exported generate package. The two
// differ in one thing only, and it is the reason this package exists: a
// caller of generate gets a whole compilation unit back from one call, while
// rasqlgen writes a package as several files and needs the typed surface
// without the descriptors that its own schema_gen.go declares. That second
// form is TableSurfaceSource, which stays here because it is only safe to
// use alongside the descriptor file rasqlgen writes beside it.
package schemagen

import (
	"bytes"
	"fmt"
	"go/format"
	"go/token"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/lestrrat-go/rasql/schema"
)

// reservedFieldNames holds the identifiers a generated table type already uses.
// A column whose generated method name lands on one of them is rejected,
// because the method would shadow the embedded rasql.Table or its own methods,
// or collide with a mapping method declared on the row type.
//
// DecodeRow is deliberately absent, and the list is exactly these eight. Nothing
// emits a DecodeRow method any more, so a decode_row column collides with
// nothing: PackageSource with such a column returns a nil error and emits an
// ordinary DecodeRow field. Re-adding it here would reject a legitimate column
// name for a method that does not exist.
var reservedFieldNames = map[string]struct{}{
	"As":               {},
	"Column":           {},
	"ColumnValue":      {},
	"Ref":              {},
	"ScanDestinations": {},
	"ScanRow":          {},
	"Table":            {},
	"tableRow":         {},
}

// Validate checks whether packageName and tables can produce Go source.
// It checks names across all tables, so callers that generate one file per
// table can still reject collisions between those files before writing any.
func Validate(packageName string, tables ...schema.TableDef) error {
	_, err := prepareSchema(packageName, tables)
	return err
}

// PackageSource returns a whole generated package for tables: the row types,
// table types, column accessors and relationships, together with every
// table's runtime descriptor. Its output is a complete compilation unit, so
// a caller that writes it as the only file of a package still compiles.
func PackageSource(packageName string, tables ...schema.TableDef) ([]byte, error) {
	clones, err := prepareSchema(packageName, tables)
	if err != nil {
		return nil, err
	}
	return schemaSource(packageName, clones, clones, withDescriptors)
}

// TableSource returns a whole generated package holding one table. allTables
// supplies the package-wide descriptors used to derive relationship methods,
// and only table itself is emitted. generate.TableSource documents what a
// caller gets, including the one thing a table's own descriptor cannot
// supply: the generated type of a table at the other end of a relationship.
func TableSource(packageName string, table schema.TableDef, allTables ...schema.TableDef) ([]byte, error) {
	return tableSource(packageName, table, allTables, withDescriptors)
}

// TableSurfaceSource returns the same source as TableSource without the
// table's runtime descriptor, for a caller that writes DescriptorSource's
// output beside it. Its output alone does not compile: the package-level
// accessor function it emits reads the table value the descriptor file
// declares.
//
// rasqlgen is that caller. It writes one file per table plus a single
// schema_gen.go for the whole package, so a descriptor emitted per table
// would be declared twice.
func TableSurfaceSource(packageName string, table schema.TableDef, allTables ...schema.TableDef) ([]byte, error) {
	return tableSource(packageName, table, allTables, withoutDescriptors)
}

func tableSource(packageName string, table schema.TableDef, allTables []schema.TableDef, descriptors descriptorMode) ([]byte, error) {
	if len(allTables) == 0 {
		allTables = []schema.TableDef{table}
	}
	clones, err := prepareSchema(packageName, allTables)
	if err != nil {
		return nil, err
	}
	for _, candidate := range clones {
		if candidate.Schema == table.Schema && candidate.Name == table.Name {
			return schemaSource(packageName, []schema.TableDef{candidate}, clones, descriptors)
		}
	}
	return nil, fmt.Errorf("generate: table %q is not present in allTables", table.QualifiedName())
}

// descriptorMode says whether schemaSource emits each table's runtime
// descriptor beside its typed surface. Only the split-file caller leaves it
// out, and only because it writes those descriptors itself.
type descriptorMode bool

const (
	withDescriptors    descriptorMode = true
	withoutDescriptors descriptorMode = false
)

// schemaSource emits the typed surface for tables: the row type, its scan
// methods, the table type, its column accessors, its package-level accessor
// function, As, and any relationships. Each table's runtime descriptor comes
// with it unless descriptors says otherwise.
func schemaSource(packageName string, tables, allTables []schema.TableDef, descriptors descriptorMode) ([]byte, error) {

	var source bytes.Buffer
	source.WriteString("// Code generated by rasqlgen; DO NOT EDIT.\n\n")
	source.WriteString("package ")
	source.WriteString(packageName)
	source.WriteString("\n\n")
	// An empty schema declares nothing, so importing anything would not compile.
	if len(tables) > 0 {
		source.WriteString("import (\n")
		source.WriteString("\t\"fmt\"\n")
		if containsTime(tables) {
			source.WriteString("\t\"time\"\n")
		}
		if containsRelationships(tables, allTables) {
			source.WriteString("\t\"context\"\n")
		}
		source.WriteString("\n")
		source.WriteString("\t\"github.com/lestrrat-go/rasql\"\n")
		source.WriteString("\t\"github.com/lestrrat-go/rasql/query\"\n")
		// The descriptor literal is the only thing here that names the
		// schema package, so leaving it out leaves the import out too.
		if descriptors == withDescriptors {
			source.WriteString("\t\"github.com/lestrrat-go/rasql/schema\"\n")
		}
		source.WriteString(")\n\n")
	}
	for _, table := range tables {
		writeRowType(&source, table)
		source.WriteString("\n")
		writeRowScan(&source, table)
		source.WriteString("\n")
		writeRowColumnValue(&source, table)
		source.WriteString("\n")
		writeTableType(&source, table)
		source.WriteString("\n")
		writeTableColumns(&source, table)
		source.WriteString("\n")
		if descriptors == withDescriptors {
			writeTableDescriptor(&source, table)
			source.WriteString("\n")
		}
		writeTableAccessor(&source, table)
		source.WriteString("\n")
		writeTableAs(&source, table)
		source.WriteString("\n")
		writeRelationships(&source, table, allTables)
		source.WriteString("\n")
	}
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("generate: format source: %w", err)
	}
	return formatted, nil
}

// DescriptorSource returns the file holding every table's runtime
// descriptor. generate.DescriptorSource documents what a caller gets; what
// matters here is that it is the companion of TableSurfaceSource, not of
// TableSource, and that its literal is the merged definition prepareSchema
// produces rather than the input one.
func DescriptorSource(packageName string, tables ...schema.TableDef) ([]byte, error) {
	clones, err := prepareSchema(packageName, tables)
	if err != nil {
		return nil, err
	}
	return descriptorSource(packageName, clones)
}

func descriptorSource(packageName string, tables []schema.TableDef) ([]byte, error) {
	var source bytes.Buffer
	source.WriteString("// Code generated by rasqlgen; DO NOT EDIT.\n\n")
	source.WriteString("package ")
	source.WriteString(packageName)
	source.WriteString("\n\n")
	if len(tables) > 0 {
		source.WriteString("import (\n")
		source.WriteString("\t\"github.com/lestrrat-go/rasql\"\n")
		source.WriteString("\t\"github.com/lestrrat-go/rasql/schema\"\n")
		source.WriteString(")\n\n")
	}
	for _, table := range tables {
		writeTableDescriptor(&source, table)
		source.WriteString("\n")
	}
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("generate: format source: %w", err)
	}
	return formatted, nil
}

// writeTableDescriptor writes the unexported schema.TableDef literal, the
// unexported table value built from it, and the exported accessor that
// clones the literal for a caller who needs their own copy. The literal
// variable stays unexported because rasql.TableFrom does not clone: the
// table value shares its slices in place, so an exported variable would let
// any importer change a live table by writing to one of its fields.
func writeTableDescriptor(source *bytes.Buffer, table schema.TableDef) {
	source.WriteString("var ")
	source.WriteString(definitionName(table.Name))
	source.WriteString(" = ")
	writeTableDefLiteral(source, table)
	source.WriteString("\n\n")

	source.WriteString("var ")
	source.WriteString(descriptorName(table.Name))
	source.WriteString(" = ")
	source.WriteString(tableTypeName(table.Name))
	source.WriteString("{rasql.TableFrom[")
	source.WriteString(rowTypeName(table))
	source.WriteString("](")
	source.WriteString(definitionName(table.Name))
	source.WriteString(")}\n\n")

	source.WriteString("// ")
	source.WriteString(definitionAccessorName(table.Name))
	source.WriteString(" returns a copy of the descriptor for the ")
	source.WriteString(quote(table.Name))
	source.WriteString(" table.\n")
	source.WriteString("func ")
	source.WriteString(definitionAccessorName(table.Name))
	source.WriteString("() schema.TableDef { return ")
	source.WriteString(definitionName(table.Name))
	source.WriteString(".Clone() }\n")
}

// descriptorTestFuncName is the name descriptorTestSource gives the test it
// writes, and the one defining site of that literal outside the checked-in
// generated files themselves.
//
// The generated test is an internal test in the caller's own package, so it
// shares a declaration space with every hand-written test file beside it. A
// plain name such as TestGeneratedDefinitionsAreValid is one a person could
// reasonably write for a test of their own, and a package that already
// declared it would stop compiling the moment rasqlgen wrote the generated
// file. Nothing catches that first: rasqlgen inspects only the destination
// path it writes, never the other files in the package, and the colliding
// declaration lives in one of those.
//
// Naming the generator makes the collision implausible instead of merely
// unlikely, and the name is a constant rather than derived from the package
// or the descriptors, so regenerating the same input keeps producing the same
// bytes.
//
// Being fixed does not make it safe on its own: a table named
// test_rasqlgen_generated_definitions_are_valid derives exactly this
// identifier for its accessor, and both declarations would land in files
// rasqlgen writes. reservedPackageNames is what stops that.
const descriptorTestFuncName = "TestRasqlgenGeneratedDefinitionsAreValid"

// reservedPackageNames holds every package-level identifier rasqlgen emits
// with a fixed spelling rather than deriving it from a table. validateVariableNames
// seeds its collision set with these, so a table whose generated name spells one
// of them is refused with a collision error instead of generating a package
// with two declarations of the same name.
//
// Names derived from a table name are not listed: validateVariableNames already
// compares those against each other as it walks the input. Names a generated
// type declares as a method rather than at package level are not listed either;
// reservedFieldNames holds those.
var reservedPackageNames = map[string]struct{}{
	descriptorTestFuncName: {},
}

// DescriptorTestSource returns the generated test that validates every
// table's descriptor. generate.DescriptorTestSource documents what a caller
// gets, including why the emitted test is an internal one.
func DescriptorTestSource(packageName string, tables ...schema.TableDef) ([]byte, error) {
	clones, err := prepareSchema(packageName, tables)
	if err != nil {
		return nil, err
	}
	return descriptorTestSource(packageName, clones)
}

func descriptorTestSource(packageName string, tables []schema.TableDef) ([]byte, error) {
	var source bytes.Buffer
	source.WriteString("// Code generated by rasqlgen; DO NOT EDIT.\n\n")
	source.WriteString("package ")
	source.WriteString(packageName)
	source.WriteString("\n\n")
	source.WriteString("import (\n\t\"testing\"\n\n\t\"github.com/lestrrat-go/rasql/schema\"\n)\n\n")
	source.WriteString("func ")
	source.WriteString(descriptorTestFuncName)
	source.WriteString("(t *testing.T) {\n")
	source.WriteString("\tfor _, definition := range []schema.TableDef{")
	for index, table := range tables {
		if index > 0 {
			source.WriteString(", ")
		}
		source.WriteString(definitionName(table.Name))
	}
	source.WriteString("} {\n")
	source.WriteString("\t\tif err := definition.Validate(); err != nil {\n")
	source.WriteString("\t\t\tt.Errorf(\"%s: %s\", definition.Name, err)\n")
	source.WriteString("\t\t}\n")
	source.WriteString("\t}\n}\n")
	formatted, err := format.Source(source.Bytes())
	if err != nil {
		return nil, fmt.Errorf("generate: format source: %w", err)
	}
	return formatted, nil
}

func prepareSchema(packageName string, tables []schema.TableDef) ([]schema.TableDef, error) {
	if !token.IsIdentifier(packageName) {
		return nil, fmt.Errorf("generate: invalid package name %q", packageName)
	}
	clones := make([]schema.TableDef, len(tables))
	for i, table := range tables {
		if err := table.Validate(); err != nil {
			return nil, fmt.Errorf("generate: table at index %d: %w", i, err)
		}
		clones[i] = table.Clone()
		clones[i].Relationships = mergeRelationships(clones[i])
	}
	sort.Slice(clones, func(left, right int) bool {
		return clones[left].Name < clones[right].Name
	})
	if err := validateVariableNames(clones); err != nil {
		return nil, err
	}
	return clones, nil
}

func derivedRelationships(table schema.TableDef) []schema.RelationshipDef {
	relationships := make([]schema.RelationshipDef, 0, len(table.ForeignKeys))
	for _, key := range table.ForeignKeys {
		if len(key.Columns) == 0 {
			continue
		}
		name := key.Columns[0]
		name = strings.TrimSuffix(name, "_id")
		name = variableName(name)
		if name == "" {
			name = variableName(key.ReferencedTable)
		}
		relationships = append(relationships, schema.RelationshipDef{
			Name:              name,
			Kind:              schema.RelationshipBelongsTo,
			Columns:           append([]string(nil), key.Columns...),
			ReferencedSchema:  key.ReferencedSchema,
			ReferencedTable:   key.ReferencedTable,
			ReferencedColumns: append([]string(nil), key.ReferencedColumns...),
		})
	}
	return relationships
}

func mergeRelationships(table schema.TableDef) []schema.RelationshipDef {
	relationships := make([]schema.RelationshipDef, 0, len(table.ForeignKeys))
	matched := make([]bool, len(table.ForeignKeys))
	for _, relationship := range table.Relationships {
		relationships = append(relationships, relationship)
		for index, key := range table.ForeignKeys {
			if matched[index] || !relationshipMatchesForeignKey(relationship, key) {
				continue
			}
			matched[index] = true
			break
		}
	}
	for index, key := range table.ForeignKeys {
		if matched[index] {
			continue
		}
		derived := derivedRelationships(schema.TableDef{ForeignKeys: []schema.ForeignKeyDef{key}})
		relationships = append(relationships, derived...)
	}
	return relationships
}

func relationshipMatchesForeignKey(relationship schema.RelationshipDef, key schema.ForeignKeyDef) bool {
	return relationship.ReferencedSchema == key.ReferencedSchema &&
		relationship.ReferencedTable == key.ReferencedTable &&
		stringSlicesEqual(relationship.Columns, key.Columns) &&
		stringSlicesEqual(relationship.ReferencedColumns, key.ReferencedColumns)
}

func stringSlicesEqual(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

// generatedNameConflict returns the error for a package-level name a table
// would generate that is already taken. A name rasqlgen always emits, such as
// descriptorTestFuncName, is reported as reserved, because no other table in
// the input explains the clash and the caller's only remedy is to rename the
// table.
func generatedNameConflict(tableName, generated string) error {
	if _, reserved := reservedPackageNames[generated]; reserved {
		return fmt.Errorf("generate: table %q duplicates generated name %q, which rasqlgen reserves", tableName, generated)
	}
	return fmt.Errorf("generate: table %q duplicates generated name %q", tableName, generated)
}

func validateVariableNames(tables []schema.TableDef) error {
	names := make(map[string]struct{}, len(tables)*2+len(reservedPackageNames))
	// Seed the fixed package-level names rasqlgen writes beside the per-table
	// declarations, so a table whose own generated name spells one of them is
	// refused here instead of producing a package that does not compile.
	for reserved := range reservedPackageNames {
		names[reserved] = struct{}{}
	}
	for _, table := range tables {
		variable := variableName(table.Name)
		if variable == "" || !token.IsIdentifier(variable) {
			return fmt.Errorf("generate: table %q cannot become a Go identifier", table.Name)
		}
		if _, exists := names[variable]; exists {
			return generatedNameConflict(table.Name, variable)
		}
		names[variable] = struct{}{}
		if table.RowName != "" {
			if err := validateRowName(table); err != nil {
				return err
			}
		}
		for _, generated := range []string{
			rowTypeName(table),
			tableTypeName(table.Name),
			descriptorName(table.Name),
			definitionName(table.Name),
			definitionAccessorName(table.Name),
		} {
			if _, exists := names[generated]; exists {
				return generatedNameConflict(table.Name, generated)
			}
			names[generated] = struct{}{}
		}
		// timeScannerTypeName is deliberately absent from this set: a RowName can
		// never equal one. It derives from the TABLE name and is always unexported,
		// because descriptorName lowers the leading uppercase run of goName(tableName)
		// and ValidateIdentifier confines a table name to ASCII [A-Za-z_][A-Za-z0-9_]*,
		// so that lowered rune is always ASCII a-z. A stated RowName passes
		// validateExportedGoIdentifier, which requires an uppercase first rune. The two
		// name spaces are disjoint by exportedness.
		//
		// Checked, not assumed: a table with a time column and RowName
		// "UsersTimeScanner" emits both UsersTimeScanner and usersTimeScanner, which
		// are distinct Go identifiers, and the generated package builds and vets clean.
		// Setting RowName to the literal scanner name is rejected, same-table and
		// cross-table. Falsify this by finding an accepted table name whose
		// timeScannerTypeName begins with an uppercase rune.

		methods := make(map[string]struct{}, len(table.Columns))
		for _, column := range table.Columns {
			method := goName(column.Name)
			if method == "" || !token.IsIdentifier(method) {
				return fmt.Errorf("generate: column %q on table %q cannot become a Go method", column.Name, table.Name)
			}
			if _, exists := reservedFieldNames[method]; exists {
				return fmt.Errorf("generate: column %q on table %q uses reserved generated method %q", column.Name, table.Name, method)
			}
			if _, exists := methods[method]; exists {
				return fmt.Errorf("generate: column %q on table %q duplicates generated method %q", column.Name, table.Name, method)
			}
			methods[method] = struct{}{}
		}
		relationshipMethods := make(map[string]struct {
			index int
			name  string
		})
		for index, relationship := range table.Relationships {
			if !relationshipSupportedForTable(table, relationship, tables) {
				continue
			}
			method := goName(relationship.Name)
			if !token.IsIdentifier(method) {
				return fmt.Errorf("generate: relationship %q on table %q cannot become a Go method", relationship.Name, table.Name)
			}
			if reservedRelationshipMethod(method) {
				return fmt.Errorf("generate: relationship %q on table %q uses reserved generated method %q", relationship.Name, table.Name, method)
			}
			if previous, exists := relationshipMethods[method]; exists {
				return fmt.Errorf("generate: relationships[%d] %q and relationships[%d] %q on table %q duplicate generated method %q", previous.index, previous.name, index, relationship.Name, table.Name, method)
			}
			relationshipMethods[method] = struct {
				index int
				name  string
			}{index: index, name: relationship.Name}
		}
		for _, relationship := range table.Relationships {
			if !relationshipSupportedForTable(table, relationship, tables) {
				continue
			}
			method := goName(relationship.Name)
			if _, exists := methods[method]; exists {
				return fmt.Errorf("generate: relationship %q on table %q collides with generated method %q", relationship.Name, table.Name, method)
			}
		}
		for _, relationship := range relationshipSpecs(table, tables) {
			if _, exists := methods[relationship.method]; exists {
				return fmt.Errorf("generate: relationship %q on table %q collides with generated method %q", relationship.method, table.Name, relationship.method)
			}
		}
	}
	for _, table := range tables {
		for _, relationship := range relationshipSpecs(table, tables) {
			if _, exists := names[relationship.typeName]; exists {
				return fmt.Errorf("generate: relationship %q on table %q duplicates generated name %q", relationship.method, table.Name, relationship.typeName)
			}
			names[relationship.typeName] = struct{}{}
		}
	}
	return nil
}

// validateRowName rejects a stated RowName that collides with another name
// rasqlgen generates for table's own row, naming RowNamed in the message so
// the error blames the option a person typed by hand rather than the table.
// schema.TableDef.Validate, which every entry point into this package runs
// before validateVariableNames, already guarantees a non-empty RowName is a
// valid, exported Go identifier, so this checks only collisions specific to
// the generated surface: a reserved method name, and this table's own
// accessor, table type, descriptor variable, definition variable, or
// definition accessor. A collision with another table's generated names,
// with a relationship type name, or with a fixed name in
// reservedPackageNames is caught afterward by the shared names set the
// caller builds from rowTypeName(table).
func validateRowName(table schema.TableDef) error {
	if _, reserved := reservedFieldNames[table.RowName]; reserved {
		return fmt.Errorf("generate: table %q states row name %q via RowNamed, which is a reserved generated name", table.Name, table.RowName)
	}
	for _, collision := range []struct {
		label     string
		generated string
	}{
		{"accessor", variableName(table.Name)},
		{"table type", tableTypeName(table.Name)},
		{"descriptor variable", descriptorName(table.Name)},
		{"definition variable", definitionName(table.Name)},
		{"definition accessor", definitionAccessorName(table.Name)},
	} {
		if table.RowName == collision.generated {
			return fmt.Errorf("generate: table %q states row name %q via RowNamed, which collides with its own generated %s", table.Name, table.RowName, collision.label)
		}
	}
	return nil
}

func relationshipSupported(child, parent schema.TableDef, relationship schema.RelationshipDef) (schema.ColumnDef, schema.ColumnDef, string, bool) {
	if relationship.Kind != schema.RelationshipBelongsTo || len(relationship.Columns) != 1 || len(relationship.ReferencedColumns) != 1 {
		return schema.ColumnDef{}, schema.ColumnDef{}, "", false
	}
	childColumn, ok := child.Column(relationship.Columns[0])
	if !ok || childColumn.Nullable {
		return schema.ColumnDef{}, schema.ColumnDef{}, "", false
	}
	parentColumn, ok := parent.Column(relationship.ReferencedColumns[0])
	if !ok || parentColumn.Nullable || len(parent.PrimaryKey) != 1 || parent.PrimaryKey[0] != parentColumn.Name {
		return schema.ColumnDef{}, schema.ColumnDef{}, "", false
	}
	keyType, ok := relationKeyType(parentColumn)
	if !ok || keyType != rowFieldType(childColumn) {
		return schema.ColumnDef{}, schema.ColumnDef{}, "", false
	}
	return parentColumn, childColumn, keyType, true
}

func relationshipSupportedForTable(table schema.TableDef, relationship schema.RelationshipDef, allTables []schema.TableDef) bool {
	parent, ok := relationshipTable(allTables, relationship.ReferencedSchema, relationship.ReferencedTable)
	if !ok {
		return false
	}
	_, _, _, ok = relationshipSupported(table, parent, relationship)
	return ok
}

// variableName returns the exported Go identifier for a table name. It
// delegates to goName so tables use the same initialism rules as columns:
// a table named api_keys and a column named api_key both spell "API" the
// same way, instead of the table getting a plain "Api".
func variableName(name string) string {
	return goName(name)
}

// rowTypeName returns the Go row type generated for table: table.RowName
// when the descriptor states one, or the default <Table>Row otherwise.
func rowTypeName(table schema.TableDef) string {
	if table.RowName != "" {
		return table.RowName
	}
	return variableName(table.Name) + "Row"
}

// tableTypeName returns the exported wrapper type holding the typed table and
// its column fields.
func tableTypeName(tableName string) string {
	return variableName(tableName) + "Table"
}

// DescriptorVarName returns the package-level variable a generated table
// accessor reads, which is the single declaration a TableSurfaceSource file
// needs from the descriptor file written beside it. rasqlgen names it when
// it refuses a run, to say which value a generated file already in the
// output directory reads and whether that run still declares it. Which
// files a run may leave behind is decided by their names, not by this one.
func DescriptorVarName(tableName string) string {
	return descriptorName(tableName)
}

// descriptorName returns the unexported variable name backing an accessor.
// Distinctness across accessors is enforced by validateVariableNames, which
// includes descriptor names in its collision set, not by any property of
// this lowering.
func descriptorName(tableName string) string {
	accessor := variableName(tableName)
	if accessor == "" {
		return ""
	}
	runes := []rune(accessor)
	// Find the leading run of uppercase runes: for "APIKeys" that is "APIK",
	// because casing alone cannot tell an initialism from the capital that
	// starts the next word. When the run stops partway through the name, its
	// last rune is that next word's leading capital (the "K" of "Keys") and
	// stays as-is; only the initialism before it gets lowered, so "APIKeys"
	// becomes "apiKeys" rather than "apikeys". A run spanning the whole name
	// (e.g. "ID") or only the first rune (e.g. "Users") has no such word
	// boundary to protect, so it lowers completely.
	end := 0
	for end < len(runes) && unicode.IsUpper(runes[end]) {
		end++
	}
	if end > 1 && end < len(runes) {
		end--
	}
	for index := 0; index < end; index++ {
		runes[index] = unicode.ToLower(runes[index])
	}
	return string(runes) + "Table"
}

// definitionName returns the unexported variable name holding a table's
// schema.TableDef literal, derived from descriptorName the same way
// timeScannerTypeName derives from it, so the three package-level names for
// one table stay in step.
func definitionName(tableName string) string {
	return strings.TrimSuffix(descriptorName(tableName), "Table") + "Def"
}

// definitionAccessorName returns the exported function name that hands back a
// clone of a table's schema.TableDef.
func definitionAccessorName(tableName string) string {
	return variableName(tableName) + "Def"
}

func goName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool {
		return r == '_'
	})
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
					continue
				}
				result.WriteRune(r)
			}
		}
	}
	return result.String()
}

func containsTime(tables []schema.TableDef) bool {
	for _, table := range tables {
		for _, column := range table.Columns {
			if column.Type.Kind() == schema.KindTime {
				return true
			}
		}
	}
	return false
}

// writeTableType writes the exported wrapper type: the typed table plus one
// query.ColumnRef field per column, so a mistyped column name fails to compile.
func writeTableType(source *bytes.Buffer, table schema.TableDef) {
	typeName := tableTypeName(table.Name)
	source.WriteString("// ")
	source.WriteString(typeName)
	source.WriteString(" is the generated table type for the ")
	source.WriteString(quote(table.Name))
	source.WriteString(" table.\n")
	source.WriteString("type ")
	source.WriteString(typeName)
	source.WriteString(" struct {\n\trasql.Table[")
	source.WriteString(rowTypeName(table))
	source.WriteString("]\n}\n")
}

// writeTableColumns writes one accessor method per column, returning a
// reference to that column on t.
func writeTableColumns(source *bytes.Buffer, table schema.TableDef) {
	typeName := tableTypeName(table.Name)
	for _, column := range table.Columns {
		method := goName(column.Name)
		source.WriteString("// ")
		source.WriteString(method)
		source.WriteString(" returns a reference to the ")
		source.WriteString(quote(column.Name))
		source.WriteString(" column.\n")
		source.WriteString("func (t ")
		source.WriteString(typeName)
		source.WriteString(") ")
		source.WriteString(method)
		source.WriteString("() query.ColumnRef { return rasql.ColumnOf(t.Table, ")
		source.WriteString(quote(column.Name))
		source.WriteString(") }\n")
	}
}

func writeTableAccessor(source *bytes.Buffer, table schema.TableDef) {
	accessor := variableName(table.Name)
	source.WriteString("// ")
	source.WriteString(accessor)
	source.WriteString(" returns the descriptor for the ")
	source.WriteString(quote(table.Name))
	source.WriteString(" table.\n")
	source.WriteString("func ")
	source.WriteString(accessor)
	source.WriteString("() ")
	source.WriteString(tableTypeName(table.Name))
	source.WriteString(" {\n\treturn ")
	source.WriteString(descriptorName(table.Name))
	source.WriteString("\n}\n")
}

// writeTableAs writes the alias constructor. Its body is fixed regardless of
// table width: the accessor methods read the embedded table they are called
// on, so nothing needs rebinding.
func writeTableAs(source *bytes.Buffer, table schema.TableDef) {
	typeName := tableTypeName(table.Name)
	source.WriteString("// As returns the table under alias.\n")
	source.WriteString("func (t ")
	source.WriteString(typeName)
	source.WriteString(") As(alias string) (")
	source.WriteString(typeName)
	source.WriteString(", error) {\n")
	source.WriteString("\taliased, err := rasql.As(t.Table, alias)\n")
	source.WriteString("\tif err != nil {\n\t\treturn ")
	source.WriteString(typeName)
	source.WriteString("{}, err\n\t}\n\treturn ")
	source.WriteString(typeName)
	source.WriteString("{Table: aliased}, nil\n}\n")
}

func writeRowType(source *bytes.Buffer, table schema.TableDef) {
	source.WriteString("type ")
	source.WriteString(rowTypeName(table))
	source.WriteString(" struct {\n")
	for _, column := range table.Columns {
		source.WriteString("\t")
		source.WriteString(goName(column.Name))
		source.WriteByte(' ')
		if column.Nullable {
			source.WriteByte('*')
		}
		source.WriteString(rowFieldType(column))
		source.WriteString("\n")
	}
	source.WriteString("}\n")
}

type relationshipSpec struct {
	kind          schema.RelationshipKind
	method        string
	typeName      string
	parent        schema.TableDef
	child         schema.TableDef
	parentColumn  schema.ColumnDef
	childColumn   schema.ColumnDef
	parentField   string
	childField    string
	parentKeyType string
}

type inverseRelationshipCandidate struct {
	child        schema.TableDef
	relationship schema.RelationshipDef
	parentColumn schema.ColumnDef
	childColumn  schema.ColumnDef
	keyType      string
}

func containsRelationships(tables, allTables []schema.TableDef) bool {
	for _, table := range tables {
		if len(relationshipSpecs(table, allTables)) > 0 {
			return true
		}
	}
	return false
}

func relationshipSpecs(table schema.TableDef, allTables []schema.TableDef) []relationshipSpec {
	result := make([]relationshipSpec, 0)
	usedMethods := make(map[string]struct{})
	for _, relationship := range table.Relationships {
		parent, ok := relationshipTable(allTables, relationship.ReferencedSchema, relationship.ReferencedTable)
		if !ok {
			continue
		}
		parentColumn, childColumn, keyType, ok := relationshipSupported(table, parent, relationship)
		if !ok {
			continue
		}
		method := goName(relationship.Name)
		if !token.IsIdentifier(method) || reservedRelationshipMethod(method) {
			continue
		}
		if _, exists := usedMethods[method]; exists {
			continue
		}
		usedMethods[method] = struct{}{}
		result = append(result, relationshipSpec{
			kind:          schema.RelationshipBelongsTo,
			method:        method,
			typeName:      tableTypeName(table.Name) + method + "Relation",
			parent:        parent,
			child:         table,
			parentColumn:  parentColumn,
			childColumn:   childColumn,
			parentField:   goName(parentColumn.Name),
			childField:    goName(childColumn.Name),
			parentKeyType: keyType,
		})
	}

	candidates := make([]inverseRelationshipCandidate, 0)
	for _, child := range allTables {
		for _, relationship := range child.Relationships {
			if relationship.ReferencedTable != table.Name || relationship.ReferencedSchema != table.Schema {
				continue
			}
			parentColumn, childColumn, keyType, ok := relationshipSupported(child, table, relationship)
			if !ok {
				continue
			}
			candidates = append(candidates, inverseRelationshipCandidate{
				child:        child,
				relationship: relationship,
				parentColumn: parentColumn,
				childColumn:  childColumn,
				keyType:      keyType,
			})
		}
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftKey := inverseRelationshipSortKey(candidates[left])
		rightKey := inverseRelationshipSortKey(candidates[right])
		return leftKey < rightKey
	})
	for _, candidate := range candidates {
		method := inverseRelationshipMethodName(candidate.child, candidate.relationship, usedMethods)
		if method == "" {
			continue
		}
		usedMethods[method] = struct{}{}
		result = append(result, relationshipSpec{
			kind:          schema.RelationshipHasMany,
			method:        method,
			typeName:      tableTypeName(table.Name) + method + "Relation",
			parent:        table,
			child:         candidate.child,
			parentColumn:  candidate.parentColumn,
			childColumn:   candidate.childColumn,
			parentField:   goName(candidate.parentColumn.Name),
			childField:    goName(candidate.childColumn.Name),
			parentKeyType: candidate.keyType,
		})
	}
	return result
}

func inverseRelationshipSortKey(candidate inverseRelationshipCandidate) string {
	return strings.Join([]string{
		candidate.child.Schema,
		candidate.child.Name,
		candidate.relationship.Name,
		strings.Join(candidate.relationship.Columns, "\x00"),
		strings.Join(candidate.relationship.ReferencedColumns, "\x00"),
		candidate.parentColumn.Name,
		candidate.childColumn.Name,
	}, "\x00")
}

// Inverse methods use the child table name when it is available. A collision
// or reserved name gets the relationship name as a stable prefix, followed by
// a numeric suffix only when that name is also occupied.
func inverseRelationshipMethodName(child schema.TableDef, relationship schema.RelationshipDef, usedMethods map[string]struct{}) string {
	base := variableName(child.Name)
	if base == "" {
		return ""
	}
	if _, exists := usedMethods[base]; !exists && !reservedRelationshipMethod(base) {
		return base
	}

	prefix := goName(relationship.Name)
	if prefix == "" {
		return ""
	}
	base = prefix + base
	if _, exists := usedMethods[base]; !exists && !reservedRelationshipMethod(base) {
		return base
	}
	for suffix := 2; ; suffix++ {
		candidate := base + strconv.Itoa(suffix)
		if _, exists := usedMethods[candidate]; !exists && !reservedRelationshipMethod(candidate) {
			return candidate
		}
	}
}

func relationshipTable(tables []schema.TableDef, referencedSchema, referencedName string) (schema.TableDef, bool) {
	for _, table := range tables {
		if table.Name != referencedName {
			continue
		}
		if table.Schema != referencedSchema {
			continue
		}
		return table, true
	}
	return schema.TableDef{}, false
}

func relationKeyType(column schema.ColumnDef) (string, bool) {
	fieldType := rowFieldType(column)
	if fieldType == "[]byte" || column.Nullable {
		return "", false
	}
	return fieldType, true
}

func reservedRelationshipMethod(name string) bool {
	_, reserved := reservedFieldNames[name]
	return reserved
}

func selfRelationshipAlias(relationship relationshipSpec) string {
	role := "parent"
	if relationship.kind == schema.RelationshipHasMany {
		role = "child"
	}
	return strings.ToLower(variableName(relationship.parent.Name)) + "_" + strings.ToLower(relationship.method) + "_" + role
}

func writeRelationships(source *bytes.Buffer, table schema.TableDef, allTables []schema.TableDef) {
	for _, relationship := range relationshipSpecs(table, allTables) {
		source.WriteString("// ")
		source.WriteString(relationship.typeName)
		source.WriteString(" describes the ")
		source.WriteString(relationship.method)
		source.WriteString(" relationship from ")
		source.WriteString(tableTypeName(table.Name))
		source.WriteString(".\n")
		source.WriteString("type ")
		source.WriteString(relationship.typeName)
		source.WriteString(" struct {\n")
		source.WriteString("\tParent ")
		source.WriteString(tableTypeName(relationship.parent.Name))
		source.WriteString("\n\tChild ")
		source.WriteString(tableTypeName(relationship.child.Name))
		source.WriteString("\n\tParentKey query.ColumnRef\n\tChildKey query.ColumnRef\n}\n\n")

		source.WriteString("// ")
		source.WriteString(relationship.method)
		source.WriteString(" returns the generated relationship descriptor.\n")
		source.WriteString("func (t ")
		source.WriteString(tableTypeName(table.Name))
		source.WriteString(") ")
		source.WriteString(relationship.method)
		source.WriteString("() ")
		source.WriteString(relationship.typeName)
		source.WriteString(" {\n")
		if relationship.kind == schema.RelationshipHasMany {
			source.WriteString("\tchild := ")
			source.WriteString(variableName(relationship.child.Name))
			source.WriteString("()\n\tparent := t\n")
		} else {
			source.WriteString("\tchild := t\n\tparent := ")
			source.WriteString(variableName(relationship.parent.Name))
			source.WriteString("()\n")
		}
		if relationship.parent.Schema == relationship.child.Schema && relationship.parent.Name == relationship.child.Name {
			joined := "parent"
			if relationship.kind == schema.RelationshipHasMany {
				joined = "child"
			}
			source.WriteString("\taliased, err := ")
			source.WriteString(joined)
			source.WriteString(".As(")
			source.WriteString(quote(selfRelationshipAlias(relationship)))
			source.WriteString(")\n\tif err != nil {\n\t\tpanic(err)\n\t}\n\t")
			source.WriteString(joined)
			source.WriteString(" = aliased\n")
		}
		source.WriteString("\treturn ")
		source.WriteString(relationship.typeName)
		source.WriteString("{Parent: parent, Child: child, ParentKey: parent.")
		source.WriteString(relationship.parentField)
		source.WriteString("(), ChildKey: child.")
		source.WriteString(relationship.childField)
		source.WriteString("()}\n}\n\n")

		source.WriteString("// Join returns an INNER JOIN for the relationship.\n")
		source.WriteString("func (r ")
		source.WriteString(relationship.typeName)
		source.WriteString(") Join() query.Join {\n\treturn rasql.InnerJoin(r.")
		if relationship.kind == schema.RelationshipHasMany {
			source.WriteString("Child")
		} else {
			source.WriteString("Parent")
		}
		source.WriteString(", query.Equal(r.ParentKey, r.ChildKey))\n}\n\n")
		writeRelationshipLoad(source, relationship)
	}
}

func writeRelationshipLoad(source *bytes.Buffer, relationship relationshipSpec) {
	if relationship.kind == schema.RelationshipHasMany {
		source.WriteString("// Load fetches all children for parents in one query and groups them by parent key.\n")
		source.WriteString("func (r ")
		source.WriteString(relationship.typeName)
		source.WriteString(") Load(ctx context.Context, db rasql.DB, parents []")
		source.WriteString(rowTypeName(relationship.parent))
		source.WriteString(") (map[")
		source.WriteString(relationship.parentKeyType)
		source.WriteString("][]")
		source.WriteString(rowTypeName(relationship.child))
		source.WriteString(", error) {\n\treturn rasql.LoadHasMany[")
		source.WriteString(rowTypeName(relationship.parent))
		source.WriteString(", ")
		source.WriteString(rowTypeName(relationship.child))
		source.WriteString(", ")
		source.WriteString(relationship.parentKeyType)
		source.WriteString("](ctx, db, r.Child, r.ChildKey, parents, func(row ")
		source.WriteString(rowTypeName(relationship.parent))
		source.WriteString(") ")
		source.WriteString(relationship.parentKeyType)
		source.WriteString(" { return row.")
		source.WriteString(relationship.parentField)
		source.WriteString(" }, func(row ")
		source.WriteString(rowTypeName(relationship.child))
		source.WriteString(") ")
		source.WriteString(relationship.parentKeyType)
		source.WriteString(" { return row.")
		source.WriteString(relationship.childField)
		source.WriteString(" })\n}\n\n")
		return
	}

	source.WriteString("// Load fetches all related parents for children in one query and groups them by foreign-key value.\n")
	source.WriteString("func (r ")
	source.WriteString(relationship.typeName)
	source.WriteString(") Load(ctx context.Context, db rasql.DB, children []")
	source.WriteString(rowTypeName(relationship.child))
	source.WriteString(") (map[")
	source.WriteString(relationship.parentKeyType)
	source.WriteString("]")
	source.WriteString(rowTypeName(relationship.parent))
	source.WriteString(", error) {\n\treturn rasql.LoadBelongsTo[")
	source.WriteString(rowTypeName(relationship.child))
	source.WriteString(", ")
	source.WriteString(rowTypeName(relationship.parent))
	source.WriteString(", ")
	source.WriteString(relationship.parentKeyType)
	source.WriteString("](ctx, db, r.Parent, r.ParentKey, children, func(row ")
	source.WriteString(rowTypeName(relationship.child))
	source.WriteString(") ")
	source.WriteString(relationship.parentKeyType)
	source.WriteString(" { return row.")
	source.WriteString(relationship.childField)
	source.WriteString(" }, func(row ")
	source.WriteString(rowTypeName(relationship.parent))
	source.WriteString(") ")
	source.WriteString(relationship.parentKeyType)
	source.WriteString(" { return row.")
	source.WriteString(relationship.parentField)
	source.WriteString(" })\n}\n\n")
}

// writeRowScan writes the direct database/sql scan path and the runtime result
// column mapping path.
func writeRowScan(source *bytes.Buffer, table schema.TableDef) {
	typeName := rowTypeName(table)
	if tableHasTimeColumn(table) {
		source.WriteString("type ")
		source.WriteString(timeScannerTypeName(table.Name))
		source.WriteString(" func(any) error\n\n")
		source.WriteString("func (s ")
		source.WriteString(timeScannerTypeName(table.Name))
		source.WriteString(") Scan(value any) error {\n\treturn s(value)\n}\n\n")
	}
	source.WriteString("// ScanRow scans each result column directly into its field.\n")
	source.WriteString("func (r *")
	source.WriteString(typeName)
	source.WriteString(") ScanRow(src rasql.ScanSource) error {\n")
	for index, column := range table.Columns {
		if !isTimeColumn(column) {
			continue
		}
		source.WriteString("\ttimeScanner")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(" := ")
		writeTimeScannerLiteral(source, table.Name, "&r."+goName(column.Name))
		source.WriteString("\n")
	}
	source.WriteString("\treturn src.Scan(")
	for index, column := range table.Columns {
		if index > 0 {
			source.WriteString(", ")
		}
		if isTimeColumn(column) {
			source.WriteString("&timeScanner")
			source.WriteString(strconv.Itoa(index))
		} else {
			source.WriteString("&r.")
			source.WriteString(goName(column.Name))
		}
	}
	source.WriteString(")\n}\n")

	source.WriteString("\n// ScanDestinations maps result-column names to fields on r.\n")
	source.WriteString("func (r *")
	source.WriteString(typeName)
	source.WriteString(") ScanDestinations(columns []string) ([]any, error) {\n")
	if len(table.Columns) > 0 {
		source.WriteString("\tconst (\n")
		for index, column := range table.Columns {
			source.WriteString("\t\t")
			source.WriteString(scanIndexName(column.Name))
			if index == 0 {
				source.WriteString(" = iota")
			}
			source.WriteString("\n")
		}
		source.WriteString("\t)\n")
	}
	for index, column := range table.Columns {
		if !isTimeColumn(column) {
			continue
		}
		source.WriteString("\ttimeScanner")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(" := ")
		writeTimeScannerLiteral(source, table.Name, "&r."+goName(column.Name))
		source.WriteString("\n")
	}
	source.WriteString("\tdestinations := make([]any, len(columns))\n")
	if len(table.Columns) > 0 {
		source.WriteString("\tscanned := rasql.NewScanMask(")
		source.WriteString(strconv.Itoa(len(table.Columns)))
		source.WriteString(")\n")
	}
	source.WriteString("\tvar discard any\n")
	source.WriteString("\tfor index, column := range columns {\n\t\tswitch column {\n")
	for index, column := range table.Columns {
		field := goName(column.Name)
		source.WriteString("\t\tcase ")
		source.WriteString(quote(column.Name))
		source.WriteString(":\n\t\t\tif !scanned.Mark(")
		source.WriteString(scanIndexName(column.Name))
		source.WriteString(") {\n\t\t\t\treturn nil, fmt.Errorf(\"duplicate result column %q\", column)\n\t\t\t}\n")
		source.WriteString("\t\t\tdestinations[index] = ")
		if isTimeColumn(column) {
			source.WriteString("&timeScanner")
			source.WriteString(strconv.Itoa(index))
		} else {
			source.WriteString("&r.")
			source.WriteString(field)
		}
		source.WriteString("\n")
	}
	source.WriteString("\t\tdefault:\n\t\t\tdestinations[index] = &discard\n\t\t}\n\t}\n")
	source.WriteString("\treturn destinations, nil\n}\n")
}

func tableHasTimeColumn(table schema.TableDef) bool {
	for _, column := range table.Columns {
		if isTimeColumn(column) {
			return true
		}
	}
	return false
}

func isTimeColumn(column schema.ColumnDef) bool {
	return column.Type.Kind() == schema.KindTime
}

func timeScannerTypeName(tableName string) string {
	return strings.TrimSuffix(descriptorName(tableName), "Table") + "TimeScanner"
}

func writeTimeScannerLiteral(source *bytes.Buffer, tableName string, destination string) {
	source.WriteString(timeScannerTypeName(tableName))
	source.WriteString("(func(value any) error {\n")
	source.WriteString("\t\treturn rasql.ScanValue(")
	source.WriteString(destination)
	source.WriteString(", value)\n\t})")
}

// scanIndexName returns the name of the local constant that holds a column's
// position in the row type's [rasql.ScanMask].
func scanIndexName(columnName string) string {
	return "scanIndex" + goName(columnName)
}

// writeRowColumnValue writes the rasql.ColumnValuer implementation, which is
// the write-direction counterpart of the scan methods.
func writeRowColumnValue(source *bytes.Buffer, table schema.TableDef) {
	typeName := rowTypeName(table)
	source.WriteString("// ColumnValue returns the value of the named column.\n")
	source.WriteString("func (r ")
	source.WriteString(typeName)
	source.WriteString(") ColumnValue(name string) (any, bool) {\n")
	source.WriteString("\tswitch name {\n")
	for _, column := range table.Columns {
		source.WriteString("\tcase ")
		source.WriteString(quote(column.Name))
		source.WriteString(":\n\t\treturn r.")
		source.WriteString(goName(column.Name))
		source.WriteString(", true\n")
	}
	source.WriteString("\t}\n\treturn nil, false\n}\n")
}

// rowFieldType returns the Go type of the row field holding column. It takes
// the whole column because signedness changes the answer: an unsigned integer
// column reaches 18446744073709551615, which int64 cannot hold, so it generates
// a uint64 field. Every other integer column keeps int64.
func rowFieldType(column schema.ColumnDef) string {
	switch typed := column.Type.(type) {
	case schema.IntegerType:
		if typed.Unsigned {
			return "uint64"
		}
		return "int64"
	case schema.BooleanType:
		return "bool"
	case schema.FloatType:
		return "float64"
	case schema.TextType, schema.UUIDType:
		return "string"
	case schema.BytesType, schema.JSONType:
		return "[]byte"
	case schema.TimeType:
		return "time.Time"
	case schema.DecimalType:
		return "string"
	default:
		return "any"
	}
}

// writeTable emits table as a schema.MustTableDef call built from the option
// constructors: a column constructor per column, PrimaryKey, Unique or
// UniqueNamed, Check or CheckNamed, Index or UniqueIndex, ForeignKey or
// ForeignKeyOn with RelationshipNamed for a matching relationship, and
// InSchema when table names a schema.
// writeTableDefLiteral writes a schema.TableDef composite literal describing
// table, stating every field the option form folds. It emits a field only
// when it is non-zero, so a simple table stays short; format.Source handles
// alignment. table.Relationships is written verbatim rather than derived, so
// a table that matched two relationships to one foreign key keeps both,
// where writeForeignKeyOptions's RelationshipNamed option kept only the
// first.
func writeTableDefLiteral(source *bytes.Buffer, table schema.TableDef) {
	source.WriteString("schema.TableDef{\n")
	if table.Schema != "" {
		source.WriteString("Schema: ")
		source.WriteString(quote(table.Schema))
		source.WriteString(",\n")
	}
	source.WriteString("Name: ")
	source.WriteString(quote(table.Name))
	source.WriteString(",\n")
	if table.RowName != "" {
		source.WriteString("RowName: ")
		source.WriteString(quote(table.RowName))
		source.WriteString(",\n")
	}
	if len(table.Columns) > 0 {
		source.WriteString("Columns: []schema.ColumnDef{\n")
		for _, column := range table.Columns {
			writeColumnDefLiteral(source, column)
		}
		source.WriteString("},\n")
	}
	if len(table.PrimaryKey) > 0 {
		source.WriteString("PrimaryKey: ")
		writeStringLiteralSlice(source, table.PrimaryKey)
		source.WriteString(",\n")
	}
	if table.Strict {
		source.WriteString("Strict: true,\n")
	}
	if table.WithoutRowID {
		source.WriteString("WithoutRowID: true,\n")
	}
	if table.PrimaryKeyAutoincrement {
		source.WriteString("PrimaryKeyAutoincrement: true,\n")
	}
	if table.PrimaryKeyOnConflict != "" {
		source.WriteString("PrimaryKeyOnConflict: ")
		source.WriteString(conflictResolutionConstant(table.PrimaryKeyOnConflict))
		source.WriteString(",\n")
	}
	if len(table.UniqueConstraints) > 0 {
		source.WriteString("UniqueConstraints: []schema.UniqueDef{\n")
		for _, constraint := range table.UniqueConstraints {
			writeUniqueDefLiteral(source, constraint)
		}
		source.WriteString("},\n")
	}
	if len(table.Checks) > 0 {
		source.WriteString("Checks: []schema.CheckDef{\n")
		for _, check := range table.Checks {
			source.WriteString("{")
			if check.Name != "" {
				source.WriteString("Name: ")
				source.WriteString(quote(check.Name))
				source.WriteString(", ")
			}
			source.WriteString("Expression: ")
			source.WriteString(quote(check.Expression))
			if check.NoInherit {
				source.WriteString(", NoInherit: true")
			}
			if check.NotValid {
				source.WriteString(", NotValid: true")
			}
			if check.NotEnforced {
				source.WriteString(", NotEnforced: true")
			}
			source.WriteString("},\n")
		}
		source.WriteString("},\n")
	}
	if len(table.Indexes) > 0 {
		source.WriteString("Indexes: []schema.IndexDef{\n")
		for _, index := range table.Indexes {
			source.WriteString("{Name: ")
			source.WriteString(quote(index.Name))
			if len(index.Columns) > 0 {
				source.WriteString(", Columns: ")
				writeStringLiteralSlice(source, index.Columns)
			}
			if index.Unique {
				source.WriteString(", Unique: true")
			}
			if index.Method != "" {
				source.WriteString(", Method: schema.IndexMethod(")
				source.WriteString(quote(string(index.Method)))
				source.WriteString(")")
			}
			if len(index.Expressions) > 0 {
				source.WriteString(", Expressions: ")
				writeStringLiteralSlice(source, index.Expressions)
			}
			if index.Predicate != "" {
				source.WriteString(", Predicate: ")
				source.WriteString(quote(index.Predicate))
			}
			source.WriteString("},\n")
		}
		source.WriteString("},\n")
	}
	if len(table.ForeignKeys) > 0 {
		source.WriteString("ForeignKeys: []schema.ForeignKeyDef{\n")
		for _, key := range table.ForeignKeys {
			writeForeignKeyDefLiteral(source, key)
		}
		source.WriteString("},\n")
	}
	if len(table.Relationships) > 0 {
		source.WriteString("Relationships: []schema.RelationshipDef{\n")
		for _, relationship := range table.Relationships {
			writeRelationshipDefLiteral(source, relationship)
		}
		source.WriteString("},\n")
	}
	source.WriteString("}")
}

func writeUniqueDefLiteral(source *bytes.Buffer, constraint schema.UniqueDef) {
	source.WriteString("{")
	if constraint.Name != "" {
		source.WriteString("Name: ")
		source.WriteString(quote(constraint.Name))
		source.WriteString(", ")
	}
	source.WriteString("Columns: ")
	writeStringLiteralSlice(source, constraint.Columns)
	if constraint.Deferrable != "" {
		source.WriteString(", Deferrable: ")
		source.WriteString(deferrabilityConstant(constraint.Deferrable))
	}
	if constraint.NullsNotDistinct {
		source.WriteString(", NullsNotDistinct: true")
	}
	if len(constraint.IncludeColumns) > 0 {
		source.WriteString(", IncludeColumns: ")
		writeStringLiteralSlice(source, constraint.IncludeColumns)
	}
	if constraint.OnConflict != "" {
		source.WriteString(", OnConflict: ")
		source.WriteString(conflictResolutionConstant(constraint.OnConflict))
	}
	source.WriteString("},\n")
}

func writeColumnDefLiteral(source *bytes.Buffer, column schema.ColumnDef) {
	source.WriteString("{Name: ")
	source.WriteString(quote(column.Name))
	source.WriteString(", Type: ")
	writeColumnTypeLiteral(source, column.Type)
	if column.Nullable {
		source.WriteString(", Nullable: true")
	}
	if column.Default != "" {
		source.WriteString(", Default: ")
		source.WriteString(quote(column.Default))
	}
	source.WriteString("},\n")
}

// writeColumnTypeLiteral writes the schema.ColumnType literal for columnType.
// TextType and DecimalType stay calls into schema.NewTextWidth and
// schema.NewDecimalScale for their optional field, because TextWidth and
// DecimalScale each carry an unexported set bool that tells a stated zero
// apart from an unstated one; every other type is a plain literal.
func writeColumnTypeLiteral(source *bytes.Buffer, columnType schema.ColumnType) {
	switch typed := columnType.(type) {
	case schema.IntegerType:
		if typed.Unsigned {
			source.WriteString("schema.IntegerType{Unsigned: true}")
			return
		}
		source.WriteString("schema.IntegerType{}")
	case schema.TextType:
		width, stated := typed.Width.Value()
		if !stated && !typed.Fixed {
			source.WriteString("schema.TextType{}")
			return
		}
		source.WriteString("schema.TextType{")
		if stated {
			source.WriteString("Width: schema.NewTextWidth(")
			source.WriteString(strconv.Itoa(width))
			source.WriteString(")")
		}
		if typed.Fixed {
			if stated {
				source.WriteString(", ")
			}
			source.WriteString("Fixed: true")
		}
		source.WriteString("}")
	case schema.DecimalType:
		source.WriteString("schema.DecimalType{Precision: ")
		source.WriteString(strconv.Itoa(typed.Precision))
		if scale, stated := typed.Scale.Value(); stated {
			source.WriteString(", Scale: schema.NewDecimalScale(")
			source.WriteString(strconv.Itoa(scale))
			source.WriteString(")")
		}
		source.WriteString("}")
	case schema.BooleanType:
		source.WriteString("schema.BooleanType{}")
	case schema.FloatType:
		source.WriteString("schema.FloatType{}")
	case schema.BytesType:
		source.WriteString("schema.BytesType{}")
	case schema.TimeType:
		source.WriteString("schema.TimeType{}")
	case schema.JSONType:
		source.WriteString("schema.JSONType{}")
	case schema.UUIDType:
		source.WriteString("schema.UUIDType{}")
	}
}

func writeForeignKeyDefLiteral(source *bytes.Buffer, key schema.ForeignKeyDef) {
	source.WriteString("{")
	if key.Name != "" {
		source.WriteString("Name: ")
		source.WriteString(quote(key.Name))
		source.WriteString(", ")
	}
	source.WriteString("Columns: ")
	writeStringLiteralSlice(source, key.Columns)
	if key.ReferencedSchema != "" {
		source.WriteString(", ReferencedSchema: ")
		source.WriteString(quote(key.ReferencedSchema))
	}
	source.WriteString(", ReferencedTable: ")
	source.WriteString(quote(key.ReferencedTable))
	source.WriteString(", ReferencedColumns: ")
	writeStringLiteralSlice(source, key.ReferencedColumns)
	if key.Match != "" {
		source.WriteString(", Match: ")
		source.WriteString(matchTypeConstant(key.Match))
	}
	if key.OnDelete != "" {
		source.WriteString(", OnDelete: ")
		source.WriteString(referenceActionConstant(key.OnDelete))
	}
	if key.OnUpdate != "" {
		source.WriteString(", OnUpdate: ")
		source.WriteString(referenceActionConstant(key.OnUpdate))
	}
	if key.Deferrable != "" {
		source.WriteString(", Deferrable: ")
		source.WriteString(deferrabilityConstant(key.Deferrable))
	}
	if key.NotValid {
		source.WriteString(", NotValid: true")
	}
	if key.NotEnforced {
		source.WriteString(", NotEnforced: true")
	}
	source.WriteString("},\n")
}

func writeRelationshipDefLiteral(source *bytes.Buffer, relationship schema.RelationshipDef) {
	source.WriteString("{Name: ")
	source.WriteString(quote(relationship.Name))
	source.WriteString(", Kind: ")
	source.WriteString(relationshipKindConstant(relationship.Kind))
	source.WriteString(", Columns: ")
	writeStringLiteralSlice(source, relationship.Columns)
	if relationship.ReferencedSchema != "" {
		source.WriteString(", ReferencedSchema: ")
		source.WriteString(quote(relationship.ReferencedSchema))
	}
	source.WriteString(", ReferencedTable: ")
	source.WriteString(quote(relationship.ReferencedTable))
	source.WriteString(", ReferencedColumns: ")
	writeStringLiteralSlice(source, relationship.ReferencedColumns)
	source.WriteString("},\n")
}

func relationshipKindConstant(kind schema.RelationshipKind) string {
	switch kind {
	case schema.RelationshipBelongsTo:
		return "schema.RelationshipBelongsTo"
	case schema.RelationshipHasMany:
		return "schema.RelationshipHasMany"
	default:
		return "schema.RelationshipKind(" + quote(string(kind)) + ")"
	}
}

// writeStringLiteralSlice writes values as a []string composite literal.
func writeStringLiteralSlice(source *bytes.Buffer, values []string) {
	source.WriteString("[]string{")
	for index, value := range values {
		if index > 0 {
			source.WriteString(", ")
		}
		source.WriteString(quote(value))
	}
	source.WriteByte('}')
}

func referenceActionConstant(action schema.ReferenceAction) string {
	switch action {
	case schema.NoAction:
		return "schema.NoAction"
	case schema.Restrict:
		return "schema.Restrict"
	case schema.Cascade:
		return "schema.Cascade"
	case schema.SetNull:
		return "schema.SetNull"
	case schema.SetDefault:
		return "schema.SetDefault"
	default:
		return "schema.ReferenceAction(" + quote(string(action)) + ")"
	}
}

func matchTypeConstant(match schema.MatchType) string {
	switch match {
	case schema.MatchFull:
		return "schema.MatchFull"
	case schema.MatchPartial:
		return "schema.MatchPartial"
	default:
		return "schema.MatchType(" + quote(string(match)) + ")"
	}
}

func deferrabilityConstant(deferrable schema.Deferrability) string {
	switch deferrable {
	case schema.DeferrableInitiallyImmediate:
		return "schema.DeferrableInitiallyImmediate"
	case schema.DeferrableInitiallyDeferred:
		return "schema.DeferrableInitiallyDeferred"
	default:
		return "schema.Deferrability(" + quote(string(deferrable)) + ")"
	}
}

func conflictResolutionConstant(conflict schema.ConflictResolution) string {
	switch conflict {
	case schema.ConflictRollback:
		return "schema.ConflictRollback"
	case schema.ConflictFail:
		return "schema.ConflictFail"
	case schema.ConflictIgnore:
		return "schema.ConflictIgnore"
	case schema.ConflictReplace:
		return "schema.ConflictReplace"
	default:
		return "schema.ConflictResolution(" + quote(string(conflict)) + ")"
	}
}

func quote(value string) string {
	return strconv.Quote(value)
}
