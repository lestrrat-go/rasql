package rasql_test

import (
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	"testing"
)

type validationDecoder struct {
	schema   rasql.ResultSchema
	presence []rasql.Presence
}

func (d *validationDecoder) ResultSchema() rasql.ResultSchema       { return d.schema }
func (d *validationDecoder) Presence() []rasql.Presence             { return d.presence }
func (*validationDecoder) DecodeRow(rasql.ScanSource, *int64) error { return nil }
func validationTable(t *testing.T, name string) rasql.ReadTable[int64] {
	t.Helper()
	table, err := rasql.ReadTableOf[int64](schema.TableDef{Name: name, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	return table
}
func validationProjection(t *testing.T) rasql.Projection[int64] {
	t.Helper()
	s, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	p, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", rasql.Value(int64(1)), schema.IntegerType{}, "")}, &validationDecoder{schema: s})
	require.NoError(t, err)
	return p
}
func TestQ1SourceIdentityAndDecoderValidation(t *testing.T) {
	a := validationTable(t, "a")
	b := validationTable(t, "b")
	ar, _ := rasql.SourceOf(a, "same")
	br, _ := rasql.SourceOf(b, "same")
	p := validationProjection(t)
	q := rasql.Select(ar.Source(), p).Where(rasql.EqualValue(rasql.Value(int64(1)), int64(1)))
	require.NoError(t, q.Validate())
	ac, err := rasql.BindColumn[int64, int64](ar, "id", "")
	require.NoError(t, err)
	columnProjection, err := rasql.Scalar("id", ac.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	columnQuery := rasql.Select(ar.Source(), columnProjection).Where(rasql.EqualValue(ac.Expr(), int64(1))).GroupBy(rasql.Group(ac.Expr())).OrderBy(rasql.AscExpr(ac.Expr()))
	require.NoError(t, columnQuery.Validate())
	bc, err := rasql.BindColumn[int64, int64](br, "id", "")
	require.NoError(t, err)
	outside := rasql.Select(ar.Source(), columnProjection).Where(rasql.EqualValue(bc.Expr(), int64(1)))
	var pe *rasql.PlanError
	require.ErrorAs(t, outside.Validate(), &pe)
	require.Equal(t, "invalid_source", pe.Code)
	joined := rasql.Select(ar.Source(), p).Join(br.Source(), rasql.EqualExpr(ac.Expr(), bc.Expr()))
	require.NoError(t, joined.Validate())
	decoder := &validationDecoder{schema: p.Schema()}
	projection2, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", rasql.Value(int64(1)), schema.IntegerType{}, "")}, decoder)
	require.NoError(t, err)
	decoder.schema = rasql.ResultSchema{}
	require.Error(t, rasql.Select(ar.Source(), projection2).Validate())
}
