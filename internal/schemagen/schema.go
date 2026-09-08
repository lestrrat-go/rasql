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
	"fmt"
	"go/token"
	"slices"
	"sort"
	"strconv"
	"strings"
	"unicode"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
)

// reservedFieldNames holds the identifiers a generated table type already uses.
// A column whose generated method name lands on one of them is rejected,
// because the method would shadow the embedded rasql.Table or its own methods,
// or collide with a mapping method declared on the row type.
//
// DecodeRow is deliberately absent, and the list is exactly these eight.
// Nothing emits a DecodeRow method any more, so a decode_row column collides
// with nothing: a table with such a column renders without error, with an
// ordinary DecodeRow field. Re-adding it here would reject a legitimate column
// name for a method that does not exist.
//
// reservedRelationshipMethod is this map's only reader now: a relationship
// whose derived method name lands on one of these is rejected the same way a
// column's would be.
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

// isUsablePackageName reports whether name can head a "package" clause that
// compiles. token.IsIdentifier already refuses a Go keyword, an empty
// string, and anything that is not a valid identifier at all (a name
// starting with a digit, or containing a character such as "-"), so none
// of those need a separate check here. The blank identifier "_" is the one
// name token.IsIdentifier accepts that still cannot be used: "_" is an
// ordinary identifier token everywhere else in Go, but the language spec
// requires a PackageName to not be the blank identifier, so "package _" is
// not a package clause the compiler accepts. This is the same check
// generate.Store.Plan makes on the same name, and the same one
// isUsableGoIdentifier in package template makes on a generated function's
// package name; every place in this package that validates a package name
// calls this one function so the rule cannot drift between them.
func isUsablePackageName(name string) bool {
	return name != "_" && token.IsIdentifier(name)
}

// packageNameError reports name as an unusable package name, explaining why:
// either it is the blank identifier, which the language reserves and never
// allows as a package name, or it is not a Go identifier at all.
func packageNameError(name string) error {
	if name == "_" {
		return fmt.Errorf("generate: invalid package name %q: the blank identifier cannot name a package", name)
	}
	return fmt.Errorf("generate: invalid package name %q: must be a valid Go identifier", name)
}

type SourceOptions struct {
	Dir   string
	Names *ResolvedNames
}

// descriptorTestFuncName was the fixed name the deleted legacy renderer gave
// the internal test it wrote into every generated package's schema_gen_test.go.
// The compact emitter's own schema_gen_test.go declares no such test -- it is
// an empty marker file -- so nothing generated today can collide with this
// name. It stays reserved anyway, in reservedPackageNames below, as a
// defensive holdover: a config still using NameOverrides.Objects for a table
// literally spelled by this constant would otherwise be free to collide with
// it, and there is no benefit to allowing that.
const descriptorTestFuncName = "TestRasqlgenGeneratedDefinitionsAreValid"

// reservedPackageNames holds package-level identifiers the deleted legacy
// renderer emitted with a fixed spelling rather than deriving one from a
// table, plus "Tables", the fixed accessor it declared once per package. The
// compact emitter declares neither today, so nothing it generates can
// collide with either name on its own. ResolvedNames.collectPackageNames
// still seeds every resolved package's name set with them, reserving both
// defensively rather than assuming the gap stays empty forever.
var reservedPackageNames = map[string]struct{}{
	descriptorTestFuncName: {},
	"Tables":               {},
}

func relationshipTargetSchema(relationship schema.RelationshipDef) string {
	if relationship.ResolvedReferencedSchema != "" {
		return relationship.ResolvedReferencedSchema
	}
	return relationship.ReferencedSchema
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

func relationshipColumnsSupported(child, parent schema.TableDef, relationship schema.RelationshipDef, bindings *generatedBindings) ([]schema.ColumnDef, []schema.ColumnDef, string, BindingRef, bool) {
	if (relationship.Kind != schema.RelationshipBelongsTo && relationship.Kind != schema.RelationshipHasOne && relationship.Kind != schema.RelationshipHasMany) || len(relationship.Columns) == 0 || len(relationship.Columns) != len(relationship.ReferencedColumns) {
		return nil, nil, "", BindingRef{}, false
	}
	children := make([]schema.ColumnDef, len(relationship.Columns))
	parents := make([]schema.ColumnDef, len(relationship.Columns))
	for index, name := range relationship.Columns {
		childColumn, ok := child.Column(name)
		if !ok {
			return nil, nil, "", BindingRef{}, false
		}
		parentColumn, ok := parent.Column(relationship.ReferencedColumns[index])
		if !ok {
			return nil, nil, "", BindingRef{}, false
		}
		if relationship.Kind == schema.RelationshipBelongsTo && parentColumn.Nullable {
			return nil, nil, "", BindingRef{}, false
		}
		if relationship.Kind != schema.RelationshipBelongsTo && childColumn.Nullable {
			return nil, nil, "", BindingRef{}, false
		}
		if bindings != nil {
			parentRef, parentOK := bindings.ref(parent, parentColumn)
			childRef, childOK := bindings.ref(child, childColumn)
			if !parentOK || !childOK || !SameBindingType(parentRef, childRef, false) {
				return nil, nil, "", BindingRef{}, false
			}
		} else if ColumnGoType(childColumn) != ColumnGoType(parentColumn) {
			return nil, nil, "", BindingRef{}, false
		}
		children[index], parents[index] = childColumn, parentColumn
	}
	if relationship.Kind == schema.RelationshipHasMany {
		if len(children) == 1 {
			keyRef, ok := relationshipBindingRef(parent, parents[0], child, children[0], bindings)
			if !ok {
				return nil, nil, "", BindingRef{}, false
			}
			return children, parents, keyRef.resolved.For(false), keyRef, true
		}
		return children, parents, "", BindingRef{}, true
	}
	if len(parents) == 1 {
		if !columnsAreUnique(parent, relationship.ReferencedColumns) {
			return nil, nil, "", BindingRef{}, false
		}
		keyRef, ok := relationshipBindingRef(parent, parents[0], child, children[0], bindings)
		if !ok {
			return nil, nil, "", BindingRef{}, false
		}
		keyType := keyRef.resolved.For(false)
		if children[0].Nullable {
			keyType = "*" + keyType
		}
		return children, parents, keyType, keyRef, true
	}
	return children, parents, "", BindingRef{}, true
}

func relationshipBindingRef(parent schema.TableDef, parentColumn schema.ColumnDef, child schema.TableDef, childColumn schema.ColumnDef, bindings *generatedBindings) (BindingRef, bool) {
	if bindings != nil {
		parentRef, parentOK := bindings.ref(parent, parentColumn)
		childRef, childOK := bindings.ref(child, childColumn)
		return parentRef, parentOK && childOK && SameBindingType(parentRef, childRef, false)
	}
	keyType, compatible := sameColumnBindingType(parentColumn, childColumn)
	if !compatible {
		return BindingRef{}, false
	}
	resolved, err := ResolveGoBinding(parentColumn)
	if err != nil || keyType != resolved.For(false) {
		return BindingRef{}, false
	}
	return BindingRef{key: keyType, nullable: "*" + keyType, resolved: resolved}, true
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

// DescriptorVarName returns the unexported package-level variable a
// generated table's rasql.Table (or rasql.ReadTable) wrapper is built from:
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

type relationshipSpec struct {
	kind                   schema.RelationshipKind
	method                 string
	identity               string
	typeName               string
	parent                 schema.TableDef
	child                  schema.TableDef
	parentColumn           schema.ColumnDef
	childColumn            schema.ColumnDef
	parentField            string
	childField             string
	parentAccessor         string
	childAccessor          string
	parentKeyType          string
	parentType             string
	childType              string
	parentRow              string
	childRow               string
	parentKeyRef           BindingRef
	parentColumns          []schema.ColumnDef
	childColumns           []schema.ColumnDef
	parentFields           []string
	childFields            []string
	keyName                string
	keyFields              []string
	keyColumns             []schema.ColumnDef
	through                schema.TableDef
	throughSource          []schema.ColumnDef
	throughTarget          []schema.ColumnDef
	throughRow             string
	throughTableAccessor   string
	childTableAccessor     string
	throughSourceAccessors []string
	throughTargetAccessors []string
	childColumnAccessors   []string
	childAllAccessors      []string
	targetPrimaryKeyTypes  []string
	targetPrimaryKeyFields []string
	parentColumnTypes      []string
	childColumnTypes       []string
}

type inverseRelationshipCandidate struct {
	child         schema.TableDef
	relationship  schema.RelationshipDef
	parentColumn  schema.ColumnDef
	childColumn   schema.ColumnDef
	keyType       string
	parentColumns []schema.ColumnDef
	childColumns  []schema.ColumnDef
	kind          schema.RelationshipKind
	keyRef        BindingRef
}

func relationshipSpecs(table schema.TableDef, allTables []schema.TableDef, names *ResolvedNames, bindings *generatedBindings) []relationshipSpec {
	result := make([]relationshipSpec, 0)
	usedMethods := make(map[string]struct{})
	for _, column := range table.Columns {
		accessor := resolvedColumnName(names, table, column.Name).Accessor
		usedMethods[accessor] = struct{}{}
		usedMethods[accessor+"Ref"] = struct{}{}
	}
	for _, relationship := range table.Relationships {
		parent, ok := relationshipTable(allTables, relationshipTargetSchema(relationship), relationship.ReferencedTable)
		if !ok {
			continue
		}
		if relationship.Kind == schema.RelationshipManyToMany {
			through, sourceColumns, targetColumns, ok := manyToManyColumnsSupported(table, parent, relationship, allTables)
			if !ok {
				continue
			}
			method := goName(relationship.Name)
			if !token.IsIdentifier(method) || reservedRelationshipMethod(method) {
				continue
			}
			if _, exists := usedMethods[method]; exists {
				method = nextRelationshipMethod(method, usedMethods)
			}
			usedMethods[method] = struct{}{}
			keyType := ""
			var keyRef BindingRef
			if len(sourceColumns) == 1 {
				if bindings != nil {
					keyRef, ok = bindings.ref(table, sourceColumns[0])
					if !ok {
						continue
					}
				} else {
					resolved, err := ResolveGoBinding(sourceColumns[0])
					if err != nil {
						continue
					}
					keyRef = BindingRef{key: resolved.For(false), nullable: resolved.For(true), resolved: resolved}
				}
				keyType = keyRef.resolved.For(false)
			}
			parentObject := resolvedObjectName(names, table)
			childObject := resolvedObjectName(names, parent)
			throughObject := resolvedObjectName(names, through)
			parentColumn := resolvedColumnName(names, table, sourceColumns[0].Name)
			childColumn := resolvedColumnName(names, parent, targetColumns[0].Name)
			throughSource := relationshipThroughColumns(through, relationship.Through.SourceColumns)
			throughTarget := relationshipThroughColumns(through, relationship.Through.TargetColumns)
			result = append(result, relationshipSpec{kind: relationship.Kind, method: method, identity: relationshipIdentity(table, relationship), typeName: parentObject.TableType + method + "Relation", parent: table, child: parent, parentColumn: sourceColumns[0], childColumn: targetColumns[0], parentField: parentColumn.Field, childField: childColumn.Field, parentAccessor: parentColumn.Accessor, childAccessor: childColumn.Accessor, parentKeyType: keyType, parentColumns: sourceColumns, childColumns: targetColumns, parentFields: resolvedColumnFields(names, table, sourceColumns), childFields: resolvedColumnFields(names, parent, targetColumns), keyName: parentObject.TableType + method + "Key", keyFields: resolvedColumnFields(names, table, sourceColumns), keyColumns: sourceColumns, through: through, throughSource: throughSource, throughTarget: throughTarget, parentType: parentObject.TableType, childType: childObject.TableType, parentRow: parentObject.RowType, childRow: childObject.RowType, parentKeyRef: keyRef, throughRow: throughObject.RowType, throughTableAccessor: throughObject.Accessor, childTableAccessor: childObject.Accessor, throughSourceAccessors: resolvedColumnAccessors(names, through, throughSource), throughTargetAccessors: resolvedColumnAccessors(names, through, throughTarget), childColumnAccessors: resolvedColumnAccessors(names, parent, targetColumns), childAllAccessors: resolvedColumnAccessors(names, parent, parent.Columns)})
			continue
		}
		childColumns, parentColumns, keyType, keyRef, ok := relationshipColumnsSupported(table, parent, relationship, bindings)
		if !ok {
			continue
		}
		if relationship.Kind == schema.RelationshipHasMany || relationship.Kind == schema.RelationshipHasOne {
			method := goName(relationship.Name)
			if !token.IsIdentifier(method) || reservedRelationshipMethod(method) {
				continue
			}
			if _, exists := usedMethods[method]; exists {
				method = nextRelationshipMethod(method, usedMethods)
			}
			usedMethods[method] = struct{}{}
			parentObject := resolvedObjectName(names, table)
			childObject := resolvedObjectName(names, parent)
			parentColumnName := resolvedColumnName(names, table, childColumns[0].Name)
			childColumnName := resolvedColumnName(names, parent, parentColumns[0].Name)
			result = append(result, relationshipSpec{
				kind:           relationship.Kind,
				method:         method,
				identity:       relationshipIdentity(table, relationship),
				typeName:       parentObject.TableType + method + "Relation",
				parent:         table,
				child:          parent,
				parentColumn:   childColumns[0],
				childColumn:    parentColumns[0],
				parentField:    parentColumnName.Field,
				childField:     childColumnName.Field,
				parentAccessor: parentColumnName.Accessor,
				childAccessor:  childColumnName.Accessor,
				parentKeyType:  keyType,
				parentColumns:  childColumns,
				childColumns:   parentColumns,
				parentFields:   resolvedColumnFields(names, table, childColumns),
				childFields:    resolvedColumnFields(names, parent, parentColumns),
				keyName:        parentObject.TableType + method + "Key",
				keyFields:      resolvedColumnFields(names, table, childColumns),
				keyColumns:     childColumns,
				parentType:     parentObject.TableType, childType: childObject.TableType,
				parentRow: parentObject.RowType, childRow: childObject.RowType,
				parentKeyRef: keyRef,
			})
			continue
		}
		parentColumn, childColumn := parentColumns[0], childColumns[0]
		method := goName(relationship.Name)
		if !token.IsIdentifier(method) || reservedRelationshipMethod(method) {
			continue
		}
		if _, exists := usedMethods[method]; exists {
			method = nextRelationshipMethod(method, usedMethods)
		}
		usedMethods[method] = struct{}{}
		childType := tableTypeName(table.Name)
		parentType := tableTypeName(parent.Name)
		childRow := rowTypeName(table)
		parentRow := rowTypeName(parent)
		parentField, childField := goName(parentColumn.Name), goName(childColumn.Name)
		parentAccessor, childAccessor := parentField, childField
		if names != nil {
			if resolved, ok := names.Object(table); ok {
				childType = resolved.TableType
				childRow = resolved.RowType
			}
			if resolved, ok := names.Object(parent); ok {
				parentType = resolved.TableType
				parentRow = resolved.RowType
			}
			if resolved, ok := names.Column(parent, parentColumn.Name); ok {
				parentAccessor = resolved.Accessor
				parentField = resolved.Field
			}
			if resolved, ok := names.Column(table, childColumn.Name); ok {
				childAccessor = resolved.Accessor
				childField = resolved.Field
			}
		}
		result = append(result, relationshipSpec{
			kind:           relationship.Kind,
			method:         method,
			identity:       relationshipIdentity(table, relationship),
			typeName:       childType + method + "Relation",
			parent:         parent,
			child:          table,
			parentColumn:   parentColumn,
			childColumn:    childColumn,
			parentField:    parentField,
			childField:     childField,
			parentKeyType:  keyType,
			parentAccessor: parentAccessor, childAccessor: childAccessor,
			parentType: parentType, childType: childType, parentRow: parentRow, childRow: childRow,
			parentKeyRef:  keyRef,
			parentColumns: parentColumns, childColumns: childColumns,
			parentFields: resolvedColumnFields(names, parent, parentColumns), childFields: resolvedColumnFields(names, table, childColumns),
			keyName:   childType + method + "Key",
			keyFields: resolvedColumnFields(names, table, childColumns), keyColumns: childColumns,
		})
	}

	candidates := make([]inverseRelationshipCandidate, 0)
	for _, child := range allTables {
		for _, relationship := range child.Relationships {
			if relationship.Kind != schema.RelationshipBelongsTo {
				continue
			}
			if relationship.ReferencedTable != table.Name || relationshipTargetSchema(relationship) != table.Schema {
				continue
			}
			childColumns, parentColumns, keyType, keyRef, ok := relationshipColumnsSupported(child, table, relationship, bindings)
			if !ok {
				continue
			}
			// Inverse synthesis emits a has_many/has_one relationship on table.
			// Its map key follows the referenced parent key, even when the child
			// belongs_to field is nullable.
			if len(parentColumns) == 1 {
				keyType = keyRef.resolved.For(false)
			}
			kind := inverseKind(child, relationship)
			groupSize := inverseRelationshipGroupSize(child, table, bindings)
			method := inverseRelationshipMethodName(child, relationship, groupSize)
			if hasStructuralInverse(table, child, relationship, kind, method) {
				continue
			}
			parentColumn, childColumn := parentColumns[0], childColumns[0]
			candidates = append(candidates, inverseRelationshipCandidate{
				child:         child,
				relationship:  relationship,
				parentColumn:  parentColumn,
				childColumn:   childColumn,
				keyType:       keyType,
				parentColumns: parentColumns, childColumns: childColumns,
				kind:   kind,
				keyRef: keyRef,
			})
		}
	}
	sort.SliceStable(candidates, func(left, right int) bool {
		leftKey := inverseRelationshipSortKey(candidates[left])
		rightKey := inverseRelationshipSortKey(candidates[right])
		return leftKey < rightKey
	})
	groups := make(map[string]int)
	for _, candidate := range candidates {
		key := candidate.child.Schema + "\x00" + candidate.child.Name + "\x00" + table.Schema + "\x00" + table.Name
		groups[key]++
	}
	for _, candidate := range candidates {
		key := candidate.child.Schema + "\x00" + candidate.child.Name + "\x00" + table.Schema + "\x00" + table.Name
		method := inverseRelationshipMethodName(candidate.child, candidate.relationship, groups[key])
		parentType, childType := tableTypeName(table.Name), tableTypeName(candidate.child.Name)
		parentRow, childRow := rowTypeName(table), rowTypeName(candidate.child)
		parentField, childField := goName(candidate.parentColumn.Name), goName(candidate.childColumn.Name)
		parentAccessor, childAccessor := parentField, childField
		if names != nil {
			if resolved, ok := names.Object(table); ok {
				parentType, parentRow = resolved.TableType, resolved.RowType
			}
			if resolved, ok := names.Object(candidate.child); ok {
				childType, childRow = resolved.TableType, resolved.RowType
			}
			if resolved, ok := names.Column(table, candidate.parentColumn.Name); ok {
				parentAccessor = resolved.Accessor
				parentField = resolved.Field
			}
			if resolved, ok := names.Column(candidate.child, candidate.childColumn.Name); ok {
				childAccessor = resolved.Accessor
				childField = resolved.Field
			}
		}
		result = append(result, relationshipSpec{
			kind:           candidate.kind,
			method:         method,
			identity:       relationshipIdentity(candidate.child, candidate.relationship),
			typeName:       parentType + method + "Relation",
			parent:         table,
			child:          candidate.child,
			parentColumn:   candidate.parentColumn,
			childColumn:    candidate.childColumn,
			parentField:    parentField,
			childField:     childField,
			parentKeyType:  candidate.keyType,
			parentAccessor: parentAccessor, childAccessor: childAccessor,
			parentType: parentType, childType: childType, parentRow: parentRow, childRow: childRow,
			parentKeyRef:  candidate.keyRef,
			parentColumns: candidate.parentColumns, childColumns: candidate.childColumns,
			parentFields: resolvedColumnFields(names, table, candidate.parentColumns), childFields: resolvedColumnFields(names, candidate.child, candidate.childColumns),
			keyName:   parentType + method + "Key",
			keyFields: resolvedColumnFields(names, table, candidate.parentColumns), keyColumns: candidate.parentColumns,
		})
	}
	for index := range result {
		result[index].parentColumnTypes = relationshipColumnTypes(bindings, result[index].parent, result[index].parentColumns)
		result[index].childColumnTypes = relationshipColumnTypes(bindings, result[index].child, result[index].childColumns)
		target := result[index].child
		if result[index].kind == schema.RelationshipBelongsTo {
			target = result[index].parent
		}
		result[index].targetPrimaryKeyTypes = make([]string, len(target.PrimaryKey))
		result[index].targetPrimaryKeyFields = make([]string, len(target.PrimaryKey))
		for keyIndex, name := range target.PrimaryKey {
			column, _ := target.Column(name)
			if bindings != nil {
				result[index].targetPrimaryKeyTypes[keyIndex], _ = bindings.typeFor(target, column, false)
			} else {
				binding, _ := ResolveGoBinding(column)
				result[index].targetPrimaryKeyTypes[keyIndex] = binding.For(false)
			}
			resolved := resolvedColumnName(names, target, name)
			result[index].targetPrimaryKeyFields[keyIndex] = resolved.Field
		}
	}
	return result
}

func nextRelationshipMethod(base string, used map[string]struct{}) string {
	for suffix := 2; ; suffix++ {
		candidate := base + strconv.Itoa(suffix)
		if _, occupied := used[candidate]; !occupied && !reservedRelationshipMethod(candidate) {
			return candidate
		}
	}
}

func relationshipColumnTypes(bindings *generatedBindings, table schema.TableDef, columns []schema.ColumnDef) []string {
	types := make([]string, len(columns))
	for index, column := range columns {
		if bindings != nil {
			types[index], _ = bindings.typeFor(table, column, false)
		} else {
			binding, _ := ResolveGoBinding(column)
			types[index] = binding.For(false)
		}
	}
	return types
}

func relationshipThroughColumns(table schema.TableDef, names []string) []schema.ColumnDef {
	result := make([]schema.ColumnDef, len(names))
	for index, name := range names {
		result[index], _ = table.Column(name)
	}
	return result
}

func manyToManyColumnsSupported(source, target schema.TableDef, relationship schema.RelationshipDef, allTables []schema.TableDef) (schema.TableDef, []schema.ColumnDef, []schema.ColumnDef, bool) {
	if relationship.Through == nil || len(relationship.Columns) == 0 || len(relationship.Columns) != len(relationship.ReferencedColumns) || len(relationship.Columns) != len(relationship.Through.SourceColumns) || len(relationship.ReferencedColumns) != len(relationship.Through.TargetColumns) {
		return schema.TableDef{}, nil, nil, false
	}
	through, ok := relationshipTable(allTables, relationship.Through.Table.Schema, relationship.Through.Table.Name)
	if !ok {
		return schema.TableDef{}, nil, nil, false
	}
	sourceColumns, targetColumns := relationshipThroughColumns(source, relationship.Columns), relationshipThroughColumns(target, relationship.ReferencedColumns)
	for index, name := range relationship.Through.SourceColumns {
		throughColumn, found := through.Column(name)
		_, compatible := sameColumnBindingType(throughColumn, sourceColumns[index])
		if !found || !compatible {
			return schema.TableDef{}, nil, nil, false
		}
	}
	for index, name := range relationship.Through.TargetColumns {
		throughColumn, found := through.Column(name)
		_, compatible := sameColumnBindingType(throughColumn, targetColumns[index])
		if !found || !compatible {
			return schema.TableDef{}, nil, nil, false
		}
	}
	return through, sourceColumns, targetColumns, true
}

func inverseRelationshipSortKey(candidate inverseRelationshipCandidate) string {
	return relationshipIdentity(candidate.child, candidate.relationship)
}

func inverseKind(child schema.TableDef, relationship schema.RelationshipDef) schema.RelationshipKind {
	if columnsAreUnique(child, relationship.Columns) {
		return schema.RelationshipHasOne
	}
	return schema.RelationshipHasMany
}

// hasStructuralInverse reports whether the canonical schema already carries
// the inverse relation for a child-side foreign key. The semantic compiler
// supplies both sides; legacy descriptors supply only the child side, so the
// auto-derived path remains necessary when no matching relation is present.
// Matching the complete key shape keeps unrelated same-named relations as
// real collisions instead of hiding them behind this compatibility path.
func inverseRelationshipGroupSize(child, parent schema.TableDef, bindings *generatedBindings) int {
	count := 0
	for _, relationship := range child.Relationships {
		if relationship.Kind != schema.RelationshipBelongsTo {
			continue
		}
		if relationshipTargetSchema(relationship) != parent.Schema || relationship.ReferencedTable != parent.Name {
			continue
		}
		if _, _, _, _, ok := relationshipColumnsSupported(child, parent, relationship, bindings); ok {
			count++
		}
	}
	return count
}

func hasStructuralInverse(table, child schema.TableDef, relationship schema.RelationshipDef, kind schema.RelationshipKind, method string) bool {
	for _, inverse := range table.Relationships {
		if inverse.Kind != kind || goName(inverse.Name) != method || relationshipTargetSchema(inverse) != child.Schema || inverse.ReferencedTable != child.Name {
			continue
		}
		if stringSlicesEqual(inverse.Columns, relationship.ReferencedColumns) && stringSlicesEqual(inverse.ReferencedColumns, relationship.Columns) {
			return true
		}
	}
	return false
}

func columnsAreUnique(table schema.TableDef, columns []string) bool {
	if slices.Equal(table.PrimaryKey, columns) {
		return true
	}
	for _, unique := range table.UniqueConstraints {
		if slices.Equal(unique.Columns, columns) {
			return true
		}
	}
	return false
}

func relationshipIdentity(child schema.TableDef, relationship schema.RelationshipDef) string {
	return strings.Join([]string{
		child.Schema,
		child.Name,
		relationship.Name,
		strings.Join(relationship.Columns, "\x00"),
		relationshipTargetSchema(relationship),
		relationship.ReferencedTable,
		strings.Join(relationship.ReferencedColumns, "\x00"),
	}, "\x00")
}

// Inverse methods use the child table name when it is available. A collision
// or reserved name gets the relationship name as a stable prefix, followed by
// a numeric suffix only when that name is also occupied.
func inverseRelationshipMethodName(child schema.TableDef, relationship schema.RelationshipDef, groupSize int) string {
	if relationship.InverseName != "" {
		return goName(relationship.InverseName)
	}
	if groupSize == 1 {
		return variableName(child.Name)
	}
	return goName(relationship.Name) + variableName(child.Name)
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

func resolvedColumnFields(names *ResolvedNames, table schema.TableDef, columns []schema.ColumnDef) []string {
	fields := make([]string, len(columns))
	for index, column := range columns {
		resolved := resolvedColumnName(names, table, column.Name)
		fields[index] = resolved.Field
	}
	return fields
}

func resolvedColumnAccessors(names *ResolvedNames, table schema.TableDef, columns []schema.ColumnDef) []string {
	accessors := make([]string, len(columns))
	for index, column := range columns {
		resolved := resolvedColumnName(names, table, column.Name)
		accessors[index] = resolved.Accessor
	}
	return accessors
}

func resolvedObjectName(names *ResolvedNames, table schema.TableDef) ResolvedObjectNames {
	if names != nil {
		if resolved, ok := names.Object(table); ok {
			return resolved
		}
	}
	return ResolvedObjectNames{Accessor: variableName(table.Name), TableType: tableTypeName(table.Name), RowType: rowTypeName(table)}
}

func resolvedColumnName(names *ResolvedNames, table schema.TableDef, column string) ResolvedColumnNames {
	if names != nil {
		if resolved, ok := names.Column(table, column); ok {
			return resolved
		}
	}
	return ResolvedColumnNames{Field: goName(column), Accessor: goName(column)}
}

func reservedRelationshipMethod(name string) bool {
	_, reserved := reservedFieldNames[name]
	return reserved
}

func timeScannerTypeName(tableName string) string {
	return strings.TrimSuffix(descriptorName(tableName), "Table") + "TimeScanner"
}

// scanIndexName returns the name of the local constant that holds a column's
// position in the row type's [rasql.ScanMask].
func scanIndexName(columnName string) string {
	return "scanIndex" + goName(columnName)
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

func writeRelationshipDefLiteral(source *bytes.Buffer, relationship schema.RelationshipDef) {
	source.WriteString("{Name: ")
	source.WriteString(quote(relationship.Name))
	if relationship.InverseName != "" {
		source.WriteString(", InverseName: ")
		source.WriteString(quote(relationship.InverseName))
	}
	source.WriteString(", Kind: ")
	source.WriteString(relationshipKindConstant(relationship.Kind))
	if relationship.Optionality != "" {
		source.WriteString(", Optionality: schema.RelationshipOptionality(")
		source.WriteString(quote(string(relationship.Optionality)))
		source.WriteString(")")
	}
	source.WriteString(", Columns: ")
	writeStringLiteralSlice(source, relationship.Columns)
	if relationship.ReferencedSchema != "" {
		source.WriteString(", ReferencedSchema: ")
		source.WriteString(quote(relationship.ReferencedSchema))
	}
	if relationship.ResolvedReferencedSchema != "" {
		source.WriteString(", ResolvedReferencedSchema: ")
		source.WriteString(quote(relationship.ResolvedReferencedSchema))
	}
	source.WriteString(", ReferencedTable: ")
	source.WriteString(quote(relationship.ReferencedTable))
	source.WriteString(", ReferencedColumns: ")
	writeStringLiteralSlice(source, relationship.ReferencedColumns)
	if relationship.Through != nil {
		source.WriteString(", Through: &schema.RelationshipThrough{Table: schema.ObjectName{Schema: ")
		source.WriteString(quote(relationship.Through.Table.Schema))
		source.WriteString(", Name: ")
		source.WriteString(quote(relationship.Through.Table.Name))
		source.WriteString("}, SourceColumns: ")
		writeStringLiteralSlice(source, relationship.Through.SourceColumns)
		source.WriteString(", TargetColumns: ")
		writeStringLiteralSlice(source, relationship.Through.TargetColumns)
		source.WriteString("}")
	}
	source.WriteString("},\n")
}

func relationshipKindConstant(kind schema.RelationshipKind) string {
	switch kind {
	case schema.RelationshipBelongsTo:
		return "schema.RelationshipBelongsTo"
	case schema.RelationshipHasMany:
		return "schema.RelationshipHasMany"
	case schema.RelationshipHasOne:
		return "schema.RelationshipHasOne"
	case schema.RelationshipManyToMany:
		return "schema.RelationshipManyToMany"
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
