package rasql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func graphStageCompiler(t *testing.T) rasql.Compiler {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	compiler, err := profile.Compiler(dialect.SQLite())
	require.NoError(t, err)
	return compiler
}

func TestGraphStageBindMetadata(t *testing.T) {
	t.Run("a partition limit uses one stable bind token", func(t *testing.T) {
		q := partitionQuery(t)
		token, ok := rasql.Q1PartitionLimitToken(q)
		require.True(t, ok)
		require.NotZero(t, token.ID)
		require.Equal(t, int64(2), token.Value)

		compiler := graphStageCompiler(t)
		first, err := rasql.Q1CompileQuery(compiler, q)
		require.NoError(t, err)
		second, err := rasql.Q1CompileQuery(compiler, q)
		require.NoError(t, err)
		findLimit := func(compiled bindplan.Compiled) bindplan.ID {
			for index, value := range compiled.Statement.Args() {
				if value == int64(2) {
					return compiled.Slots[index].ID
				}
			}
			return 0
		}
		require.Equal(t, token.ID, findLimit(first))
		require.Equal(t, token.ID, findLimit(second))
	})
}

type partitionRow struct{ ID int64 }

type partitionDecoder struct{ result rasql.ResultSchema }

func (d partitionDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (partitionDecoder) Presence() []rasql.Presence         { return nil }
func (partitionDecoder) DecodeRow(source rasql.ScanSource, row *partitionRow) error {
	return source.Scan(&row.ID)
}

// partitionQuery builds a query carrying the per-parent limit a graph edge
// applies, which is where a limit becomes a bound value.
func partitionQuery(t *testing.T) rasql.Query[partitionRow] {
	t.Helper()
	result, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	table, err := rasql.ReadTableOf[partitionRow](schema.TableDef{
		Name:    "items",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "i")
	require.NoError(t, err)
	column, err := rasql.BindColumn[partitionRow, int64](relation, "id", "")
	require.NoError(t, err)
	projection, err := rasql.NewProjection(
		[]rasql.ProjectionItem{rasql.Item("id", column.Expr(), schema.IntegerType{}, "")},
		partitionDecoder{result: result},
	)
	require.NoError(t, err)
	q, err := rasql.Q1WithPartitionLimit(
		rasql.Select(relation.Source(), projection),
		[]rasql.GroupKey{rasql.Group(column.Expr())},
		[]rasql.OrderTerm{rasql.AscExpr(column.Expr())}, 2)
	require.NoError(t, err)
	return q
}
