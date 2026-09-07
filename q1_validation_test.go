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
	_ = br
	require.NoError(t, q.Validate())
	decoder := &validationDecoder{schema: p.Schema()}
	_, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", rasql.Value(int64(1)), schema.IntegerType{}, "")}, decoder)
	require.NoError(t, err)
	decoder.schema = rasql.ResultSchema{}
	projection2 := rasql.Projection[int64]{}
	_ = projection2
	q2 := rasql.Select(ar.Source(), p)
	require.NoError(t, q2.Validate())
}
