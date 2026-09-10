package rasql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type compositionRow struct{ ID int64 }
type compositionDecoder struct{ schema rasql.ResultSchema }

func (d compositionDecoder) ResultSchema() rasql.ResultSchema                { return d.schema }
func (compositionDecoder) Presence() []rasql.Presence                        { return nil }
func (compositionDecoder) DecodeRow(rasql.ScanSource, *compositionRow) error { return nil }

func compositionQuery(t *testing.T) rasql.Query[compositionRow] {
	t.Helper()
	s, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	p, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", rasql.Value(int64(1)), schema.IntegerType{}, ""),
	}, compositionDecoder{schema: s})
	require.NoError(t, err)
	table, err := rasql.ReadTableOf[compositionRow](schema.TableDef{
		Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "u")
	require.NoError(t, err)
	return rasql.Select(relation.Source(), p)
}

func TestCompositionPreservesTypedResults(t *testing.T) {
	base := compositionQuery(t)
	derived, err := rasql.Derive(base, "d")
	require.NoError(t, err)
	require.NotEqual(t, rasql.Source{}, derived.Source())
	bound, err := rasql.BindResultColumn[compositionRow, int64](derived, "id")
	require.NoError(t, err)
	_ = bound
	cte, err := rasql.CTEOf("items", base)
	require.NoError(t, err)
	cteSource, err := cte.Source("i")
	require.NoError(t, err)
	require.NotEqual(t, rasql.Source{}, cteSource.Source())
	combined, err := rasql.Combine(base, rasql.UnionAll, base)
	require.NoError(t, err)
	require.NoError(t, combined.Validate())
	paged, err := combined.Limit(1)
	require.NoError(t, err)
	require.NoError(t, paged.Validate())
	count := rasql.CountQuery(base, false)
	require.NoError(t, count.Validate())
	with, err := rasql.With(base, cte)
	require.NoError(t, err)
	require.NoError(t, with.Validate())
}

func TestCompositionRejectsInvalidAliasesAndSchemas(t *testing.T) {
	base := compositionQuery(t)
	_, err := rasql.Derive(base, "bad alias")
	var planErr *rasql.PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "invalid_source", planErr.Code)
	_, err = rasql.CTEOf("bad alias", base)
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "invalid_cte", planErr.Code)
}
