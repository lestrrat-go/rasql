package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestScalarDecoders(t *testing.T) {
	t.Run("scalar decoders read SQLite rows", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		scalar, err := rasql.Scalar("value", rasql.Value(int64(0)), schema.IntegerType{}, "int.codec")
		require.NoError(t, err)
		require.Equal(t, rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}, Codec: "int.codec"}, scalar.Schema().Columns()[0])
		rows, err := db.QueryContext(t.Context(), `SELECT 42 UNION ALL SELECT 7`)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		var got []int64
		for rows.Next() {
			var value int64
			require.NoError(t, scalar.Decoder().DecodeRow(rows, &value))
			got = append(got, value)
		}
		require.NoError(t, rows.Err())
		require.Equal(t, []int64{42, 7}, got)

		nullable, err := rasql.NullableScalar("value", rasql.MinExpr(rasql.Value("unused")), schema.TextType{}, "text.codec")
		require.NoError(t, err)
		require.Equal(t, rasql.ResultColumn{Name: "value", Type: schema.TextType{}, Nullable: true, Codec: "text.codec"}, nullable.Schema().Columns()[0])
		rows, err = db.QueryContext(t.Context(), `SELECT NULL UNION ALL SELECT 'present'`)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		var values []rasql.Nullable[string]
		for rows.Next() {
			var value rasql.Nullable[string]
			require.NoError(t, nullable.Decoder().DecodeRow(rows, &value))
			values = append(values, value)
		}
		require.NoError(t, rows.Err())
		require.Equal(t, []rasql.Nullable[string]{{Valid: false}, {Value: "present", Valid: true}}, values)
	})

	t.Run("a multi-column DTO decodes presence and owns its rows", func(t *testing.T) {
		db, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { require.NoError(t, db.Close()) })

		resultSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "name", Type: schema.TextType{}},
			rasql.ResultColumn{Name: "blob", Type: schema.BytesType{}},
			rasql.ResultColumn{Name: "profile_id", Type: schema.IntegerType{}, Nullable: true, Codec: "profile.id"},
			rasql.ResultColumn{Name: "profile_payload", Type: schema.TextType{}, Nullable: true, Codec: "profile.payload"},
		)
		require.NoError(t, err)
		decoder := q1DTODecoder{resultSchema: resultSchema}
		items := []rasql.ProjectionItem{
			rasql.Item("name", rasql.Value("ignored"), schema.TextType{}, ""),
			rasql.Item("blob", rasql.Value([]byte("ignored")), schema.BytesType{}, ""),
			rasql.NullItem("profile_id", rasql.MinExpr(rasql.Value(int64(0))), schema.IntegerType{}, "profile.id"),
			rasql.NullItem("profile_payload", rasql.MinExpr(rasql.Value("ignored")), schema.TextType{}, "profile.payload"),
		}
		projection, err := rasql.NewProjection(items, decoder)
		require.NoError(t, err)

		rows, err := db.QueryContext(t.Context(), `SELECT 'absent', x'616263', NULL, NULL UNION ALL SELECT 'present', x'646566', 9, NULL`)
		require.NoError(t, err)
		defer func() { _ = rows.Close() }()
		var got []q1DTO
		for rows.Next() {
			var value q1DTO
			require.NoError(t, projection.Decoder().DecodeRow(rows, &value))
			got = append(got, value)
		}
		require.NoError(t, rows.Err())
		require.Len(t, got, 2)
		require.Equal(t, "absent", got[0].Name)
		require.Equal(t, []byte("abc"), got[0].Blob)
		require.False(t, got[0].Profile.ID.Valid)
		require.False(t, got[0].Profile.Payload.Valid)
		require.Equal(t, "present", got[1].Name)
		require.Equal(t, []byte("def"), got[1].Blob)
		require.True(t, got[1].Profile.ID.Valid)
		require.Equal(t, int64(9), got[1].Profile.ID.Value)
		require.False(t, got[1].Profile.Payload.Valid)
		require.NotSame(t, &got[0].Blob[0], &got[1].Blob[0])
		got[0].Blob[0] = 'X'
		require.Equal(t, []byte("def"), got[1].Blob)
	})
}

type q1Profile struct {
	ID      rasql.Nullable[int64]
	Payload rasql.Nullable[string]
}

type q1DTO struct {
	Name    string
	Blob    []byte
	Profile q1Profile
}

type q1DTODecoder struct{ resultSchema rasql.ResultSchema }

func (d q1DTODecoder) ResultSchema() rasql.ResultSchema { return d.resultSchema }
func (d q1DTODecoder) Presence() []rasql.Presence {
	presence, _ := rasql.NewPresence("profile", "profile_id")
	return []rasql.Presence{presence}
}
func (d q1DTODecoder) DecodeRow(source rasql.ScanSource, result *q1DTO) error {
	var id sql.NullInt64
	var payload sql.NullString
	if err := source.Scan(&result.Name, &result.Blob, &id, &payload); err != nil {
		return err
	}
	result.Profile.ID = rasql.Nullable[int64]{Value: id.Int64, Valid: id.Valid}
	result.Profile.Payload = rasql.Nullable[string]{Value: payload.String, Valid: payload.Valid}
	return nil
}

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
func TestSourceIdentityAndDecoderValidation(t *testing.T) {
	a := validationTable(t, "a")
	b := validationTable(t, "b")
	ar, _ := rasql.SourceOf(a, "same")
	br, _ := rasql.SourceOf(b, "other")
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
	duplicateSource, _ := rasql.SourceOf(b, "same")
	duplicate := rasql.Select(ar.Source(), p).Join(duplicateSource.Source(), rasql.EqualValue(rasql.Value(int64(1)), int64(1)))
	var duplicateError *rasql.PlanError
	require.ErrorAs(t, duplicate.Validate(), &duplicateError)
	require.Equal(t, "invalid_source", duplicateError.Code)
	decoder := &validationDecoder{schema: p.Schema()}
	projection2, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", rasql.Value(int64(1)), schema.IntegerType{}, "")}, decoder)
	require.NoError(t, err)
	decoder.schema = rasql.ResultSchema{}
	require.Error(t, rasql.Select(ar.Source(), projection2).Validate())
}
