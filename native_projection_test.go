package rasql

import (
	"database/sql/driver"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type nativeProjectionRow struct {
	ID    int64
	Value Nullable[string]
}

type nativeProjectionDecoder struct{ schema ResultSchema }

func (d nativeProjectionDecoder) ResultSchema() ResultSchema                     { return d.schema }
func (nativeProjectionDecoder) Presence() []Presence                             { return nil }
func (nativeProjectionDecoder) DecodeRow(ScanSource, *nativeProjectionRow) error { return nil }

func nativeProjectionTestDecoder(t *testing.T) nativeProjectionDecoder {
	t.Helper()
	sch, err := NewResultSchema(
		ResultColumn{Name: "id", Type: schema.IntegerType{}},
		ResultColumn{Name: "value", Type: schema.TextType{}, Nullable: true},
	)
	require.NoError(t, err)
	return nativeProjectionDecoder{schema: sch}
}

func TestNativeProjectionMarksSchemaWithoutBinds(t *testing.T) {
	projection, err := NativeProjection(nativeProjectionTestDecoder(t))
	require.NoError(t, err)
	require.Len(t, projection.Schema().Columns(), 2)
	query, err := Native(NativeStatement{Engine: "sqlite", SQL: "SELECT 1 AS id, NULL AS value"}, projection, Many)
	require.NoError(t, err)
	require.NoError(t, query.Validate())
	require.Empty(t, query.plan.projection[0].expression)
	require.Empty(t, query.plan.projection[1].expression)
	ordinary := Select(runtimeQuery(t).Plan().sources[0], projection)
	var planErr *PlanError
	require.ErrorAs(t, ordinary.Validate(), &planErr)
	require.Equal(t, "unsupported_feature", planErr.Code)
	projected := Project(runtimeQuery(t).Plan(), projection)
	require.ErrorAs(t, projected.Validate(), &planErr)
	require.Equal(t, "unsupported_feature", planErr.Code)
}

func TestNativeArgumentUsesNullableBindValue(t *testing.T) {
	plan, err := newNativeQueryPlan(NativeStatement{Engine: "sqlite", SQL: "SELECT ?", Args: []NativeArgument{{Value: Nullable[int64]{}}}})
	require.NoError(t, err)
	args := plan.statement.Args()
	require.Len(t, args, 1)
	token, ok := args[0].(bindToken)
	require.True(t, ok)
	require.Nil(t, token.value)
	present, err := newNativeQueryPlan(NativeStatement{Engine: "sqlite", SQL: "SELECT ?", Args: []NativeArgument{{Value: Nullable[int64]{Value: 0, Valid: true}}}})
	require.NoError(t, err)
	presentToken := present.statement.Args()[0].(bindToken)
	require.Equal(t, int64(0), presentToken.value)
}

type nativeProjectionCountingCodec struct{ calls *atomic.Int64 }

func (c nativeProjectionCountingCodec) Encode(value any) (driver.Value, error) {
	c.calls.Add(1)
	return value, nil
}
func (nativeProjectionCountingCodec) Decode(any, any) error { return nil }

func TestEncodeStatementSkipsCodecForNilArguments(t *testing.T) {
	var calls atomic.Int64
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"count": nativeProjectionCountingCodec{calls: &calls}})
	require.NoError(t, err)
	statement := stmt.New(sqltext.Text("SELECT ?, ?"), nil, "value")
	encoded, err := encodeStatement(statement, []bindSlot{{codec: "count"}, {codec: "count"}}, registry)
	require.NoError(t, err)
	require.Equal(t, int64(1), calls.Load())
	require.Nil(t, encoded.Args()[0])
	require.Equal(t, "value", encoded.Args()[1])
}
