package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestQ1ScalarDecodersUseSQLiteRows(t *testing.T) {
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

func TestQ1MultiColumnDTODecodesPresenceAndOwnsRows(t *testing.T) {
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
}
