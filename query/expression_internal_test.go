package query

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// TestColumnRefValidateReportsMissingColumn covers ColumnRef.Validate for a
// name its source table does not hold, on an unqualified, a qualified, and an
// aliased table. No exported constructor can build these values: TableRef.Column
// still zeroes its result on a bad name until PR 3 removes that error, so the
// only way to pair a valid source with a name it does not hold is the struct
// literal here, matching the "no other way around it" case in validate_test.go.
func TestColumnRefValidateReportsMissingColumn(t *testing.T) {
	descriptor := schema.TableDef{
		Name: "users",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "email", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
	}

	users, err := NewTableRef(descriptor)
	require.NoError(t, err)
	missing := ColumnRef{source: users, name: "missing"}
	require.ErrorContains(t, missing.Validate(), `table "users" has no column "missing"`)

	descriptor.Schema = "tenant"
	qualified := MustTableRef(descriptor)
	qualifiedMissing := ColumnRef{source: qualified, name: "missing"}
	require.ErrorContains(t, qualifiedMissing.Validate(), `table "tenant.users" has no column "missing"`)

	aliased, err := qualified.As("u")
	require.NoError(t, err)
	aliasedMissing := ColumnRef{source: aliased, name: "missing"}
	require.ErrorContains(t, aliasedMissing.Validate(), `table "u" has no column "missing"`)
}
