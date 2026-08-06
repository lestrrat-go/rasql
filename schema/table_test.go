package schema_test

import (
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
	require.Equal(t, 4, amount.Scale)
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
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeDecimal, Precision: 0, Scale: 0}},
		},
		"decimal column with negative scale": {
			Name:    "payments",
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeDecimal, Precision: 4, Scale: -1}},
		},
		"decimal scale exceeds precision": {
			Name:    "payments",
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeDecimal, Precision: 4, Scale: 5}},
		},
		"precision on non-decimal column": {
			Name:    "payments",
			Columns: []schema.Column{{Name: "amount", Type: schema.TypeText, Precision: 3}},
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
			column:  schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 0, Scale: 0},
			wantErr: "must state a precision of at least 1",
		},
		"negative scale": {
			column:  schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 4, Scale: -1},
			wantErr: "must not be negative",
		},
		"scale exceeds precision": {
			column:  schema.Column{Name: "amount", Type: schema.TypeDecimal, Precision: 4, Scale: 5},
			wantErr: "scale 5 exceeds precision 4",
		},
		"precision on non-decimal column": {
			column:  schema.Column{Name: "amount", Type: schema.TypeText, Precision: 3},
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
// decimal column with a valid precision and scale must validate cleanly.
func TestTableValidateAcceptsDecimalColumn(t *testing.T) {
	table := schema.Table{
		Name: "payments",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: 4},
		},
		PrimaryKey: []string{"id"},
	}

	require.NoError(t, table.Validate())
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
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: 4},
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
