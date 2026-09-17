// Package schemagen holds the pieces of Go-source generation the compact
// emitter (compact.go) shares with the rest of the package: resolving a
// package's final names (names.go), resolving a column's Go binding
// (binding.go), and rendering a schema.TableDef as the Go literal
// compact.go's schema_gen.go embeds (TableDefinitionLiteral, in this file).
// This file also holds relationshipSpecs, which computes the relationship
// methods and types a package's final names must reserve, shared between
// ResolveNames and the compact emitter's own naming.
package schemagen

import (
	"bytes"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
)

// variableName returns the exported Go identifier for a table name. It
// delegates to goName so tables use the same initialism rules as columns:
// a table named api_keys and a column named api_key both spell "API" the
// same way, instead of the table getting a plain "Api".
func variableName(name string) string {
	return goName(name)
}

// DescriptorVarName returns the unexported package-level variable a
// generated table's rasql.Table wrapper is built from:
// descriptorName's exported form, kept for a caller outside this package
// that needs to name that variable, such as an error message pointing at a
// specific generated declaration.
func DescriptorVarName(tableName string) string {
	return descriptorName(tableName)
}

// descriptorName returns the unexported variable name backing an accessor.
// Distinctness across accessors is enforced by ResolvedNames.validateCollisions
// (names.go), which includes descriptor names in its collision set, not by
// any property of this lowering.
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

// ColumnGoType returns the Go type of a value held by column. It takes the
// whole column because signedness changes the answer: an unsigned integer
// column reaches 18446744073709551615, which int64 cannot hold, so it maps to
// uint64. Every other integer column maps to int64.
//
// It is the one mapping from a schema column type to a Go type: a generated
// row struct's field and a generated query function's parameter both take
// their type from here, so a column whose type changes changes both on the
// next run.
func ColumnGoType(column schema.ColumnDef) string {
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

// writeTableDefLiteral writes a schema.TableDef composite literal describing
// table, stating every field the option form folds. It emits a field only
// when it is non-zero, so a simple table stays short; format.Source handles
// alignment.
func writeTableDefLiteral(source *bytes.Buffer, table schema.TableDef) {
	source.WriteString("schema.TableDef{\n")
	if table.Schema != "" {
		source.WriteString("Schema: ")
		source.WriteString(quote(table.Schema))
		source.WriteString(",\n")
	}
	if table.Kind != "" {
		source.WriteString("Kind: schema.ObjectKind(")
		source.WriteString(quote(string(table.Kind)))
		source.WriteString("),\n")
	}
	if table.Operations != 0 {
		source.WriteString("Operations: schema.Operation(")
		source.WriteString(strconv.Itoa(int(table.Operations)))
		source.WriteString("),\n")
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
	if table.VirtualTableModule != "" {
		source.WriteString("VirtualTableModule: ")
		source.WriteString(quote(table.VirtualTableModule))
		source.WriteString(",\n")
	}
	if len(table.VirtualTableModuleArguments) > 0 {
		source.WriteString("VirtualTableModuleArguments: ")
		writeStringLiteralSlice(source, table.VirtualTableModuleArguments)
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
	if len(table.ExclusionConstraints) > 0 {
		source.WriteString("ExclusionConstraints: []schema.ExclusionDef{\n")
		for _, exclusion := range table.ExclusionConstraints {
			writeExclusionDefLiteral(source, exclusion)
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
				writeSQLTextLiteralSlice(source, index.Expressions)
			}
			if index.Predicate != "" {
				source.WriteString(", Predicate: ")
				source.WriteString(quote(index.Predicate))
			}
			if len(index.IncludeColumns) > 0 {
				source.WriteString(", IncludeColumns: ")
				writeStringLiteralSlice(source, index.IncludeColumns)
			}
			if index.Invisible {
				source.WriteString(", Invisible: true")
			}
			if index.NotValid {
				source.WriteString(", NotValid: true")
			}
			if len(index.StorageParameters) > 0 {
				source.WriteString(", StorageParameters: ")
				writeStringMapLiteral(source, index.StorageParameters)
			}
			if index.Tablespace != "" {
				source.WriteString(", Tablespace: ")
				source.WriteString(quote(index.Tablespace))
			}
			if index.ReplicaIdentity {
				source.WriteString(", ReplicaIdentity: true")
			}
			if index.NullsNotDistinct {
				source.WriteString(", NullsNotDistinct: true")
			}
			if len(index.Keys) > 0 {
				source.WriteString(", Keys: []schema.IndexKeyDef{\n")
				for _, key := range index.Keys {
					writeIndexKeyDefLiteral(source, key)
				}
				source.WriteString("}")
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
	source.WriteString("}")
}

func writeUniqueDefLiteral(source *bytes.Buffer, constraint schema.UniqueDef) {
	source.WriteString("{")
	first := true
	if constraint.Name != "" {
		source.WriteString("Name: ")
		source.WriteString(quote(constraint.Name))
		first = false
	}
	if len(constraint.Columns) > 0 {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("Columns: ")
		writeStringLiteralSlice(source, constraint.Columns)
		first = false
	}
	if constraint.Deferrable != "" {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("Deferrable: ")
		source.WriteString(deferrabilityConstant(constraint.Deferrable))
		first = false
	}
	if constraint.NullsNotDistinct {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("NullsNotDistinct: true")
		first = false
	}
	if len(constraint.IncludeColumns) > 0 {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("IncludeColumns: ")
		writeStringLiteralSlice(source, constraint.IncludeColumns)
		first = false
	}
	if constraint.OnConflict != "" {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("OnConflict: ")
		source.WriteString(conflictResolutionConstant(constraint.OnConflict))
		first = false
	}
	if len(constraint.Keys) > 0 {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("Keys: []schema.IndexKeyDef{\n")
		for _, key := range constraint.Keys {
			writeIndexKeyDefLiteral(source, key)
		}
		source.WriteString("}")
		first = false
	}
	if constraint.Temporal {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("Temporal: true")
		first = false
	}
	if len(constraint.StorageParameters) > 0 {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("StorageParameters: ")
		writeStringMapLiteral(source, constraint.StorageParameters)
		first = false
	}
	if constraint.Tablespace != "" {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("Tablespace: ")
		source.WriteString(quote(constraint.Tablespace))
		first = false
	}
	if constraint.ReplicaIdentity {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("ReplicaIdentity: true")
		first = false
	}
	if len(constraint.Collations) > 0 {
		if !first {
			source.WriteString(", ")
		}
		source.WriteString("Collations: ")
		writeStringMapLiteral(source, constraint.Collations)
	}
	source.WriteString("},\n")
}

// writeIndexKeyDefLiteral writes one schema.IndexKeyDef composite literal,
// shared by IndexDef.Keys and UniqueDef.Keys since both reuse the same
// type.
func writeIndexKeyDefLiteral(source *bytes.Buffer, key schema.IndexKeyDef) {
	source.WriteString("{Expression: ")
	source.WriteString(quote(key.Expression))
	if key.Descending {
		source.WriteString(", Descending: true")
	}
	if key.Collation != "" {
		source.WriteString(", Collation: ")
		source.WriteString(quote(key.Collation))
	}
	if key.OperatorClass != "" {
		source.WriteString(", OperatorClass: ")
		source.WriteString(quote(key.OperatorClass))
	}
	if key.PrefixLength != 0 {
		source.WriteString(", PrefixLength: ")
		source.WriteString(strconv.Itoa(key.PrefixLength))
	}
	if key.NullsOrder != "" {
		source.WriteString(", NullsOrder: ")
		source.WriteString(nullsOrderConstant(key.NullsOrder))
	}
	source.WriteString("},\n")
}

func writeExclusionDefLiteral(source *bytes.Buffer, exclusion schema.ExclusionDef) {
	source.WriteString("{")
	if exclusion.Name != "" {
		source.WriteString("Name: ")
		source.WriteString(quote(exclusion.Name))
		source.WriteString(", ")
	}
	if exclusion.Method != "" {
		source.WriteString("Method: schema.IndexMethod(")
		source.WriteString(quote(string(exclusion.Method)))
		source.WriteString("), ")
	}
	source.WriteString("Elements: []schema.ExclusionElementDef{\n")
	for _, element := range exclusion.Elements {
		source.WriteString("{Expression: ")
		source.WriteString(quote(element.Expression))
		source.WriteString(", Operator: ")
		source.WriteString(quote(element.Operator))
		source.WriteString("},\n")
	}
	source.WriteString("}")
	if exclusion.Predicate != "" {
		source.WriteString(", Predicate: ")
		source.WriteString(quote(exclusion.Predicate))
	}
	if exclusion.Deferrable != "" {
		source.WriteString(", Deferrable: ")
		source.WriteString(deferrabilityConstant(exclusion.Deferrable))
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
	if column.GoBinding != nil {
		source.WriteString(", GoBinding: &schema.GoBinding{Type: ")
		source.WriteString(quote(column.GoBinding.Type))
		if column.GoBinding.NullableType != "" {
			source.WriteString(", NullableType: ")
			source.WriteString(quote(column.GoBinding.NullableType))
		}
		if len(column.GoBinding.Imports) > 0 {
			source.WriteString(", Imports: []schema.GoImport{")
			for _, imported := range column.GoBinding.Imports {
				source.WriteString("{Path: ")
				source.WriteString(quote(imported.Path))
				if imported.Name != "" {
					source.WriteString(", Name: ")
					source.WriteString(quote(imported.Name))
				}
				source.WriteString("},")
			}
			source.WriteString("}")
		}
		source.WriteString("}")
	}
	if column.Collation != "" {
		source.WriteString(", Collation: ")
		source.WriteString(quote(column.Collation))
	}
	if column.GeneratedExpression != "" {
		source.WriteString(", GeneratedExpression: ")
		source.WriteString(quote(column.GeneratedExpression))
	}
	if column.GeneratedStorage != "" {
		source.WriteString(", GeneratedStorage: ")
		source.WriteString(generatedStorageConstant(column.GeneratedStorage))
	}
	if column.Identity != "" {
		source.WriteString(", Identity: ")
		source.WriteString(identityGenerationConstant(column.Identity))
	}
	if column.Hidden {
		source.WriteString(", Hidden: true")
	}
	if column.NativeType != nil {
		source.WriteString(", NativeType: ")
		writeNativeTypeLiteral(source, column.NativeType)
	}
	source.WriteString("},\n")
}

func writeNativeTypeLiteral(source *bytes.Buffer, native *schema.NativeTypeDef) {
	source.WriteString("&schema.NativeTypeDef{Dialect: ")
	source.WriteString(quote(native.Dialect))
	if native.Schema != "" {
		source.WriteString(", Schema: ")
		source.WriteString(quote(native.Schema))
	}
	source.WriteString(", Name: ")
	source.WriteString(quote(native.Name))
	source.WriteString(", Kind: schema.NativeTypeKind(")
	source.WriteString(quote(string(native.Kind)))
	source.WriteString(")")
	if native.Arguments != nil {
		source.WriteString(", Arguments: []string{")
		for index, argument := range native.Arguments {
			if index > 0 {
				source.WriteString(", ")
			}
			source.WriteString(quote(argument))
		}
		source.WriteString("}")
	}
	if native.Element != nil {
		source.WriteString(", Element: ")
		writeNativeTypeLiteral(source, native.Element)
	}
	source.WriteString("}")
}

func generatedStorageConstant(storage schema.GeneratedStorage) string {
	switch storage {
	case schema.GeneratedStored:
		return "schema.GeneratedStored"
	case schema.GeneratedVirtual:
		return "schema.GeneratedVirtual"
	default:
		return "schema.GeneratedStorage(" + quote(string(storage)) + ")"
	}
}

func identityGenerationConstant(generation schema.IdentityGeneration) string {
	switch generation {
	case schema.IdentityAlways:
		return "schema.IdentityAlways"
	case schema.IdentityByDefault:
		return "schema.IdentityByDefault"
	default:
		return "schema.IdentityGeneration(" + quote(string(generation)) + ")"
	}
}

// writeColumnTypeLiteral writes the schema.ColumnType literal for columnType.
// TextType and DecimalType stay calls into schema.NewTextWidth and
// schema.NewDecimalScale for their optional field, because TextWidth and
// DecimalScale each carry an unexported set bool that tells a stated zero
// apart from an unstated one; every other type is a plain literal.
func writeColumnTypeLiteral(source *bytes.Buffer, columnType schema.ColumnType) {
	switch typed := columnType.(type) {
	case schema.OpaqueType:
		source.WriteString("schema.OpaqueType{}")
	case schema.IntegerType:
		width, stated := typed.DisplayWidth.Value()
		if !typed.Unsigned && !stated && !typed.ZeroFill {
			source.WriteString("schema.IntegerType{}")
			return
		}
		source.WriteString("schema.IntegerType{")
		fields := 0
		if typed.Unsigned {
			source.WriteString("Unsigned: true")
			fields++
		}
		if stated {
			if fields > 0 {
				source.WriteString(", ")
			}
			source.WriteString("DisplayWidth: schema.NewIntegerDisplayWidth(")
			source.WriteString(strconv.Itoa(width))
			source.WriteString(")")
			fields++
		}
		if typed.ZeroFill {
			if fields > 0 {
				source.WriteString(", ")
			}
			source.WriteString("ZeroFill: true")
		}
		source.WriteString("}")
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
		if typed.Unsigned {
			source.WriteString(", Unsigned: true")
		}
		if typed.ZeroFill {
			source.WriteString(", ZeroFill: true")
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

// TableDefinitionLiteral returns the canonical Go expression for a validated
// table descriptor. Compact generation uses the same descriptor spelling as
// the legacy renderer while keeping the generated API declarations separate.
func TableDefinitionLiteral(table schema.TableDef) (string, error) {
	if err := table.Validate(); err != nil {
		return "", err
	}
	var source bytes.Buffer
	writeTableDefLiteral(&source, table)
	return source.String(), nil
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
	if key.Temporal {
		source.WriteString(", Temporal: true")
	}
	if len(key.DeleteSetColumns) > 0 {
		source.WriteString(", DeleteSetColumns: ")
		writeStringLiteralSlice(source, key.DeleteSetColumns)
	}
	source.WriteString("},\n")
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

// writeSQLTextLiteralSlice writes values as a []sqltext.Text composite
// literal. It exists separately from writeStringLiteralSlice, rather than as
// a shared generic, because the emitted slice type name itself changes with
// the element type and a struct field's value cannot elide it the way a
// slice element can.
func writeSQLTextLiteralSlice(source *bytes.Buffer, values []sqltext.Text) {
	source.WriteString("[]sqltext.Text{")
	for index, value := range values {
		if index > 0 {
			source.WriteString(", ")
		}
		source.WriteString(quote(value))
	}
	source.WriteByte('}')
}

// writeStringMapLiteral writes values as a map[string]string composite
// literal, with keys sorted so repeated generation of the same descriptor
// produces byte-identical source.
func writeStringMapLiteral(source *bytes.Buffer, values map[string]string) {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	source.WriteString("map[string]string{")
	for index, key := range keys {
		if index > 0 {
			source.WriteString(", ")
		}
		source.WriteString(quote(key))
		source.WriteString(": ")
		source.WriteString(quote(values[key]))
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

func nullsOrderConstant(order schema.NullsOrder) string {
	switch order {
	case schema.NullsFirst:
		return "schema.NullsFirst"
	case schema.NullsLast:
		return "schema.NullsLast"
	default:
		return "schema.NullsOrder(" + quote(string(order)) + ")"
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

// quote renders value as a double-quoted Go string literal. The ~string
// constraint lets it take schema's branded sqltext.Text fields directly,
// alongside plain string and every other named string type this file
// quotes, without a conversion at each call site.
func quote[T ~string](value T) string {
	return strconv.Quote(string(value))
}
