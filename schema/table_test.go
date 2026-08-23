package schema_test

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestTableCloneCopiesDescriptor(t *testing.T) {
	descriptor := validTable()
	descriptor.Schema = "audit"
	descriptor.RowName = "Order"
	descriptor.ExclusionConstraints = []schema.ExclusionDef{{
		Name:     "orders_no_overlap",
		Elements: []schema.ExclusionElementDef{{Expression: "customer_id", Operator: "="}},
	}}
	descriptor.Relationships = []schema.RelationshipDef{{
		Name:              "Customer",
		Kind:              schema.RelationshipBelongsTo,
		Columns:           []string{"customer_id"},
		ReferencedTable:   "customers",
		ReferencedColumns: []string{"id"},
	}}
	table := descriptor.Clone()

	descriptor.Columns[0].Name = "changed"
	descriptor.PrimaryKey[0] = "changed"
	descriptor.Indexes[0].Columns[0] = "changed"
	descriptor.ForeignKeys[0].ReferencedColumns[0] = "changed"
	descriptor.ExclusionConstraints[0].Elements[0].Expression = "changed"
	descriptor.Relationships[0].Columns[0] = "changed"
	descriptor.Schema = "changed"
	descriptor.RowName = "changed"

	require.Equal(t, "audit", table.Schema)
	require.Equal(t, "Order", table.RowName)
	require.Equal(t, "id", table.Columns[0].Name)
	require.Equal(t, "id", table.PrimaryKey[0])
	require.Equal(t, "customer_id", table.Indexes[0].Columns[0])
	require.Equal(t, "id", table.ForeignKeys[0].ReferencedColumns[0])
	require.Equal(t, sqltext.Text("customer_id"), table.ExclusionConstraints[0].Elements[0].Expression)
	require.Equal(t, "customer_id", table.Relationships[0].Columns[0])

	amount, ok := table.Column("amount")
	require.True(t, ok)
	decimal, ok := amount.Type.(schema.DecimalType)
	require.True(t, ok)
	require.Equal(t, 19, decimal.Precision)
	scale, stated := decimal.Scale.Value()
	require.True(t, stated)
	require.Equal(t, 4, scale)
}

// TestTableCloneNilSlicesRoundTrip covers the empty descriptor Clone's
// TestTableCloneCopiesDescriptor case never exercises: a TableDef whose
// nine slice fields (Columns, PrimaryKey, VirtualTableModuleArguments,
// UniqueConstraints, Checks, ExclusionConstraints, Indexes, ForeignKeys,
// Relationships) are all left nil must clone to a value that is
// reflect.DeepEqual to itself, rather than to one where any of those
// fields turned into a non-nil empty slice. Its second case makes the same
// demand of every container a constraint, index, foreign key or
// relationship owns.
func TestTableCloneNilSlicesRoundTrip(t *testing.T) {
	t.Run("table level", func(t *testing.T) {
		descriptor := schema.TableDef{Name: "orders"}
		clone := descriptor.Clone()

		require.True(t, reflect.DeepEqual(descriptor, clone))
		require.Nil(t, clone.Columns)
		require.Nil(t, clone.PrimaryKey)
		require.Nil(t, clone.VirtualTableModuleArguments)
		require.Nil(t, clone.UniqueConstraints)
		require.Nil(t, clone.Checks)
		require.Nil(t, clone.ExclusionConstraints)
		require.Nil(t, clone.Indexes)
		require.Nil(t, clone.ForeignKeys)
		require.Nil(t, clone.Relationships)
	})

	t.Run("within an element", func(t *testing.T) {
		descriptor := schema.TableDef{
			Name:                 "orders",
			UniqueConstraints:    []schema.UniqueDef{{Name: "orders_email_key"}},
			ExclusionConstraints: []schema.ExclusionDef{{Name: "orders_no_overlap"}},
			Indexes:              []schema.IndexDef{{Name: "orders_id_idx"}},
			ForeignKeys:          []schema.ForeignKeyDef{{Name: "orders_customer_fk"}},
			Relationships:        []schema.RelationshipDef{{Name: "Customer"}},
		}
		clone := descriptor.Clone()

		require.True(t, reflect.DeepEqual(descriptor, clone))
		require.Nil(t, clone.UniqueConstraints[0].Columns)
		require.Nil(t, clone.UniqueConstraints[0].IncludeColumns)
		require.Nil(t, clone.UniqueConstraints[0].Keys)
		require.Nil(t, clone.UniqueConstraints[0].StorageParameters)
		require.Nil(t, clone.UniqueConstraints[0].Collations)
		require.Nil(t, clone.ExclusionConstraints[0].Elements)
		require.Nil(t, clone.Indexes[0].Columns)
		require.Nil(t, clone.Indexes[0].Expressions)
		require.Nil(t, clone.Indexes[0].IncludeColumns)
		require.Nil(t, clone.Indexes[0].Keys)
		require.Nil(t, clone.Indexes[0].StorageParameters)
		require.Nil(t, clone.ForeignKeys[0].Columns)
		require.Nil(t, clone.ForeignKeys[0].ReferencedColumns)
		require.Nil(t, clone.ForeignKeys[0].DeleteSetColumns)
		require.Nil(t, clone.Relationships[0].Columns)
		require.Nil(t, clone.Relationships[0].ReferencedColumns)
	})
}

// TestTableCloneEmptyContainersStayNonNil covers the other direction of the
// same property: a container the source states as empty rather than leaving
// unset must clone to a non-nil empty container, so a descriptor decoded
// from JSON with an empty array in it (encoding/json produces a non-nil
// empty slice for one) still clones to a value equal to itself.
func TestTableCloneEmptyContainersStayNonNil(t *testing.T) {
	t.Run("table level", func(t *testing.T) {
		descriptor := schema.TableDef{
			Name:                        "orders",
			Columns:                     []schema.ColumnDef{},
			PrimaryKey:                  []string{},
			VirtualTableModuleArguments: []string{},
			UniqueConstraints:           []schema.UniqueDef{},
			Checks:                      []schema.CheckDef{},
			ExclusionConstraints:        []schema.ExclusionDef{},
			Indexes:                     []schema.IndexDef{},
			ForeignKeys:                 []schema.ForeignKeyDef{},
			Relationships:               []schema.RelationshipDef{},
		}
		clone := descriptor.Clone()

		require.True(t, reflect.DeepEqual(descriptor, clone))
		require.NotNil(t, clone.Columns)
		require.NotNil(t, clone.PrimaryKey)
		require.NotNil(t, clone.VirtualTableModuleArguments)
		require.NotNil(t, clone.UniqueConstraints)
		require.NotNil(t, clone.Checks)
		require.NotNil(t, clone.ExclusionConstraints)
		require.NotNil(t, clone.Indexes)
		require.NotNil(t, clone.ForeignKeys)
		require.NotNil(t, clone.Relationships)
	})

	t.Run("within an element", func(t *testing.T) {
		descriptor := schema.TableDef{
			Name: "orders",
			UniqueConstraints: []schema.UniqueDef{{
				Name:              "orders_email_key",
				Columns:           []string{},
				IncludeColumns:    []string{},
				Keys:              []schema.IndexKeyDef{},
				StorageParameters: map[string]string{},
				Collations:        map[string]string{},
			}},
			ExclusionConstraints: []schema.ExclusionDef{{
				Name:     "orders_no_overlap",
				Elements: []schema.ExclusionElementDef{},
			}},
			Indexes: []schema.IndexDef{{
				Name:              "orders_id_idx",
				Columns:           []string{},
				Expressions:       []sqltext.Text{},
				IncludeColumns:    []string{},
				Keys:              []schema.IndexKeyDef{},
				StorageParameters: map[string]string{},
			}},
			ForeignKeys: []schema.ForeignKeyDef{{
				Name:              "orders_customer_fk",
				Columns:           []string{},
				ReferencedColumns: []string{},
				DeleteSetColumns:  []string{},
			}},
			Relationships: []schema.RelationshipDef{{
				Name:              "Customer",
				Columns:           []string{},
				ReferencedColumns: []string{},
			}},
		}
		clone := descriptor.Clone()

		require.True(t, reflect.DeepEqual(descriptor, clone))
		require.NotNil(t, clone.UniqueConstraints[0].Columns)
		require.NotNil(t, clone.UniqueConstraints[0].IncludeColumns)
		require.NotNil(t, clone.UniqueConstraints[0].Keys)
		require.NotNil(t, clone.UniqueConstraints[0].StorageParameters)
		require.NotNil(t, clone.UniqueConstraints[0].Collations)
		require.NotNil(t, clone.ExclusionConstraints[0].Elements)
		require.NotNil(t, clone.Indexes[0].Columns)
		require.NotNil(t, clone.Indexes[0].Expressions)
		require.NotNil(t, clone.Indexes[0].IncludeColumns)
		require.NotNil(t, clone.Indexes[0].Keys)
		require.NotNil(t, clone.Indexes[0].StorageParameters)
		require.NotNil(t, clone.ForeignKeys[0].Columns)
		require.NotNil(t, clone.ForeignKeys[0].ReferencedColumns)
		require.NotNil(t, clone.ForeignKeys[0].DeleteSetColumns)
		require.NotNil(t, clone.Relationships[0].Columns)
		require.NotNil(t, clone.Relationships[0].ReferencedColumns)
	})
}

// TestTableCloneContainersAreIndependent proves Clone's doc claim that a
// clone shares no slice, no map and no pointer with its source, for every
// container any descriptor a TableDef reaches owns and for a column type
// held by pointer, and in both directions: writing through the source must
// be invisible to the clone, and writing through the clone must be invisible
// to the source. Each case compares the side it did not touch against a
// freshly built descriptor, so anything still shared anywhere in the tree
// shows up as an inequality rather than needing its own assertion.
func TestTableCloneContainersAreIndependent(t *testing.T) {
	t.Run("mutating the source", func(t *testing.T) {
		descriptor := containerTableDef()
		clone := descriptor.Clone()

		mutateContainers(&descriptor)

		require.True(t, reflect.DeepEqual(containerTableDef(), clone))
	})

	t.Run("mutating the clone", func(t *testing.T) {
		descriptor := containerTableDef()
		clone := descriptor.Clone()

		mutateContainers(&clone)

		require.True(t, reflect.DeepEqual(containerTableDef(), descriptor))
	})
}

// TestRelationshipCloneContainers covers RelationshipDef.Clone on its own,
// since it is exported and TableDef.Clone is not its only caller: it must
// keep each column list's nilness and hand back lists independent of the
// source's in both directions.
func TestRelationshipCloneContainers(t *testing.T) {
	unset := schema.RelationshipDef{Name: "Customer", Kind: schema.RelationshipBelongsTo}
	require.True(t, reflect.DeepEqual(unset, unset.Clone()))
	require.Nil(t, unset.Clone().Columns)
	require.Nil(t, unset.Clone().ReferencedColumns)

	empty := schema.RelationshipDef{
		Name:              "Customer",
		Kind:              schema.RelationshipBelongsTo,
		Columns:           []string{},
		ReferencedColumns: []string{},
	}
	require.True(t, reflect.DeepEqual(empty, empty.Clone()))
	require.NotNil(t, empty.Clone().Columns)
	require.NotNil(t, empty.Clone().ReferencedColumns)

	populated := schema.RelationshipDef{
		Name:              "Customer",
		Kind:              schema.RelationshipBelongsTo,
		Columns:           []string{"customer_id"},
		ReferencedTable:   "customers",
		ReferencedColumns: []string{"id"},
	}

	fromSource := populated.Clone()
	populated.Columns[0] = "changed"
	populated.ReferencedColumns[0] = "changed"
	require.Equal(t, "customer_id", fromSource.Columns[0])
	require.Equal(t, "id", fromSource.ReferencedColumns[0])

	populated.Columns[0] = "customer_id"
	populated.ReferencedColumns[0] = "id"
	fromClone := populated.Clone()
	fromClone.Columns[0] = "changed"
	fromClone.ReferencedColumns[0] = "changed"
	require.Equal(t, "customer_id", populated.Columns[0])
	require.Equal(t, "id", populated.ReferencedColumns[0])
}

// TestTableCloneCoversEveryContainerField is the mechanical half of the same
// guarantee: rather than naming the containers it knows about, it walks
// TableDef with reflection, fills every slice and map it finds -- at the
// table level and inside each element type -- and every column type field
// with a pointer, clones the result, and requires nothing to be shared. A
// container field, or a field holding a column type, added to TableDef or to
// any descriptor type it reaches fails here as soon as that type's own Clone
// forgets to copy it, so keeping this file's hand-written cases up to date is
// not what stands between a new field and a silently aliased one.
func TestTableCloneCoversEveryContainerField(t *testing.T) {
	descriptor := reflect.New(reflect.TypeOf(schema.TableDef{})).Elem()
	fillContainerFields(descriptor)
	source := descriptor.Interface().(schema.TableDef)

	clone := source.Clone()

	require.True(t, reflect.DeepEqual(source, clone))
	requireContainerFieldsUnshared(t, reflect.ValueOf(source), reflect.ValueOf(clone), "TableDef")
}

// fillContainerFields gives every slice field of value one element, every map
// field one entry, and every schema.ColumnType field a pointer to a column
// type, descending into the element type of each slice so a container an
// element owns is filled too. A field of any other kind is left at its zero
// value: a slice, a map and a pointer are the three things a descriptor and
// its clone can share, and ColumnDef.Type is the only field that can hold the
// third.
func fillContainerFields(value reflect.Value) {
	for i := range value.NumField() {
		field := value.Field(i)
		switch field.Kind() {
		case reflect.Slice:
			element := reflect.New(field.Type().Elem()).Elem()
			if element.Kind() == reflect.Struct {
				fillContainerFields(element)
			}
			field.Set(reflect.Append(field, element))
		case reflect.Map:
			field.Set(reflect.MakeMap(field.Type()))
			field.SetMapIndex(reflect.New(field.Type().Key()).Elem(), reflect.New(field.Type().Elem()).Elem())
		case reflect.Interface:
			if field.Type() == reflect.TypeOf((*schema.ColumnType)(nil)).Elem() {
				field.Set(reflect.ValueOf(schema.ColumnType(&schema.IntegerType{})))
			}
		}
	}
}

// requireContainerFieldsUnshared reports a failure for each slice, map or
// pointer-backed interface field source and clone still share, naming the
// field's own path through the descriptor. It descends into slice elements
// the same way fillContainerFields does, so a container an element owns is
// checked too.
func requireContainerFieldsUnshared(t *testing.T, source, clone reflect.Value, path string) {
	t.Helper()
	for i := range source.NumField() {
		name := path + "." + source.Type().Field(i).Name
		sourceField, cloneField := source.Field(i), clone.Field(i)
		switch sourceField.Kind() {
		case reflect.Interface:
			require.False(t, sourceField.IsNil(), "%s: fixture left this interface nil, so nothing was checked", name)
			if sourceField.Elem().Kind() != reflect.Pointer {
				continue
			}
			require.NotEqual(t, sourceField.Elem().Pointer(), cloneField.Elem().Pointer(), "%s points at the same value as the source", name)
		case reflect.Slice:
			require.NotZero(t, sourceField.Len(), "%s: fixture left this slice empty, so nothing was checked", name)
			require.NotEqual(t, sourceField.Pointer(), cloneField.Pointer(), "%s shares its backing array with the source", name)
			if sourceField.Type().Elem().Kind() != reflect.Struct {
				continue
			}
			for element := range sourceField.Len() {
				requireContainerFieldsUnshared(t, sourceField.Index(element), cloneField.Index(element), fmt.Sprintf("%s[%d]", name, element))
			}
		case reflect.Map:
			require.NotZero(t, sourceField.Len(), "%s: fixture left this map empty, so nothing was checked", name)
			require.NotEqual(t, sourceField.Pointer(), cloneField.Pointer(), "%s shares its map with the source", name)
		}
	}
}

// containerTableDef returns a descriptor that states every container any
// schema descriptor owns, each with one entry, so a clone of it exercises
// every copy TableDef.Clone makes. Each call builds the value afresh, so a
// test can compare a mutated copy against a pristine one.
//
// The second column holds its ColumnType by pointer. That is not a shape the
// rest of the package supports -- Validate and JSON encoding both reject it,
// since a column type is meant to be held by value -- but a pointer is the
// one thing besides a slice or a map that a plain assignment would leave
// shared, so Clone has to copy what it points at and this fixture has to
// state one.
func containerTableDef() schema.TableDef {
	return schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: &schema.DecimalType{Precision: 19}},
		},
		PrimaryKey:                  []string{"id"},
		VirtualTableModule:          "fts5",
		VirtualTableModuleArguments: []string{"content=''"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:              "orders_email_key",
			Columns:           []string{"email"},
			IncludeColumns:    []string{"id"},
			Keys:              []schema.IndexKeyDef{{Expression: "email"}},
			StorageParameters: map[string]string{"fillfactor": "70"},
			Collations:        map[string]string{"email": "C"},
		}},
		Checks: []schema.CheckDef{{Name: "orders_amount_positive", Expression: "amount > 0"}},
		ExclusionConstraints: []schema.ExclusionDef{{
			Name:     "orders_no_overlap",
			Elements: []schema.ExclusionElementDef{{Expression: "customer_id", Operator: "="}},
		}},
		Indexes: []schema.IndexDef{{
			Name:              "orders_customer_id_idx",
			Columns:           []string{"customer_id"},
			Expressions:       []sqltext.Text{"lower(email)"},
			IncludeColumns:    []string{"id"},
			Keys:              []schema.IndexKeyDef{{Expression: "customer_id"}},
			StorageParameters: map[string]string{"fillfactor": "70"},
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.SetNull,
			DeleteSetColumns:  []string{"customer_id"},
		}},
		Relationships: []schema.RelationshipDef{{
			Name:              "Customer",
			Kind:              schema.RelationshipBelongsTo,
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
		}},
	}
}

// mutateContainers writes through every container containerTableDef states,
// including a new key in each map, and through the pointer its second column
// holds its type by, so anything a clone still shares with its source
// carries the write across.
func mutateContainers(table *schema.TableDef) {
	table.Columns[0].Name = "changed"
	table.Columns[1].Type.(*schema.DecimalType).Precision = 1
	table.PrimaryKey[0] = "changed"
	table.VirtualTableModuleArguments[0] = "changed"
	table.Checks[0].Expression = "changed"

	unique := &table.UniqueConstraints[0]
	unique.Name = "changed"
	unique.Columns[0] = "changed"
	unique.IncludeColumns[0] = "changed"
	unique.Keys[0].Expression = "changed"
	unique.StorageParameters["fillfactor"] = "changed"
	unique.StorageParameters["added"] = "changed"
	unique.Collations["email"] = "changed"
	unique.Collations["added"] = "changed"

	exclusion := &table.ExclusionConstraints[0]
	exclusion.Name = "changed"
	exclusion.Elements[0].Expression = "changed"

	index := &table.Indexes[0]
	index.Name = "changed"
	index.Columns[0] = "changed"
	index.Expressions[0] = "changed"
	index.IncludeColumns[0] = "changed"
	index.Keys[0].Expression = "changed"
	index.StorageParameters["fillfactor"] = "changed"
	index.StorageParameters["added"] = "changed"

	key := &table.ForeignKeys[0]
	key.Name = "changed"
	key.Columns[0] = "changed"
	key.ReferencedColumns[0] = "changed"
	key.DeleteSetColumns[0] = "changed"

	relationship := &table.Relationships[0]
	relationship.Name = "changed"
	relationship.Columns[0] = "changed"
	relationship.ReferencedColumns[0] = "changed"
}

// TestTableCloneMixedNilAndPopulatedSlices covers a descriptor with some of
// the nine slice fields populated and others left nil, so a future fix that
// preserves nil by dropping the deep copy (aliasing the source slice's
// backing array instead of copying it) cannot pass unnoticed: it asserts
// both that unset fields stay nil and that the populated ones are
// independent copies.
func TestTableCloneMixedNilAndPopulatedSlices(t *testing.T) {
	descriptor := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		Indexes: []schema.IndexDef{{
			Name:    "orders_id_idx",
			Columns: []string{"id"},
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
		}},
	}
	clone := descriptor.Clone()

	require.True(t, reflect.DeepEqual(descriptor, clone))
	require.Nil(t, clone.PrimaryKey)
	require.Nil(t, clone.VirtualTableModuleArguments)
	require.Nil(t, clone.UniqueConstraints)
	require.Nil(t, clone.Checks)
	require.Nil(t, clone.ExclusionConstraints)
	require.Nil(t, clone.Relationships)

	descriptor.Indexes[0].Columns[0] = "changed"
	descriptor.ForeignKeys[0].ReferencedColumns[0] = "changed"
	require.Equal(t, "id", clone.Indexes[0].Columns[0])
	require.Equal(t, "id", clone.ForeignKeys[0].ReferencedColumns[0])
}

// TestDecimalScale covers the distinction the type exists for: a stated scale
// of zero must not read back the same as a scale nobody stated.
func TestDecimalScale(t *testing.T) {
	var unstated schema.DecimalScale
	_, stated := unstated.Value()
	require.False(t, stated)

	value, stated := schema.NewDecimalScale(0).Value()
	require.True(t, stated)
	require.Equal(t, 0, value)

	value, stated = schema.NewDecimalScale(4).Value()
	require.True(t, stated)
	require.Equal(t, 4, value)
}

// TestTextWidth covers the distinction the type exists for: a stated width
// of zero must not read back the same as a width nobody stated.
func TestTextWidth(t *testing.T) {
	var unstated schema.TextWidth
	_, stated := unstated.Value()
	require.False(t, stated)

	value, stated := schema.NewTextWidth(0).Value()
	require.True(t, stated)
	require.Equal(t, 0, value)

	value, stated = schema.NewTextWidth(255).Value()
	require.True(t, stated)
	require.Equal(t, 255, value)
}

func TestColumnTypeKinds(t *testing.T) {
	tests := []struct {
		name      string
		typeValue schema.ColumnType
		kind      schema.TypeKind
	}{
		{name: "boolean", typeValue: schema.BooleanType{}, kind: schema.KindBoolean},
		{name: "integer", typeValue: schema.IntegerType{}, kind: schema.KindInteger},
		{name: "float", typeValue: schema.FloatType{}, kind: schema.KindFloat},
		{name: "text", typeValue: schema.TextType{}, kind: schema.KindText},
		{name: "bytes", typeValue: schema.BytesType{}, kind: schema.KindBytes},
		{name: "time", typeValue: schema.TimeType{}, kind: schema.KindTime},
		{name: "json", typeValue: schema.JSONType{}, kind: schema.KindJSON},
		{name: "uuid", typeValue: schema.UUIDType{}, kind: schema.KindUUID},
		{name: "decimal", typeValue: schema.DecimalType{}, kind: schema.KindDecimal},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.Equal(t, test.kind, test.typeValue.Kind())
		})
	}
}

// TestDecimalScaleJSON pins the snapshot form rasqlgen's -input reads: a
// stated scale is a plain JSON number, and an unstated one is null rather than
// a number that would decode back as a stated scale of zero.
func TestDecimalScaleJSON(t *testing.T) {
	encoded, err := json.Marshal(schema.ColumnDef{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(0)}})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Kind":"decimal","Precision":19,"Scale":0`)
	require.Contains(t, string(encoded), `"Unsigned":false`)
	require.Contains(t, string(encoded), `"ZeroFill":false`)

	encoded, err = json.Marshal(schema.ColumnDef{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"DisplayWidth":null`)
	require.Contains(t, string(encoded), `"Kind":"integer"`)
	require.Contains(t, string(encoded), `"Unsigned":false`)
	require.Contains(t, string(encoded), `"ZeroFill":false`)

	var decoded schema.ColumnDef
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"amount","Type":{"Kind":"decimal","Precision":19,"Scale":0}}`), &decoded))
	decimal, ok := decoded.Type.(schema.DecimalType)
	require.True(t, ok)
	scale, stated := decimal.Scale.Value()
	require.True(t, stated)
	require.Equal(t, 0, scale)

	decoded = schema.ColumnDef{}
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"amount","Type":{"Kind":"decimal","Precision":19}}`), &decoded))
	decimal, ok = decoded.Type.(schema.DecimalType)
	require.True(t, ok)
	_, stated = decimal.Scale.Value()
	require.False(t, stated)

	decoded = schema.ColumnDef{}
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"amount","Type":{"Kind":"decimal","Precision":19,"Scale":null}}`), &decoded))
	decimal, ok = decoded.Type.(schema.DecimalType)
	require.True(t, ok)
	_, stated = decimal.Scale.Value()
	require.False(t, stated)

	require.Error(t, json.Unmarshal([]byte(`{"Name":"amount","Type":{"Kind":"decimal","Precision":19,"Scale":"four"}}`), &decoded))
}

// TestTextWidthJSON covers the JSON round-trip of both TextType forms: a
// stated width is a plain JSON number and an unstated one is null rather
// than a number that would decode back as a stated width of zero, the same
// distinction TestDecimalScaleJSON pins for DecimalType.Scale.
func TestTextWidthJSON(t *testing.T) {
	encoded, err := json.Marshal(schema.ColumnDef{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Type":{"Fixed":false,"Kind":"text","Width":255}`)

	encoded, err = json.Marshal(schema.ColumnDef{Name: "bio", Type: schema.TextType{}})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Type":{"Fixed":false,"Kind":"text","Width":null}`)

	var decoded schema.ColumnDef
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"email","Type":{"Kind":"text","Width":255}}`), &decoded))
	text, ok := decoded.Type.(schema.TextType)
	require.True(t, ok)
	width, stated := text.Width.Value()
	require.True(t, stated)
	require.Equal(t, 255, width)

	decoded = schema.ColumnDef{}
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"bio","Type":{"Kind":"text","Width":null}}`), &decoded))
	text, ok = decoded.Type.(schema.TextType)
	require.True(t, ok)
	_, stated = text.Width.Value()
	require.False(t, stated)

	// A snapshot written before TextType had a width omits it entirely,
	// which decodes as unstated rather than an error, exactly like the
	// existing decimal snapshots this package still reads.
	decoded = schema.ColumnDef{}
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"bio","Type":{"Kind":"text"}}`), &decoded))
	text, ok = decoded.Type.(schema.TextType)
	require.True(t, ok)
	_, stated = text.Width.Value()
	require.False(t, stated)

	require.Error(t, json.Unmarshal([]byte(`{"Name":"email","Type":{"Kind":"text","Width":"wide"}}`), &decoded))
}

// TestTextTypeFixedJSON covers the JSON round-trip of TextType.Fixed
// alongside Width, for both the fixed-width and variable-width forms, and
// pins that a snapshot written before Fixed existed decodes as unstated
// (false), the same backward-compatible read TestTextWidthJSON pins for a
// snapshot written before Width existed.
func TestTextTypeFixedJSON(t *testing.T) {
	encoded, err := json.Marshal(schema.ColumnDef{Name: "id", Type: schema.TextType{Width: schema.NewTextWidth(36), Fixed: true}})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Type":{"Fixed":true,"Kind":"text","Width":36}`)

	var decoded schema.ColumnDef
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, schema.TextType{Width: schema.NewTextWidth(36), Fixed: true}, decoded.Type)

	encoded, err = json.Marshal(schema.ColumnDef{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Type":{"Fixed":false,"Kind":"text","Width":255}`)

	decoded = schema.ColumnDef{}
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, schema.TextType{Width: schema.NewTextWidth(255)}, decoded.Type)

	// A snapshot written before TextType had Fixed omits it entirely, which
	// decodes as false rather than an error.
	decoded = schema.ColumnDef{}
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"email","Type":{"Kind":"text","Width":255}}`), &decoded))
	require.Equal(t, schema.TextType{Width: schema.NewTextWidth(255)}, decoded.Type)

	require.Error(t, json.Unmarshal([]byte(`{"Name":"id","Type":{"Kind":"text","Width":36,"Fixed":"yes"}}`), &decoded))
}

func TestTableColumn(t *testing.T) {
	table := validTable()
	require.NoError(t, table.Validate())

	column, ok := table.Column("customer_id")
	require.True(t, ok)
	require.Equal(t, schema.IntegerType{}, column.Type)

	_, ok = table.Column("missing")
	require.False(t, ok)
}

func TestTableValidate(t *testing.T) {
	tests := map[string]schema.TableDef{
		"empty table name": {
			Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		},
		"invalid column type": {
			Name:    "orders",
			Columns: []schema.ColumnDef{{Name: "id"}},
		},
		"invalid row name": {
			Name:    "orders",
			RowName: "1bad",
			Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		},
		"duplicate column": {
			Name: "orders",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "id", Type: schema.IntegerType{}},
			},
		},
		"unknown primary key column": {
			Name:       "orders",
			Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			PrimaryKey: []string{"missing"},
		},
		"empty index name": {
			Name:    "orders",
			Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			Indexes: []schema.IndexDef{{Columns: []string{"id"}}},
		},
		"foreign key arity": {
			Name: "orders",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "customer_id", Type: schema.IntegerType{}},
			},
			ForeignKeys: []schema.ForeignKeyDef{{
				Columns:           []string{"id", "customer_id"},
				ReferencedTable:   "customers",
				ReferencedColumns: []string{"id"},
			}},
		},
		"unique constraint name duplicates check name": {
			Name: "orders",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
			},
			UniqueConstraints: []schema.UniqueDef{{
				Name:    "dup",
				Columns: []string{"id"},
			}},
			Checks: []schema.CheckDef{{
				Name:       "dup",
				Expression: "id > 0",
			}},
		},
		"unique constraint name duplicates foreign key name": {
			Name: "orders",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "org_id", Type: schema.IntegerType{}},
			},
			UniqueConstraints: []schema.UniqueDef{{
				Name:    "dup",
				Columns: []string{"id"},
			}},
			ForeignKeys: []schema.ForeignKeyDef{{
				Name:              "dup",
				Columns:           []string{"org_id"},
				ReferencedTable:   "orgs",
				ReferencedColumns: []string{"id"},
			}},
		},
		"check name duplicates foreign key name": {
			Name: "orders",
			Columns: []schema.ColumnDef{
				{Name: "id", Type: schema.IntegerType{}},
				{Name: "org_id", Type: schema.IntegerType{}},
			},
			Checks: []schema.CheckDef{{
				Name:       "dup",
				Expression: "id > 0",
			}},
			ForeignKeys: []schema.ForeignKeyDef{{
				Name:              "dup",
				Columns:           []string{"org_id"},
				ReferencedTable:   "orgs",
				ReferencedColumns: []string{"id"},
			}},
		},
		"decimal column with zero precision": {
			Name:    "payments",
			Columns: []schema.ColumnDef{{Name: "amount", Type: schema.DecimalType{Precision: 0, Scale: schema.NewDecimalScale(0)}}},
		},
		"decimal column with negative scale": {
			Name:    "payments",
			Columns: []schema.ColumnDef{{Name: "amount", Type: schema.DecimalType{Precision: 4, Scale: schema.NewDecimalScale(-1)}}},
		},
		"decimal scale exceeds precision": {
			Name:    "payments",
			Columns: []schema.ColumnDef{{Name: "amount", Type: schema.DecimalType{Precision: 4, Scale: schema.NewDecimalScale(5)}}},
		},
		"decimal column with no scale": {
			Name:    "payments",
			Columns: []schema.ColumnDef{{Name: "amount", Type: schema.DecimalType{Precision: 19}}},
		},
		"text column with negative width": {
			Name:    "users",
			Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(-1)}}},
		},
		"fixed-width text column without a stated width": {
			Name:    "users",
			Columns: []schema.ColumnDef{{Name: "code", Type: schema.TextType{Fixed: true}}},
		},
	}

	for name, table := range tests {
		t.Run(name, func(t *testing.T) {
			err := table.Validate()
			require.Error(t, err)

			var validationErr *schema.ValidationError
			require.True(t, errors.As(err, &validationErr))
		})
	}
}

// TestTableValidateDecimalColumnMessages pins the exact wording of each
// decimal validation error, since TestTableValidate's shared case table only
// checks that a *schema.ValidationError came back, not what it says.
func TestTableValidateDecimalColumnMessages(t *testing.T) {
	tests := map[string]struct {
		column  schema.ColumnDef
		wantErr string
	}{
		"zero precision": {
			column:  schema.ColumnDef{Name: "amount", Type: schema.DecimalType{Precision: 0, Scale: schema.NewDecimalScale(0)}},
			wantErr: "must state a precision of at least 1",
		},
		"negative scale": {
			column:  schema.ColumnDef{Name: "amount", Type: schema.DecimalType{Precision: 4, Scale: schema.NewDecimalScale(-1)}},
			wantErr: "must not be negative",
		},
		"scale exceeds precision": {
			column:  schema.ColumnDef{Name: "amount", Type: schema.DecimalType{Precision: 4, Scale: schema.NewDecimalScale(5)}},
			wantErr: "scale 5 exceeds precision 4",
		},
		"no scale at all": {
			column:  schema.ColumnDef{Name: "amount", Type: schema.DecimalType{Precision: 19}},
			wantErr: "must state a scale",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			table := schema.TableDef{Name: "payments", Columns: []schema.ColumnDef{test.column}}
			err := table.Validate()
			require.ErrorContains(t, err, test.wantErr)
		})
	}
}

// TestTableValidateAcceptsDecimalColumn is the positive counterpart: a
// decimal column with a valid precision and scale must validate cleanly. A
// stated scale of zero is one of those valid scales, so it is covered here
// rather than among the rejections.
func TestTableValidateAcceptsDecimalColumn(t *testing.T) {
	table := schema.TableDef{
		Name: "payments",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
			{Name: "quantity", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(0)}},
		},
		PrimaryKey: []string{"id"},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateAcceptsNonDefaultIndexMethod proves that an IndexDef
// naming a non-default schema.IndexMethod, such as what inspect now records
// for a live PostgreSQL GIN index or a MySQL FULLTEXT index, is valid input:
// Validate describes the index, and only render.CreateIndexes and the
// migrate diff-live path refuse to build DDL for it.
func TestTableValidateAcceptsNonDefaultIndexMethod(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "tags", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:    "documents_tags_gin_idx",
			Columns: []string{"tags"},
			Method:  "gin",
		}},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateAcceptsPartialIndex proves that an IndexDef naming a
// Predicate, such as what inspect now records for a live PostgreSQL or
// SQLite partial index, is valid input: Validate describes the index, and
// only render.CreateIndexes and the migrate diff-live path refuse to build
// DDL for it.
func TestTableValidateAcceptsPartialIndex(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:      "documents_active_idx",
			Columns:   []string{"status"},
			Predicate: "status = 'active'",
		}},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateAcceptsExpressionIndex proves that an IndexDef naming
// Expressions instead of Columns, such as what inspect now records for a
// live expression index, is valid input, and that Expressions may mix a
// bare column name with a real expression: an index's full key order lives
// in one place instead of being split across Columns and a second list.
func TestTableValidateAcceptsExpressionIndex(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "title", Type: schema.TextType{}},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:        "documents_lower_title_idx",
			Expressions: []sqltext.Text{"lower(title)", "created_at"},
		}},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateRejectsIndexWithBothColumnsAndExpressions proves that an
// IndexDef cannot name both Columns and Expressions: Expressions already
// carries a plain-column key as its own bare column name, so a non-empty
// Columns alongside it would leave the index's true key order ambiguous.
func TestTableValidateRejectsIndexWithBothColumnsAndExpressions(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "title", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:        "documents_bad_idx",
			Columns:     []string{"title"},
			Expressions: []sqltext.Text{"lower(title)"},
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].columns")
}

// TestTableValidateAcceptsIndexKeyDetails proves that an IndexDef naming
// Keys, such as what inspect now records for a descending key, a
// non-default per-key collation or operator class, or a MySQL prefix
// length, is valid input: Validate describes the index, and only
// render.CreateIndexes and the migrate diff-live path refuse to build DDL
// for it.
func TestTableValidateAcceptsIndexKeyDetails(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name: "documents_created_at_idx",
			Keys: []schema.IndexKeyDef{{Expression: "created_at", Descending: true}},
		}},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateRejectsIndexKeyWithEmptyExpression proves that a Keys
// element cannot leave Expression empty: unlike Columns and Expressions,
// where a positional gap would just be an empty string in a flat list, an
// IndexKeyDef with no Expression names no key at all.
func TestTableValidateRejectsIndexKeyWithEmptyExpression(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name: "documents_created_at_idx",
			Keys: []schema.IndexKeyDef{{Descending: true}},
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].keys[0].expression")
}

// TestTableValidateRejectsIndexWithColumnsAndKeys is the Keys counterpart to
// TestTableValidateRejectsIndexWithBothColumnsAndExpressions: Keys already
// carries a plain-column key as its own bare column name in
// IndexKeyDef.Expression, so a non-empty Columns alongside it would leave
// the index's true key order ambiguous.
func TestTableValidateRejectsIndexWithColumnsAndKeys(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "created_at", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:    "documents_bad_idx",
			Columns: []string{"created_at"},
			Keys:    []schema.IndexKeyDef{{Expression: "created_at", Descending: true}},
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].columns")
}

// TestTableValidateRejectsIndexWithExpressionsAndKeys is the Keys
// counterpart of TestTableValidateRejectsIndexWithColumnsAndKeys for
// Expressions: an index states its keys in exactly one of Columns,
// Expressions, or Keys, never a mix.
func TestTableValidateRejectsIndexWithExpressionsAndKeys(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "title", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:        "documents_bad_idx",
			Expressions: []sqltext.Text{"lower(title)"},
			Keys:        []schema.IndexKeyDef{{Expression: "lower(title)"}},
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].expressions")
}

// TestTableValidateAcceptsIndexIncludeColumns proves that an IndexDef
// naming IncludeColumns, such as what inspect now records for a live
// PostgreSQL index with an INCLUDE clause, is valid input: Validate
// describes the index, and only render.CreateIndexes and the migrate
// diff-live path refuse to build DDL for it.
func TestTableValidateAcceptsIndexIncludeColumns(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
			{Name: "title", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:           "documents_status_idx",
			Columns:        []string{"status"},
			IncludeColumns: []string{"title"},
		}},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateRejectsIndexIncludeColumnsDuplicatingKeyColumn proves
// that an index's INCLUDE column cannot repeat one of its own key columns:
// the same rule TestTableValidatesUniqueConstraintIncludeColumnsOverlapsColumns
// enforces for a unique constraint's INCLUDE columns.
func TestTableValidateRejectsIndexIncludeColumnsDuplicatingKeyColumn(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:           "documents_status_idx",
			Columns:        []string{"status"},
			IncludeColumns: []string{"status"},
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].include_columns[0]")
	require.ErrorContains(t, err, `duplicates column "status"`)
}

// TestTableValidateRejectsIndexIncludeColumnsReferencingUnknownColumn
// proves that an index's INCLUDE column must name a real column, the same
// way a key column must.
func TestTableValidateRejectsIndexIncludeColumnsReferencingUnknownColumn(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:           "documents_status_idx",
			Columns:        []string{"status"},
			IncludeColumns: []string{"missing"},
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].include_columns[0]")
	require.ErrorContains(t, err, `references unknown column "missing"`)
}

// TestTableValidateAcceptsInvisibleIndex proves that an IndexDef setting
// Invisible, such as what inspect now records for a live MySQL invisible
// index, is valid input: Validate describes the index, and only
// render.CreateIndexes and the migrate diff-live path refuse to build DDL
// for it.
func TestTableValidateAcceptsInvisibleIndex(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:      "documents_status_idx",
			Columns:   []string{"status"},
			Invisible: true,
		}},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateAcceptsIndexValidityStorageAndPlacement proves that an
// IndexDef setting NotValid, StorageParameters, or Tablespace, such as what
// inspect now records for a live PostgreSQL index, is valid input: Validate
// describes the index, and only render.CreateIndexes and the migrate
// diff-live path refuse to build DDL for it.
func TestTableValidateAcceptsIndexValidityStorageAndPlacement(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:              "documents_status_idx",
			Columns:           []string{"status"},
			NotValid:          true,
			StorageParameters: map[string]string{"fillfactor": "70"},
			Tablespace:        "pg_custom",
		}},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateAcceptsReplicaIdentityIndex proves that a unique
// IndexDef setting ReplicaIdentity, such as what inspect now records for a
// live PostgreSQL table's replica identity index, is valid input.
func TestTableValidateAcceptsReplicaIdentityIndex(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:            "documents_status_idx",
			Columns:         []string{"status"},
			Unique:          true,
			ReplicaIdentity: true,
		}},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateRejectsNonUniqueReplicaIdentityIndex proves that Validate
// rejects a nonsensical ReplicaIdentity: PostgreSQL requires REPLICA
// IDENTITY USING INDEX to name a unique (and NOT NULL) index, so a
// non-unique IndexDef setting ReplicaIdentity can never come from a live
// database and must not be accepted as a descriptor to render either.
func TestTableValidateRejectsNonUniqueReplicaIdentityIndex(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:            "documents_status_idx",
			Columns:         []string{"status"},
			ReplicaIdentity: true,
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].replica_identity")
	require.ErrorContains(t, err, "must not be set on a non-unique index")
}

// TestTableValidateRejectsIndexStorageParameterWithEmptyKey proves that
// Validate rejects an empty StorageParameters key: a live PostgreSQL
// reloptions entry always has a non-empty name, so an empty key can only
// come from a hand-built descriptor and is nonsense to render.
func TestTableValidateRejectsIndexStorageParameterWithEmptyKey(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:              "documents_status_idx",
			Columns:           []string{"status"},
			StorageParameters: map[string]string{"": "70"},
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].storage_parameters")
	require.ErrorContains(t, err, "must not have an empty key")
}

// TestTableValidateAcceptsIndexNullsNotDistinct proves that Validate accepts
// a plain unique index declared NullsNotDistinct.
func TestTableValidateAcceptsIndexNullsNotDistinct(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:             "documents_status_idx",
			Columns:          []string{"status"},
			Unique:           true,
			NullsNotDistinct: true,
		}},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateRejectsNonUniqueIndexNullsNotDistinct proves that
// Validate rejects a nonsensical NullsNotDistinct: NULLS NOT DISTINCT only
// applies to a unique index, so a non-unique IndexDef setting it can never
// come from a live database and must not be accepted as a descriptor to
// render either.
func TestTableValidateRejectsNonUniqueIndexNullsNotDistinct(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}, Nullable: true},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name:             "documents_status_idx",
			Columns:          []string{"status"},
			NullsNotDistinct: true,
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].nulls_not_distinct")
	require.ErrorContains(t, err, "must not be set on a non-unique index")
}

// TestTableValidatesIndexKeyNullsOrder proves that Validate rejects an
// IndexKeyDef.NullsOrder value other than the ones NullsOrder itself names.
func TestTableValidatesIndexKeyNullsOrder(t *testing.T) {
	table := schema.TableDef{
		Name: "documents",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{{
			Name: "documents_status_idx",
			Keys: []schema.IndexKeyDef{{Expression: "status", NullsOrder: schema.NullsOrder("SIDEWAYS")}},
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "indexes[0].keys[0].nulls_order")
	require.ErrorContains(t, err, `unsupported nulls order "SIDEWAYS"`)
}

// TestTableValidateUnsignedColumn covers the integer-specific signedness
// option. Other concrete types cannot carry it in the first place.
func TestTableValidateUnsignedColumn(t *testing.T) {
	table := schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{Unsigned: true}},
			{Name: "sequence", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())

}

// TestTableValidateAcceptsIntegerDisplayWidthAndZeroFill proves that an
// IntegerType naming a stated DisplayWidth and a true ZeroFill, such as what
// inspect now records for a live MySQL int(n) ZEROFILL column, is valid
// input: Validate describes the column, and only render.CreateTable and the
// migrate diff-live path refuse to build DDL for it.
func TestTableValidateAcceptsIntegerDisplayWidthAndZeroFill(t *testing.T) {
	table := schema.TableDef{
		Name: "counters",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "total", Type: schema.IntegerType{Unsigned: true, DisplayWidth: schema.NewIntegerDisplayWidth(10), ZeroFill: true}},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())
}

// TestTableValidateAcceptsDecimalUnsignedAndZeroFill proves that a
// DecimalType naming a true Unsigned and a true ZeroFill, such as what
// inspect now records for a live MySQL DECIMAL(p,s) UNSIGNED ZEROFILL
// column, is valid input: Validate describes the column, and only
// render.CreateTable and the migrate diff-live path refuse to build DDL for
// it.
func TestTableValidateAcceptsDecimalUnsignedAndZeroFill(t *testing.T) {
	table := schema.TableDef{
		Name: "invoices",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.DecimalType{Precision: 10, Scale: schema.NewDecimalScale(2), Unsigned: true, ZeroFill: true}},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())
}

func TestTableValidateRejectsNegativeIntegerDisplayWidth(t *testing.T) {
	table := schema.TableDef{
		Name: "counters",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "total", Type: schema.IntegerType{DisplayWidth: schema.NewIntegerDisplayWidth(-1)}},
		},
		PrimaryKey: []string{"id"},
	}

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "must not be negative")
}

// TestTableValidateAcceptsGeneratedColumn proves that a ColumnDef naming
// GeneratedExpression and GeneratedStorage together, such as what inspect
// now records for a live SQLite generated column, is valid input: Validate
// describes the column, and only render.CreateTable and the migrate
// diff-live path refuse to build DDL for it.
func TestTableValidateAcceptsGeneratedColumn(t *testing.T) {
	table := validTable()
	table.Columns[1].GeneratedExpression = "amount * 2"
	table.Columns[1].GeneratedStorage = schema.GeneratedStored
	require.NoError(t, table.Validate())
}

func TestTableValidateRejectsGeneratedExpressionWithoutStorage(t *testing.T) {
	table := validTable()
	table.Columns[1].GeneratedExpression = "amount * 2"

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "stated together")
}

func TestTableValidateRejectsGeneratedStorageWithoutExpression(t *testing.T) {
	table := validTable()
	table.Columns[1].GeneratedStorage = schema.GeneratedStored

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "stated together")
}

func TestTableValidateRejectsInvalidGeneratedStorage(t *testing.T) {
	table := validTable()
	table.Columns[1].GeneratedExpression = "amount * 2"
	table.Columns[1].GeneratedStorage = schema.GeneratedStorage("bogus")

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported generated column storage")
}

// TestTableValidateRejectsGeneratedColumnWithDefault proves that a
// generated column stating both GeneratedExpression and Default is
// rejected: a generated column's value comes from its expression, and SQL
// itself does not let a generated column also carry a DEFAULT clause.
func TestTableValidateRejectsGeneratedColumnWithDefault(t *testing.T) {
	table := validTable()
	table.Columns[2].GeneratedExpression = "upper(status)"
	table.Columns[2].GeneratedStorage = schema.GeneratedVirtual

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "must not also state a Default")
}

// TestTableValidateAcceptsIdentityColumn proves that a ColumnDef naming
// either IdentityGeneration, such as what inspect now records for a live
// PostgreSQL GENERATED ... AS IDENTITY column or a MySQL AUTO_INCREMENT
// column, is valid input.
func TestTableValidateAcceptsIdentityColumn(t *testing.T) {
	always := validTable()
	always.Columns[1].Identity = schema.IdentityAlways
	require.NoError(t, always.Validate())

	byDefault := validTable()
	byDefault.Columns[1].Identity = schema.IdentityByDefault
	require.NoError(t, byDefault.Validate())
}

func TestTableValidateRejectsInvalidIdentity(t *testing.T) {
	table := validTable()
	table.Columns[1].Identity = schema.IdentityGeneration("bogus")

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "unsupported identity generation")
}

// TestTableValidateRejectsIdentityWithGeneratedExpression proves that
// Identity and GeneratedExpression stated on the same column are rejected:
// they are different features, and no engine this package renders accepts
// both on one column.
func TestTableValidateRejectsIdentityWithGeneratedExpression(t *testing.T) {
	table := validTable()
	table.Columns[1].Identity = schema.IdentityAlways
	table.Columns[1].GeneratedExpression = "1"
	table.Columns[1].GeneratedStorage = schema.GeneratedStored

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "must not also state a GeneratedExpression")
}

// TestTableValidateRejectsIdentityWithDefault proves that an identity
// column stating a Default is rejected: PostgreSQL forbids a DEFAULT
// clause on a GENERATED ... AS IDENTITY column, and MySQL forbids one on
// AUTO_INCREMENT.
func TestTableValidateRejectsIdentityWithDefault(t *testing.T) {
	table := validTable()
	table.Columns[1].Identity = schema.IdentityByDefault
	table.Columns[1].Default = "0"

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "must not also state a Default")
}

// TestTableValidateRejectsNullableIdentity proves that an identity column
// stating Nullable is rejected: both PostgreSQL and MySQL report
// is_nullable = NO for an identity column, so a descriptor claiming
// otherwise describes no real column.
func TestTableValidateRejectsNullableIdentity(t *testing.T) {
	table := validTable()
	table.Columns[1].Identity = schema.IdentityAlways
	table.Columns[1].Nullable = true

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "must not be Nullable")
}

// TestTableValidateAcceptsTextWidth is the positive counterpart to "text
// column with negative width" in TestTableValidate: a stated width of zero
// and an ordinary positive one both validate cleanly, and so does a column
// that never states one at all.
func TestTableValidateAcceptsTextWidth(t *testing.T) {
	table := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{Width: schema.NewTextWidth(255)}},
			{Name: "flag", Type: schema.TextType{Width: schema.NewTextWidth(0)}},
			{Name: "bio", Type: schema.TextType{}},
			{Name: "code", Type: schema.TextType{Width: schema.NewTextWidth(36), Fixed: true}},
		},
		PrimaryKey: []string{"id"},
	}

	require.NoError(t, table.Validate())
}

// TestColumnUnsignedJSON pins the snapshot form rasqlgen's -input reads. A
// descriptor makes that round trip as JSON, so signedness recorded in the
// integer type survives it.
func TestColumnUnsignedJSON(t *testing.T) {
	table := schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{Unsigned: true}},
			{Name: "sequence", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())

	encoded, err := json.Marshal(table)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Unsigned":true`)

	var decoded schema.TableDef
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, table, decoded)
	require.NoError(t, decoded.Validate())
	require.Equal(t, schema.IntegerType{Unsigned: true}, decoded.Columns[0].Type)
	require.Equal(t, schema.IntegerType{}, decoded.Columns[1].Type)

	// The old flat type representation is no longer accepted.
	decoded = schema.TableDef{}
	require.Error(t, json.Unmarshal([]byte(`{"Name":"events","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}`), &decoded))
}

// TestColumnIdentityJSON proves a column carrying Identity round-trips
// through JSON, and that a descriptor snapshot written before Identity
// existed -- carrying no "Identity" key at all -- decodes as the empty
// IdentityGeneration, the same backward-compatible read
// TestColumnUnsignedJSON pins for Unsigned.
func TestColumnIdentityJSON(t *testing.T) {
	table := schema.TableDef{
		Name: "events",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways},
			{Name: "external_id", Type: schema.IntegerType{}, Identity: schema.IdentityByDefault},
			{Name: "sequence", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())

	encoded, err := json.Marshal(table)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Identity":"ALWAYS"`)
	require.Contains(t, string(encoded), `"Identity":"BY DEFAULT"`)

	var decoded schema.TableDef
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, table, decoded)
	require.NoError(t, decoded.Validate())

	// A snapshot written before Identity existed carries no "Identity" key
	// at all, and decodes as the empty IdentityGeneration.
	var noIdentity schema.TableDef
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"events","Columns":[{"Name":"id","Type":{"Kind":"integer"}}],"PrimaryKey":["id"]}`), &noIdentity))
	require.Equal(t, schema.IdentityGeneration(""), noIdentity.Columns[0].Identity)
}

// TestBrandedSQLTextFieldsJSONRoundTrip pins the wire form of every
// sqltext.Text field that carries a SQL expression rather than a plain
// name: CheckDef.Expression, IndexDef.Predicate, IndexKeyDef.Expression,
// ExclusionDef.Predicate, and ExclusionElementDef.Expression. A defined
// string type marshals through encoding/json exactly like string, but this
// is the only test that actually proves it for these five fields rather
// than assuming it from ColumnDef.Default's marshaller alone.
func TestBrandedSQLTextFieldsJSONRoundTrip(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Checks: []schema.CheckDef{
			{Name: "chk_amount", Expression: "amount is not null"},
		},
		Indexes: []schema.IndexDef{{
			Name:      "idx_active_customer",
			Predicate: "amount is not null",
			Keys:      []schema.IndexKeyDef{{Expression: "lower(customer_id)"}},
		}},
		ExclusionConstraints: []schema.ExclusionDef{{
			Name:      "excl_customer_overlap",
			Elements:  []schema.ExclusionElementDef{{Expression: "customer_id", Operator: "="}},
			Predicate: "amount is not null",
		}},
	}
	require.NoError(t, table.Validate())

	cases := []struct {
		name string
		want string
	}{
		{"CheckDef.Expression", `"Expression":"amount is not null"`},
		{"IndexDef.Predicate", `"Predicate":"amount is not null"`},
		{"IndexKeyDef.Expression", `"Expression":"lower(customer_id)"`},
		{"ExclusionDef.Predicate", `"Predicate":"amount is not null"`},
		{"ExclusionElementDef.Expression", `"Expression":"customer_id"`},
	}

	encoded, err := json.Marshal(table)
	require.NoError(t, err)
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Contains(t, string(encoded), testCase.want)
		})
	}

	var decoded schema.TableDef
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, table, decoded)
}

// TestRowNameJSON pins the snapshot form rasqlgen's -input reads: a
// RowName round-trips through JSON, and a snapshot written before RowName
// existed decodes as the empty string rather than an error, the same
// backward-compatible read TestColumnUnsignedJSON pins for Unsigned.
func TestRowNameJSON(t *testing.T) {
	table := schema.TableDef{
		Name:    "users",
		RowName: "User",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())

	encoded, err := json.Marshal(table)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"RowName":"User"`)

	var decoded schema.TableDef
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, table, decoded)

	// A snapshot written before RowName existed omits it entirely, which
	// decodes as the empty string rather than an error.
	decoded = schema.TableDef{}
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"users","Columns":[{"Name":"id","Type":{"Kind":"integer"}}],"PrimaryKey":["id"]}`), &decoded))
	require.Empty(t, decoded.RowName)
}

func TestColumnTypeJSONRejectsIrrelevantOptions(t *testing.T) {
	var decoded schema.ColumnDef
	require.Error(t, json.Unmarshal([]byte(`{"Name":"name","Type":{"Kind":"text","Unsigned":true}}`), &decoded))
	require.Error(t, json.Unmarshal([]byte(`{"Name":"id","Type":{"Kind":"integer","Precision":4}}`), &decoded))
	require.Error(t, json.Unmarshal([]byte(`{"Name":"id","Type":{"Kind":"unknown"}}`), &decoded))
}

// TestTableValidateInvalidRowNameReportsPath pins the error path for a
// RowName that is not a valid, exported Go identifier. RowName names a Go
// type rather than a SQL identifier, so this covers cases ValidateIdentifier
// alone would not catch: a lowercase (unexported) name and a Go keyword,
// alongside a name starting with a digit, a space, and a hyphen.
func TestTableValidateInvalidRowNameReportsPath(t *testing.T) {
	tests := map[string]string{
		"starts with a digit": "1bad",
		"unexported":          "order",
		"go keyword":          "type",
		"contains a space":    "Order Row",
		"contains a hyphen":   "Order-Row",
	}

	for name, rowName := range tests {
		t.Run(name, func(t *testing.T) {
			table := schema.TableDef{
				Name:    "orders",
				RowName: rowName,
				Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
			}

			err := table.Validate()
			require.Error(t, err)
			require.ErrorContains(t, err, "table.row_name")
		})
	}
}

func TestTableValidateDuplicateConstraintNameReportsPath(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		UniqueConstraints: []schema.UniqueDef{{
			Name:    "dup",
			Columns: []string{"id"},
		}},
		Checks: []schema.CheckDef{{
			Name:       "dup",
			Expression: "id > 0",
		}},
	}

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "checks[0].name")
	require.ErrorContains(t, err, "duplicates constraint")
}

func TestTableValidateAllowsRepeatedEmptyConstraintNames(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "org_id", Type: schema.IntegerType{}},
		},
		UniqueConstraints: []schema.UniqueDef{{
			Columns: []string{"id"},
		}},
		Checks: []schema.CheckDef{{
			Expression: "id > 0",
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"org_id"},
			ReferencedTable:   "orgs",
			ReferencedColumns: []string{"id"},
		}},
	}

	require.NoError(t, table.Validate())
}

func TestTableValidatesRelationshipMetadata(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
		},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_id_fkey",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
		}},
		Relationships: []schema.RelationshipDef{{
			Name:              "Customer",
			Kind:              schema.RelationshipBelongsTo,
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
		}},
	}
	require.NoError(t, table.Validate())

	table.Relationships[0].Columns = []string{"missing"}
	err := table.Validate()
	require.ErrorContains(t, err, "relationships[0].columns[0]")
}

func TestTableRejectsRelationshipWithoutMatchingForeignKey(t *testing.T) {
	table := schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "other_id", Type: schema.IntegerType{}},
		},
		ForeignKeys: []schema.ForeignKeyDef{{
			Columns:           []string{"user_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
		Relationships: []schema.RelationshipDef{{
			Name:              "User",
			Kind:              schema.RelationshipBelongsTo,
			Columns:           []string{"other_id"},
			ReferencedTable:   "users",
			ReferencedColumns: []string{"id"},
		}},
	}

	err := table.Validate()
	require.ErrorContains(t, err, "relationships[0]: does not match a declared foreign key")
}

func TestValidateIdentifier(t *testing.T) {
	require.NoError(t, schema.ValidateIdentifier("customer_42"))
	require.Error(t, schema.ValidateIdentifier("42_customer"))
	require.Error(t, schema.ValidateIdentifier("customer-id"))
	// A dotted name must stay rejected: schema qualification carries the
	// namespace in a separate field rather than a dotted string, and this
	// pins that a future change cannot weaken the rule to allow one.
	require.ErrorContains(t, schema.ValidateIdentifier("audit.events"), "invalid character")
}

// TestTableValidatesSchemaQualifier pins the validation rule added for the
// optional Schema field: an empty Schema is skipped, a valid identifier
// passes, and an invalid one reports table.schema.
func TestTableValidatesSchemaQualifier(t *testing.T) {
	base := func(schemaName string) schema.TableDef {
		table := validTable()
		table.Schema = schemaName
		return table
	}

	require.NoError(t, base("").Validate())
	require.NoError(t, base("audit").Validate())

	err := base("audit.events").Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "table.schema")

	err = base("1bad").Validate()
	require.Error(t, err)
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "table.schema")
}

// TestTableValidatesForeignKeyReferencedSchema covers ForeignKey.ReferencedSchema:
// empty stays valid, exactly like Table.Schema, and an invalid identifier is
// reported at foreign_keys[0].referenced_schema.
func TestTableValidatesForeignKeyReferencedSchema(t *testing.T) {
	base := func(referencedSchema string) schema.TableDef {
		table := validTable()
		table.ForeignKeys[0].ReferencedSchema = referencedSchema
		return table
	}

	require.NoError(t, base("").Validate())
	require.NoError(t, base("tenant").Validate())

	err := base("tenant.customers").Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "foreign_keys[0].referenced_schema")

	err = base("1bad").Validate()
	require.Error(t, err)
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "foreign_keys[0].referenced_schema")
}

// TestTableValidateAcceptsForeignKeyMatchAndDeferrability proves that a
// ForeignKeyDef naming a non-default schema.MatchType or
// schema.Deferrability, such as what inspect now records for a live
// PostgreSQL MATCH FULL or DEFERRABLE foreign key, is valid input: Validate
// describes the foreign key, and only render.CreateTable and the migrate
// diff-live path refuse to build DDL for it.
func TestTableValidateAcceptsForeignKeyMatchAndDeferrability(t *testing.T) {
	table := validTable()
	table.ForeignKeys[0].Match = schema.MatchPartial
	table.ForeignKeys[0].Deferrable = schema.DeferrableInitiallyDeferred
	require.NoError(t, table.Validate())
}

func TestTableValidatesForeignKeyMatch(t *testing.T) {
	table := validTable()
	table.ForeignKeys[0].Match = schema.MatchType("bogus")

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "foreign_keys[0].match")
}

func TestTableValidatesForeignKeyDeferrability(t *testing.T) {
	table := validTable()
	table.ForeignKeys[0].Deferrable = schema.Deferrability("bogus")

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "foreign_keys[0].deferrable")
}

// TestTableValidateAcceptsExclusionConstraint proves that an ExclusionDef
// naming a non-default Method, several Elements, a Predicate, and a
// Deferrable, such as what inspect now records for a live PostgreSQL
// EXCLUDE constraint, is valid input: Validate describes the constraint,
// and only render.CreateTable and the migrate diff-live path refuse to
// build DDL for it.
func TestTableValidateAcceptsExclusionConstraint(t *testing.T) {
	table := validTable()
	table.ExclusionConstraints = []schema.ExclusionDef{{
		Name:   "orders_no_overlap",
		Method: "gist",
		Elements: []schema.ExclusionElementDef{
			{Expression: "customer_id", Operator: "="},
			{Expression: "status", Operator: "<>"},
		},
		Predicate:  "status <> 'cancelled'",
		Deferrable: schema.DeferrableInitiallyDeferred,
	}}
	require.NoError(t, table.Validate())
}

func TestTableValidatesExclusionConstraintRequiresElements(t *testing.T) {
	table := validTable()
	table.ExclusionConstraints = []schema.ExclusionDef{{Name: "orders_no_overlap"}}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "exclusion_constraints[0].elements")
}

func TestTableValidatesExclusionConstraintElementExpression(t *testing.T) {
	table := validTable()
	table.ExclusionConstraints = []schema.ExclusionDef{{
		Name:     "orders_no_overlap",
		Elements: []schema.ExclusionElementDef{{Operator: "="}},
	}}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "exclusion_constraints[0].elements[0].expression")
}

func TestTableValidatesExclusionConstraintElementOperator(t *testing.T) {
	table := validTable()
	table.ExclusionConstraints = []schema.ExclusionDef{{
		Name:     "orders_no_overlap",
		Elements: []schema.ExclusionElementDef{{Expression: "customer_id"}},
	}}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "exclusion_constraints[0].elements[0].operator")
}

func TestTableValidatesExclusionConstraintDeferrability(t *testing.T) {
	table := validTable()
	table.ExclusionConstraints = []schema.ExclusionDef{{
		Name:       "orders_no_overlap",
		Elements:   []schema.ExclusionElementDef{{Expression: "customer_id", Operator: "="}},
		Deferrable: schema.Deferrability("bogus"),
	}}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "exclusion_constraints[0].deferrable")
}

func TestTableValidatesExclusionConstraintDuplicateName(t *testing.T) {
	table := validTable()
	table.ExclusionConstraints = []schema.ExclusionDef{{
		Name:     table.Checks[0].Name,
		Elements: []schema.ExclusionElementDef{{Expression: "customer_id", Operator: "="}},
	}}

	err := table.Validate()
	require.Error(t, err)
	require.ErrorContains(t, err, "exclusion_constraints[0].name")
	require.ErrorContains(t, err, "duplicates constraint")
}

// TestTableValidateAcceptsCheckValidationFacts proves that a CheckDef naming
// NoInherit, NotValid, or NotEnforced, such as what inspect now records for
// a live PostgreSQL NO INHERIT, NOT VALID, or NOT ENFORCED check
// constraint, is valid input: Validate describes the check constraint, and
// only render.CreateTable and the migrate diff-live path refuse to build
// DDL for it.
func TestTableValidateAcceptsCheckValidationFacts(t *testing.T) {
	table := validTable()
	table.Checks[0].NoInherit = true
	table.Checks[0].NotValid = true
	table.Checks[0].NotEnforced = true
	require.NoError(t, table.Validate())
}

// TestTableValidateAcceptsForeignKeyValidationFacts is the foreign-key
// counterpart to TestTableValidateAcceptsCheckValidationFacts.
func TestTableValidateAcceptsForeignKeyValidationFacts(t *testing.T) {
	table := validTable()
	table.ForeignKeys[0].NotValid = true
	table.ForeignKeys[0].NotEnforced = true
	require.NoError(t, table.Validate())
}

// TestTableValidateAcceptsForeignKeyTemporal proves that Validate accepts a
// ForeignKeyDef.Temporal foreign key.
func TestTableValidateAcceptsForeignKeyTemporal(t *testing.T) {
	table := validTable()
	table.ForeignKeys[0].Temporal = true
	require.NoError(t, table.Validate())
}

// TestTableValidateAcceptsForeignKeyDeleteSetColumns proves that Validate
// accepts a ForeignKeyDef.DeleteSetColumns naming a subset of the foreign
// key's own Columns when OnDelete is SetNull.
func TestTableValidateAcceptsForeignKeyDeleteSetColumns(t *testing.T) {
	table := validTable()
	table.ForeignKeys[0].OnDelete = schema.SetNull
	table.ForeignKeys[0].DeleteSetColumns = []string{"customer_id"}
	require.NoError(t, table.Validate())
}

// TestTableValidatesForeignKeyDeleteSetColumnsRequiresSetAction proves that
// Validate rejects DeleteSetColumns unless OnDelete is SetNull or
// SetDefault: a live PostgreSQL ON DELETE SET NULL/SET DEFAULT column list
// is meaningless on any other delete action.
func TestTableValidatesForeignKeyDeleteSetColumnsRequiresSetAction(t *testing.T) {
	table := validTable()
	table.ForeignKeys[0].DeleteSetColumns = []string{"customer_id"}

	err := table.Validate()
	require.ErrorContains(t, err, "foreign_keys[0].delete_set_columns")
	require.ErrorContains(t, err, "must not be set unless OnDelete is SetNull or SetDefault")
}

// TestTableValidatesForeignKeyDeleteSetColumnsUnknownColumn proves that
// Validate rejects a DeleteSetColumns entry naming a column outside the
// foreign key's own Columns.
func TestTableValidatesForeignKeyDeleteSetColumnsUnknownColumn(t *testing.T) {
	table := validTable()
	table.ForeignKeys[0].OnDelete = schema.SetNull
	table.ForeignKeys[0].DeleteSetColumns = []string{"amount"}

	err := table.Validate()
	require.ErrorContains(t, err, "foreign_keys[0].delete_set_columns[0]")
	require.ErrorContains(t, err, `names column "amount", which is not part of the foreign key`)
}

// TestTableValidatesForeignKeyDeleteSetColumnsDuplicate proves that
// Validate rejects a DeleteSetColumns entry repeated within the same
// foreign key.
func TestTableValidatesForeignKeyDeleteSetColumnsDuplicate(t *testing.T) {
	table := validTable()
	table.ForeignKeys[0].OnDelete = schema.SetNull
	table.ForeignKeys[0].DeleteSetColumns = []string{"customer_id", "customer_id"}

	err := table.Validate()
	require.ErrorContains(t, err, "foreign_keys[0].delete_set_columns[1]")
	require.ErrorContains(t, err, `duplicates column "customer_id"`)
}

// TestTableValidateAcceptsUniqueConstraintFacts proves that a UniqueDef
// naming a non-default schema.Deferrability, NullsNotDistinct, non-empty
// IncludeColumns, or a non-default schema.ConflictResolution, such as what
// inspect now records for a live PostgreSQL deferrable, NULLS NOT
// DISTINCT, or INCLUDE constraint, or a live SQLite ON CONFLICT clause, is
// valid input: Validate describes the constraint, and only
// render.CreateTable and the migrate diff-live path refuse to build DDL
// for it.
func TestTableValidateAcceptsUniqueConstraintFacts(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].Deferrable = schema.DeferrableInitiallyDeferred
	table.UniqueConstraints[0].NullsNotDistinct = true
	table.UniqueConstraints[0].IncludeColumns = []string{"id"}
	table.UniqueConstraints[0].OnConflict = schema.ConflictReplace
	require.NoError(t, table.Validate())
}

func TestTableValidatesUniqueConstraintDeferrability(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].Deferrable = schema.Deferrability("bogus")

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "unique_constraints[0].deferrable")
}

func TestTableValidatesUniqueConstraintOnConflict(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].OnConflict = schema.ConflictResolution("bogus")

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "unique_constraints[0].on_conflict")
}

// TestTableValidatesUniqueConstraintIncludeColumnsUnknownColumn proves that
// an IncludeColumns entry naming a column the table does not declare is
// reported at the include_columns element, not the constraint's own
// columns.
func TestTableValidatesUniqueConstraintIncludeColumnsUnknownColumn(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].IncludeColumns = []string{"bogus"}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "unique_constraints[0].include_columns[0]")
}

// TestTableValidatesUniqueConstraintIncludeColumnsOverlapsColumns proves
// that a column already in the constraint's own Columns cannot also appear
// in IncludeColumns: a column cannot both key the constraint and be along
// for the ride.
func TestTableValidatesUniqueConstraintIncludeColumnsOverlapsColumns(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].IncludeColumns = []string{"customer_id"}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "unique_constraints[0].include_columns[0]")
	require.ErrorContains(t, err, `duplicates column "customer_id"`)
}

// TestTableValidatesUniqueConstraintIncludeColumnsDuplicate proves that
// IncludeColumns itself cannot repeat a column.
func TestTableValidatesUniqueConstraintIncludeColumnsDuplicate(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].IncludeColumns = []string{"id", "id"}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "unique_constraints[0].include_columns[1]")
	require.ErrorContains(t, err, `duplicates column "id"`)
}

// TestTableValidateAcceptsUniqueConstraintBackingIndexFacts proves that
// Validate accepts a UniqueDef's Temporal, StorageParameters, Tablespace,
// ReplicaIdentity, and Collations facts together.
func TestTableValidateAcceptsUniqueConstraintBackingIndexFacts(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].Temporal = true
	table.UniqueConstraints[0].StorageParameters = map[string]string{"fillfactor": "70"}
	table.UniqueConstraints[0].Tablespace = "pg_custom"
	table.UniqueConstraints[0].ReplicaIdentity = true
	table.UniqueConstraints[0].Collations = map[string]string{"customer_id": "C"}

	require.NoError(t, table.Validate())
}

// TestTableValidatesUniqueConstraintStorageParameterEmptyKey proves that
// Validate rejects an empty StorageParameters key, the same terms as
// IndexDef.StorageParameters.
func TestTableValidatesUniqueConstraintStorageParameterEmptyKey(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].StorageParameters = map[string]string{"": "70"}

	err := table.Validate()
	require.ErrorContains(t, err, "unique_constraints[0].storage_parameters")
	require.ErrorContains(t, err, "must not have an empty key")
}

// TestTableValidatesUniqueConstraintCollationsUnknownColumn proves that
// Validate rejects a Collations key naming a column outside the
// constraint's own Columns: a live PostgreSQL unique constraint's backing
// index only ever carries a per-column collation for the constraint's own
// columns.
func TestTableValidatesUniqueConstraintCollationsUnknownColumn(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].Collations = map[string]string{"amount": "C"}

	err := table.Validate()
	require.ErrorContains(t, err, "unique_constraints[0].collations")
	require.ErrorContains(t, err, `names column "amount", which is not part of the constraint`)
}

// TestTableValidatesUniqueConstraintCollationsEmptyValue proves that
// Validate rejects a Collations entry with an empty collation name.
func TestTableValidatesUniqueConstraintCollationsEmptyValue(t *testing.T) {
	table := validTable()
	table.UniqueConstraints[0].Collations = map[string]string{"customer_id": ""}

	err := table.Validate()
	require.ErrorContains(t, err, "unique_constraints[0].collations")
	require.ErrorContains(t, err, `must not have an empty value for column "customer_id"`)
}

// TestTableValidateAcceptsSQLiteTableOptions proves that Strict,
// WithoutRowID, PrimaryKeyAutoincrement, and PrimaryKeyOnConflict are all
// accepted together on a table that has a primary key.
func TestTableValidateAcceptsSQLiteTableOptions(t *testing.T) {
	table := validTable()
	table.Strict = true
	table.WithoutRowID = true
	table.PrimaryKeyAutoincrement = true
	table.PrimaryKeyOnConflict = schema.ConflictReplace
	require.NoError(t, table.Validate())
}

func TestTableValidatesPrimaryKeyOnConflict(t *testing.T) {
	table := validTable()
	table.PrimaryKeyOnConflict = schema.ConflictResolution("bogus")

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "table.primary_key_on_conflict")
}

// TestTableValidatesPrimaryKeyAutoincrementWithoutPrimaryKey proves that
// PrimaryKeyAutoincrement is nonsense on a table with no primary key at
// all, so Validate rejects it instead of quietly accepting a fact that
// names nothing.
func TestTableValidatesPrimaryKeyAutoincrementWithoutPrimaryKey(t *testing.T) {
	table := validTable()
	table.PrimaryKey = nil
	table.PrimaryKeyAutoincrement = true

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "table.primary_key_autoincrement")
	require.ErrorContains(t, err, "must not be set without a primary key")
}

// TestTableValidatesPrimaryKeyOnConflictWithoutPrimaryKey is the
// PrimaryKeyOnConflict counterpart to
// TestTableValidatesPrimaryKeyAutoincrementWithoutPrimaryKey.
func TestTableValidatesPrimaryKeyOnConflictWithoutPrimaryKey(t *testing.T) {
	table := validTable()
	table.PrimaryKey = nil
	table.PrimaryKeyOnConflict = schema.ConflictReplace

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "table.primary_key_on_conflict")
	require.ErrorContains(t, err, "must not be set without a primary key")
}

// TestTableValidateAcceptsVirtualTable proves that a table naming
// VirtualTableModule and VirtualTableModuleArguments, and a hidden column,
// is accepted, provided it states none of the ordinary-table facts a
// virtual table cannot independently carry.
func TestTableValidateAcceptsVirtualTable(t *testing.T) {
	table := schema.TableDef{
		Name: "posts_fts",
		Columns: []schema.ColumnDef{
			{Name: "body", Type: schema.TextType{}, Nullable: true},
			{Name: "posts_fts", Type: schema.TextType{}, Nullable: true, Hidden: true},
			{Name: "rank", Type: schema.TextType{}, Nullable: true, Hidden: true},
		},
		VirtualTableModule:          "fts5",
		VirtualTableModuleArguments: []string{"body"},
	}
	require.NoError(t, table.Validate())
}

// TestTableValidatesHiddenColumnWithoutVirtualTableModule proves that
// ColumnDef.Hidden is nonsense on a table that is not a SQLite virtual
// table.
func TestTableValidatesHiddenColumnWithoutVirtualTableModule(t *testing.T) {
	table := validTable()
	table.Columns[0].Hidden = true

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "columns[0].hidden")
	require.ErrorContains(t, err, "must not be set without a VirtualTableModule")
}

// TestTableValidatesVirtualTableModuleArgumentsWithoutModule proves that
// VirtualTableModuleArguments is nonsense without a VirtualTableModule.
func TestTableValidatesVirtualTableModuleArgumentsWithoutModule(t *testing.T) {
	table := validTable()
	table.VirtualTableModuleArguments = []string{"body"}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "table.virtual_table_module_arguments")
	require.ErrorContains(t, err, "must not be set without a VirtualTableModule")
}

// TestTableValidatesVirtualTableModuleWithPrimaryKey proves that a virtual
// table cannot also state a primary key, since a virtual table's own
// primary key, if any, is the module's business, not something this
// package's PRAGMA-based read can independently verify.
func TestTableValidatesVirtualTableModuleWithPrimaryKey(t *testing.T) {
	table := schema.TableDef{
		Name:               "posts_fts",
		Columns:            []schema.ColumnDef{{Name: "body", Type: schema.TextType{}, Nullable: true}},
		PrimaryKey:         []string{"body"},
		VirtualTableModule: "fts5",
	}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "table.primary_key")
	require.ErrorContains(t, err, "must be empty on a SQLite virtual table")
}

// TestTableValidatesVirtualTableModuleWithUniqueConstraint proves that a
// virtual table cannot also carry a unique constraint, the same rejection
// TestTableValidatesVirtualTableModuleWithPrimaryKey proves for a primary
// key.
func TestTableValidatesVirtualTableModuleWithUniqueConstraint(t *testing.T) {
	table := schema.TableDef{
		Name:               "posts_fts",
		Columns:            []schema.ColumnDef{{Name: "body", Type: schema.TextType{}, Nullable: true}},
		VirtualTableModule: "fts5",
		UniqueConstraints:  []schema.UniqueDef{{Columns: []string{"body"}}},
	}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "table.unique_constraints")
	require.ErrorContains(t, err, "must be empty on a SQLite virtual table")
}

// TestTableValidateAcceptsUniqueConstraintKeys proves that a UniqueDef
// naming Keys instead of Columns is accepted.
func TestTableValidateAcceptsUniqueConstraintKeys(t *testing.T) {
	table := validTable()
	table.UniqueConstraints = []schema.UniqueDef{{
		Keys: []schema.IndexKeyDef{
			{Expression: "customer_id", Descending: true},
			{Expression: "status", Collation: "NOCASE"},
		},
	}}
	require.NoError(t, table.Validate())
}

// TestTableValidatesUniqueConstraintKeysWithColumns proves that a UniqueDef
// cannot name both Columns and Keys, the same either/or validateIndexes
// already enforces between IndexDef.Columns and IndexDef.Keys.
func TestTableValidatesUniqueConstraintKeysWithColumns(t *testing.T) {
	table := validTable()
	table.UniqueConstraints = []schema.UniqueDef{{
		Columns: []string{"customer_id"},
		Keys:    []schema.IndexKeyDef{{Expression: "customer_id", Descending: true}},
	}}

	err := table.Validate()
	require.Error(t, err)
	var validationErr *schema.ValidationError
	require.True(t, errors.As(err, &validationErr))
	require.ErrorContains(t, err, "unique_constraints[0].columns")
	require.ErrorContains(t, err, "must be empty when Keys describes the constraint's keys instead")
}

// TestTableQualifiedName pins QualifiedName and Qualified for both an
// unqualified and a qualified table.
func TestTableQualifiedName(t *testing.T) {
	unqualified := schema.TableDef{Name: "users"}
	require.False(t, unqualified.Qualified())
	require.Equal(t, "users", unqualified.QualifiedName())

	qualified := schema.TableDef{Schema: "audit", Name: "events"}
	require.True(t, qualified.Qualified())
	require.Equal(t, "audit.events", qualified.QualifiedName())
}

func validTable() schema.TableDef {
	return schema.TableDef{
		Name: "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "customer_id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.TextType{}, Default: "'new'"},
			{Name: "amount", Type: schema.DecimalType{Precision: 19, Scale: schema.NewDecimalScale(4)}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{{
			Name:    "orders_customer_status_key",
			Columns: []string{"customer_id", "status"},
		}},
		Checks: []schema.CheckDef{{
			Name:       "orders_status_check",
			Expression: "status <> ''",
		}},
		Indexes: []schema.IndexDef{{
			Name:    "orders_customer_idx",
			Columns: []string{"customer_id"},
		}},
		ForeignKeys: []schema.ForeignKeyDef{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.Cascade,
		}},
	}
}
