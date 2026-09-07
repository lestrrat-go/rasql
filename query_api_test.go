package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type queryAPIDecoder struct {
	resultSchema rasql.ResultSchema
	presence     []rasql.Presence
}

func (d queryAPIDecoder) ResultSchema() rasql.ResultSchema { return d.resultSchema }
func (d queryAPIDecoder) Presence() []rasql.Presence {
	return append([]rasql.Presence(nil), d.presence...)
}
func (d queryAPIDecoder) DecodeRow(source rasql.ScanSource, result *int64) error {
	return source.Scan(result)
}

type queryAPISource struct{ value any }

func (s *queryAPISource) Scan(destinations ...any) error {
	if len(destinations) != 1 {
		return sql.ErrNoRows
	}
	return rasql.ScanValue(destinations[0].(*int64), s.value)
}

func TestResultSchemaDefensiveCopyAndValidation(t *testing.T) {
	columns := []rasql.ResultColumn{{Name: "id", Type: schema.IntegerType{}}}
	result, err := rasql.NewResultSchema(columns...)
	require.NoError(t, err)
	columns[0].Name = "changed"
	require.Equal(t, "id", result.Columns()[0].Name)
	copy := result.Columns()
	copy[0].Name = "changed"
	require.Equal(t, "id", result.Columns()[0].Name)
	for _, column := range []rasql.ResultColumn{{}, {Name: "id"}, {Name: "id", Type: schema.IntegerType{}, Codec: "bad codec"}} {
		_, err := rasql.NewResultSchema(column)
		require.Error(t, err, "accepted invalid column %#v", column)
	}
	_, err = rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
	)
	require.Error(t, err)
}

func TestProjectionValidationAndPresence(t *testing.T) {
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	decoder := queryAPIDecoder{resultSchema: resultSchema}
	item := rasql.Item("id", rasql.Value(int64(1)), schema.IntegerType{}, "")
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{item}, decoder)
	require.NoError(t, err)
	require.Len(t, projection.Schema().Columns(), 1)
	wrongSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "other", Type: schema.IntegerType{}})
	require.NoError(t, err)
	_, err = rasql.NewProjection([]rasql.ProjectionItem{item}, queryAPIDecoder{resultSchema: wrongSchema})
	require.Error(t, err)
	presence, err := rasql.NewPresence("profile", "id")
	require.NoError(t, err)
	nullSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}, Nullable: true})
	require.NoError(t, err)
	nullItem := rasql.NullItem("id", rasql.MinExpr(rasql.Value(int64(1))), schema.IntegerType{}, "")
	_, err = rasql.NewProjection([]rasql.ProjectionItem{nullItem}, queryAPIDecoder{resultSchema: nullSchema, presence: []rasql.Presence{presence}})
	require.NoError(t, err)
	unknown, err := rasql.NewPresence("profile", "missing")
	require.NoError(t, err)
	_, err = rasql.NewProjection([]rasql.ProjectionItem{item}, queryAPIDecoder{resultSchema: resultSchema, presence: []rasql.Presence{unknown}})
	require.Error(t, err)
}

func TestScalarDecoderUsesFreshDestination(t *testing.T) {
	projection, err := rasql.Scalar("value", rasql.Value(int64(1)), schema.IntegerType{}, "")
	require.NoError(t, err)
	first, second := int64(0), int64(0)
	source := &queryAPISource{value: int64(42)}
	require.NoError(t, projection.Decoder().DecodeRow(source, &first))
	source.value = int64(7)
	require.NoError(t, projection.Decoder().DecodeRow(source, &second))
	require.Equal(t, int64(42), first)
	require.Equal(t, int64(7), second)
}

func TestQueryOperationsAreImmutable(t *testing.T) {
	table, err := rasql.ReadTableOf[int64](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "u")
	require.NoError(t, err)
	projection, err := rasql.Scalar("value", rasql.Value(int64(1)), schema.IntegerType{}, "")
	require.NoError(t, err)
	base := rasql.Select(relation.Source(), projection)
	filtered := base.Where(rasql.EqualValue(rasql.Value(int64(1)), int64(1)))
	require.NoError(t, base.Validate())
	require.NoError(t, filtered.Validate())
	_, err = base.Limit(-1)
	require.Error(t, err)
	_, err = base.Offset(-1)
	require.Error(t, err)
}

func TestSourceBoundColumnsValidateNullabilityAndMembership(t *testing.T) {
	table, err := rasql.ReadTableOf[int64](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "nickname", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "u")
	require.NoError(t, err)
	column, err := rasql.BindColumn[int64, int64](relation, "id", "")
	require.NoError(t, err)
	require.NotNil(t, column.Expr())
	nullable, err := rasql.BindNullColumn[int64, string](relation, "nickname", "custom.codec")
	require.NoError(t, err)
	require.NotNil(t, nullable.NullExpr())
	_, err = rasql.BindOptionalColumn[int64, int64](rasql.Optional(relation), "id", "")
	require.NoError(t, err)
	_, err = rasql.BindColumn[int64, int64](relation, "nickname", "")
	require.Error(t, err)
	_, err = rasql.BindColumn[int64, int64](relation, "missing", "")
	require.Error(t, err)
}
