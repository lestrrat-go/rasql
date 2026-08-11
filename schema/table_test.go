package schema_test

import (
	"encoding/json"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestTableCloneCopiesDescriptor(t *testing.T) {
	descriptor := validTable()
	descriptor.Schema = "audit"
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
	descriptor.Relationships[0].Columns[0] = "changed"
	descriptor.Schema = "changed"

	require.Equal(t, "audit", table.Schema)
	require.Equal(t, "id", table.Columns[0].Name)
	require.Equal(t, "id", table.PrimaryKey[0])
	require.Equal(t, "customer_id", table.Indexes[0].Columns[0])
	require.Equal(t, "id", table.ForeignKeys[0].ReferencedColumns[0])
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
	require.Contains(t, string(encoded), `"Type":{"Kind":"decimal","Precision":19,"Scale":0}`)

	encoded, err = json.Marshal(schema.ColumnDef{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Type":{"Kind":"integer","Unsigned":false}`)

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

func TestColumnTypeJSONRejectsIrrelevantOptions(t *testing.T) {
	var decoded schema.ColumnDef
	require.Error(t, json.Unmarshal([]byte(`{"Name":"name","Type":{"Kind":"text","Unsigned":true}}`), &decoded))
	require.Error(t, json.Unmarshal([]byte(`{"Name":"id","Type":{"Kind":"integer","Precision":4}}`), &decoded))
	require.Error(t, json.Unmarshal([]byte(`{"Name":"id","Type":{"Kind":"unknown"}}`), &decoded))
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
