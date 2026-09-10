package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestAcceptanceResultChecksCompoundOperandMetadataAfterBodyMutation(t *testing.T) {
	table := query.MustTableRef(schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	body, err := query.NewSelect(table, table.Column("id"))
	require.NoError(t, err)
	operand, err := query.ResultOf(&body, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	compound, err := query.CompoundQuery(operand, query.UnionAll, operand)
	require.NoError(t, err)
	result, err := query.ResultOf(compound, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	body, err = query.NewSelect(table, table.Column("id"), query.Project(query.Bind(2)).As("extra"))
	require.NoError(t, err)
	statement, err := render.Result(dialect.SQLite(), result)
	require.Error(t, err)
	require.Empty(t, statement.SQL())
}
