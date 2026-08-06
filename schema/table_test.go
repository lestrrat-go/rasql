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
	table := descriptor.Clone()

	descriptor.Columns[0].Name = "changed"
	descriptor.PrimaryKey[0] = "changed"
	descriptor.Indexes[0].Columns[0] = "changed"
	descriptor.ForeignKeys[0].ReferencedColumns[0] = "changed"

	require.Equal(t, "id", table.Columns[0].Name)
	require.Equal(t, "id", table.PrimaryKey[0])
	require.Equal(t, "customer_id", table.Indexes[0].Columns[0])
	require.Equal(t, "id", table.ForeignKeys[0].ReferencedColumns[0])

	amount, ok := table.Column("amount")
	require.True(t, ok)
	require.Equal(t, 19, amount.Precision)
	scale, stated := amount.Scale.Value()
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

// TestDecimalScaleJSON pins the snapshot form rasqlgen's -input reads: a
// stated scale is a plain JSON number, and an unstated one is null rather than
// a number that would decode back as a stated scale of zero.
func TestDecimalScaleJSON(t *testing.T) {
	encoded, err := json.Marshal(schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: schema.NewDecimalScale(0)})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Scale":0`)

	encoded, err = json.Marshal(schema.Column{Name: "id", Type: schema.TypeInteger})
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Scale":null`)

	var decoded schema.Column
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"amount","Type":"decimal","Precision":19,"Scale":0}`), &decoded))
	scale, stated := decoded.Scale.Value()
	require.True(t, stated)
	require.Equal(t, 0, scale)

	decoded = schema.Column{}
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"amount","Type":"decimal","Precision":19}`), &decoded))
	_, stated = decoded.Scale.Value()
	require.False(t, stated)

	decoded = schema.Column{}
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"amount","Type":"decimal","Precision":19,"Scale":null}`), &decoded))
	_, stated = decoded.Scale.Value()
	require.False(t, stated)

	require.Error(t, json.Unmarshal([]byte(`{"Scale":"four"}`), &decoded))
}

func TestTableColumn(t *testing.T) {
	table := validTable()
	require.NoError(t, table.Validate())

	column, ok := table.Column("customer_id")
	require.True(t, ok)
	require.Equal(t, schema.TypeInteger, column.Type)

	_, ok = table.Column("missing")
	require.False(t, ok)
}

func TestTableValidate(t *testing.T) {
	tests := map[string]schema.Table{
		"empty table name": {
			Columns: []schema.Column{{Name: "id", Type: schema.TypeInteger}},
		},
		"invalid column type": {
			Name:    "orders",
			Columns: []schema.Column{{Name: "id", Type: "money"}},
		},
		"duplicate column": {
			Name: "orders",
			Columns: []schema.Column{
				{Name: "id", Type: schema.TypeInteger},
				{Name: "id", Type: schema.TypeInteger},
			},
		},
		"unknown primary key column": {
			Name:       "orders",
			Columns:    []schema.Column{{Name: "id", Type: schema.TypeInteger}},
			PrimaryKey: []string{"missing"},
		},
		"empty index name": {
			Name:    "orders",
			Columns: []schema.Column{{Name: "id", Type: schema.TypeInteger}},
			Indexes: []schema.Index{{Columns: []string{"id"}}},
		},
		"foreign key arity": {
			Name: "orders",
			Columns: []schema.Column{
				{Name: "id", Type: schema.TypeInteger},
				{Name: "customer_id", Type: schema.TypeInteger},
			},
			ForeignKeys: []schema.ForeignKey{{
				Columns:           []string{"id", "customer_id"},
				ReferencedTable:   "customers",
				ReferencedColumns: []string{"id"},
			}},
		},
		"unique constraint name duplicates check name": {
			Name: "orders",
			Columns: []schema.Column{
				{Name: "id", Type: schema.TypeInteger},
			},
			UniqueConstraints: []schema.UniqueConstraint{{
				Name:    "dup",
				Columns: []string{"id"},
			}},
			Checks: []schema.CheckConstraint{{
				Name:       "dup",
				Expression: "id > 0",
			}},
		},
		"unique constraint name duplicates foreign key name": {
			Name: "orders",
			Columns: []schema.Column{
				{Name: "id", Type: schema.TypeInteger},
				{Name: "org_id", Type: schema.TypeInteger},
			},
			UniqueConstraints: []schema.UniqueConstraint{{
				Name:    "dup",
				Columns: []string{"id"},
			}},
			ForeignKeys: []schema.ForeignKey{{
				Name:              "dup",
				Columns:           []string{"org_id"},
				ReferencedTable:   "orgs",
				ReferencedColumns: []string{"id"},
			}},
		},
		"check name duplicates foreign key name": {
			Name: "orders",
			Columns: []schema.Column{
				{Name: "id", Type: schema.TypeInteger},
				{Name: "org_id", Type: schema.TypeInteger},
			},
			Checks: []schema.CheckConstraint{{
				Name:       "dup",
				Expression: "id > 0",
			}},
			ForeignKeys: []schema.ForeignKey{{
				Name:              "dup",
				Columns:           []string{"org_id"},
				ReferencedTable:   "orgs",
				ReferencedColumns: []string{"id"},
			}},
		},
		"decimal column with zero precision": {
			Name:    "payments",
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeDecimal, Precision: 0, Scale: schema.NewDecimalScale(0)}},
		},
		"decimal column with negative scale": {
			Name:    "payments",
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeDecimal, Precision: 4, Scale: schema.NewDecimalScale(-1)}},
		},
		"decimal scale exceeds precision": {
			Name:    "payments",
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeDecimal, Precision: 4, Scale: schema.NewDecimalScale(5)}},
		},
		"decimal column with no scale": {
			Name:    "payments",
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeDecimal, Precision: 19}},
		},
		"precision on non-decimal column": {
			Name:    "payments",
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeText, Precision: 3}},
		},
		"scale on non-decimal column": {
			Name:    "payments",
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeText, Scale: schema.NewDecimalScale(0)}},
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
		column  schema.Column
		wantErr string
	}{
		"zero precision": {
			column:  schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 0, Scale: schema.NewDecimalScale(0)},
			wantErr: "must state a precision of at least 1",
		},
		"negative scale": {
			column:  schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 4, Scale: schema.NewDecimalScale(-1)},
			wantErr: "must not be negative",
		},
		"scale exceeds precision": {
			column:  schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 4, Scale: schema.NewDecimalScale(5)},
			wantErr: "scale 5 exceeds precision 4",
		},
		"no scale at all": {
			column:  schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 19},
			wantErr: "must state a scale",
		},
		"precision on non-decimal column": {
			column:  schema.Column{Name: "amount", Type: schema.TypeText, Precision: 3},
			wantErr: "apply only to a decimal column",
		},
		"scale on non-decimal column": {
			column:  schema.Column{Name: "amount", Type: schema.TypeText, Scale: schema.NewDecimalScale(0)},
			wantErr: "apply only to a decimal column",
		},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			table := schema.Table{Name: "payments", Columns: []schema.Column{test.column}}
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
	table := schema.Table{
		Name: "payments",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: schema.NewDecimalScale(4)},
			{Name: "quantity", Type: schema.TypeDecimal, Precision: 10, Scale: schema.NewDecimalScale(0)},
		},
		PrimaryKey: []string{"id"},
	}

	require.NoError(t, table.Validate())
}

// TestTableValidateUnsignedColumn covers both sides of the rule Unsigned
// carries: an integer column may state it, and every other logical type is
// rejected, since no other logical type has a signedness for a dialect to
// render.
func TestTableValidateUnsignedColumn(t *testing.T) {
	table := schema.Table{
		Name: "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger, Unsigned: true},
			{Name: "sequence", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())

	for _, logicalType := range []schema.LogicalType{schema.TypeText, schema.TypeFloat, schema.TypeBoolean} {
		t.Run(string(logicalType), func(t *testing.T) {
			rejected := schema.Table{
				Name:    "events",
				Columns: []schema.Column{{Name: "id", Type: logicalType, Unsigned: true}},
			}
			err := rejected.Validate()
			require.ErrorContains(t, err, "columns[0].unsigned")
			require.ErrorContains(t, err, "unsigned applies only to an integer column")
		})
	}

	// A decimal column carries its own precision and scale, and states no
	// signedness even so.
	decimal := schema.Table{
		Name:    "payments",
		Columns: []schema.Column{{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: schema.NewDecimalScale(4), Unsigned: true}},
	}
	require.ErrorContains(t, decimal.Validate(), "unsigned applies only to an integer column")
}

// TestColumnUnsignedJSON pins the snapshot form rasqlgen's -input reads. A
// descriptor makes that round trip as JSON, so signedness recorded in an
// exported bool survives it; state kept unexported would be dropped there and
// the column would silently read back signed.
func TestColumnUnsignedJSON(t *testing.T) {
	table := schema.Table{
		Name: "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger, Unsigned: true},
			{Name: "sequence", Type: schema.TypeInteger},
		},
		PrimaryKey: []string{"id"},
	}
	require.NoError(t, table.Validate())

	encoded, err := json.Marshal(table)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"Unsigned":true`)

	var decoded schema.Table
	require.NoError(t, json.Unmarshal(encoded, &decoded))
	require.Equal(t, table, decoded)
	require.NoError(t, decoded.Validate())
	require.True(t, decoded.Columns[0].Unsigned)
	require.False(t, decoded.Columns[1].Unsigned)

	// A snapshot written before columns carried signedness names no Unsigned
	// at all, and decodes as the signed column it described.
	decoded = schema.Table{}
	require.NoError(t, json.Unmarshal([]byte(`{"Name":"events","Columns":[{"Name":"id","Type":"integer"}],"PrimaryKey":["id"]}`), &decoded))
	require.False(t, decoded.Columns[0].Unsigned)
}

func TestTableValidateDuplicateConstraintNameReportsPath(t *testing.T) {
	table := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
		},
		UniqueConstraints: []schema.UniqueConstraint{{
			Name:    "dup",
			Columns: []string{"id"},
		}},
		Checks: []schema.CheckConstraint{{
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
	table := schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "org_id", Type: schema.TypeInteger},
		},
		UniqueConstraints: []schema.UniqueConstraint{{
			Columns: []string{"id"},
		}},
		Checks: []schema.CheckConstraint{{
			Expression: "id > 0",
		}},
		ForeignKeys: []schema.ForeignKey{{
			Columns:           []string{"org_id"},
			ReferencedTable:   "orgs",
			ReferencedColumns: []string{"id"},
		}},
	}

	require.NoError(t, table.Validate())
}

func TestValidateIdentifier(t *testing.T) {
	require.NoError(t, schema.ValidateIdentifier("customer_42"))
	require.Error(t, schema.ValidateIdentifier("42_customer"))
	require.Error(t, schema.ValidateIdentifier("customer-id"))
}

func validTable() schema.Table {
	return schema.Table{
		Name: "orders",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "customer_id", Type: schema.TypeInteger},
			{Name: "status", Type: schema.TypeText, Default: "'new'"},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: schema.NewDecimalScale(4)},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueConstraint{{
			Name:    "orders_customer_status_key",
			Columns: []string{"customer_id", "status"},
		}},
		Checks: []schema.CheckConstraint{{
			Name:       "orders_status_check",
			Expression: "status <> ''",
		}},
		Indexes: []schema.Index{{
			Name:    "orders_customer_idx",
			Columns: []string{"customer_id"},
		}},
		ForeignKeys: []schema.ForeignKey{{
			Name:              "orders_customer_fk",
			Columns:           []string{"customer_id"},
			ReferencedTable:   "customers",
			ReferencedColumns: []string{"id"},
			OnDelete:          schema.ReferenceActionCascade,
		}},
	}
}
