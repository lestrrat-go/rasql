package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type countedNullable struct {
	value any
	valid bool
	calls *atomic.Int64
}

func (v countedNullable) NullableBind() (any, bool) {
	v.calls.Add(1)
	return v.value, v.valid
}

type nullableBindCodec struct {
	encodeCalls *atomic.Int64
	decodeCalls *atomic.Int64
}

type nativeMultiExecutor struct {
	rows    *runtimeFakeRows
	dialect dialect.Dialect
}

func (e *nativeMultiExecutor) Dialect() dialect.Dialect { return e.dialect }
func (e *nativeMultiExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	return e.rows, nil
}
func (*nativeMultiExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

type nativeMultiDecoder struct{ schema ResultSchema }

func (d nativeMultiDecoder) ResultSchema() ResultSchema { return d.schema }
func (nativeMultiDecoder) Presence() []Presence         { return nil }
func (nativeMultiDecoder) DecodeRow(source ScanSource, value *nativeProjectionRow) error {
	return source.Scan(&value.ID, &value.Value)
}

func TestNativeProjectionExecutesNullableCustomCodecColumns(t *testing.T) {
	var encodes atomic.Int64
	var decodes atomic.Int64
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"custom": nullableBindCodec{encodeCalls: &encodes, decodeCalls: &decodes}})
	require.NoError(t, err)
	sch, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "value", Type: schema.TextType{}, Nullable: true, Codec: "custom"})
	require.NoError(t, err)
	projection, err := NativeProjection(nativeMultiDecoder{schema: sch})
	require.NoError(t, err)
	query, err := Native(NativeStatement{Engine: "sqlite", SQL: "SELECT 7 AS id, 'x' AS value"}, projection, Many)
	require.NoError(t, err)
	raw := &nativeMultiExecutor{dialect: dialect.SQLite(), rows: &runtimeFakeRows{values: [][]any{{int64(7), "x"}}, columns: []string{"id", "value"}}}
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	executor, err = WithCodecs(executor, registry)
	require.NoError(t, err)
	values, err := All(t.Context(), executor, query)
	require.NoError(t, err)
	require.Len(t, values, 1)
	require.Equal(t, int64(7), values[0].ID)
	require.True(t, values[0].Value.Valid)
	require.Equal(t, "x", values[0].Value.Value)
	require.Equal(t, int64(1), decodes.Load())
}

func (c nullableBindCodec) Encode(value any) (driver.Value, error) {
	c.encodeCalls.Add(1)
	return value, nil
}
func (c nullableBindCodec) Decode(value any, destination any) error {
	c.decodeCalls.Add(1)
	return ScanValue(destination.(*string), value)
}

func TestNullableBindValueNormalizesCanonicalAndCustomValuesOnce(t *testing.T) {
	var canonicalCalls atomic.Int64
	var customCalls atomic.Int64
	plan, err := newNativeQueryPlan(NativeStatement{Engine: "sqlite", SQL: "SELECT ?, ?", Args: []NativeArgument{
		{Value: Nullable[int64]{}},
		{Value: countedNullable{value: int64(0), valid: true, calls: &customCalls}},
	}})
	require.NoError(t, err)
	require.Equal(t, int64(0), canonicalCalls.Load())
	require.Equal(t, int64(1), customCalls.Load())
	args := plan.statement.Args()
	require.Nil(t, args[0].(bindToken).value)
	require.Equal(t, int64(0), args[1].(bindToken).value)
}

func TestNullableBindValuePreservesNamedNullAndSkipsCodec(t *testing.T) {
	var encodes atomic.Int64
	var decodes atomic.Int64
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"nullable": nullableBindCodec{encodeCalls: &encodes, decodeCalls: &decodes}})
	require.NoError(t, err)
	statement := stmt.New(sqltext.Text("SELECT ?"), sql.Named("value", nil))
	encoded, err := encodeStatement(statement, []bindSlot{{codec: "nullable"}}, registry)
	require.NoError(t, err)
	require.Zero(t, encodes.Load())
	arg, ok := encoded.Args()[0].(sql.NamedArg)
	require.True(t, ok)
	require.Equal(t, "value", arg.Name)
	require.Nil(t, arg.Value)
}

func TestRepeatedNonNullCodecOccurrencesEncodeOnceEach(t *testing.T) {
	var encodes atomic.Int64
	var decodes atomic.Int64
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"nullable": nullableBindCodec{encodeCalls: &encodes, decodeCalls: &decodes}})
	require.NoError(t, err)
	statement := stmt.New(sqltext.Text("SELECT ?, ?"), int64(0), int64(0))
	_, err = encodeStatement(statement, []bindSlot{{codec: "nullable"}, {codec: "nullable"}}, registry)
	require.NoError(t, err)
	require.Equal(t, int64(2), encodes.Load())
}

func TestMissingSelectedCodecFailsBeforeExecutor(t *testing.T) {
	raw := &runtimeFakeExecutor{dialect: dialect.SQLite()}
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	sch, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}, Codec: "missing"})
	require.NoError(t, err)
	decoder := runtimeDecoder{schema: sch}
	projection, err := NativeProjection(decoder)
	require.NoError(t, err)
	query, err := Native(NativeStatement{Engine: "sqlite", SQL: "SELECT 1 AS value"}, projection, Many)
	require.NoError(t, err)
	_, err = All(t.Context(), executor, query)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "codec_unavailable", planErr.Code)
	require.Zero(t, raw.calls.Load())
}

func TestNativeProjectionRejectsTypedNilInvalidSchemaAndPresence(t *testing.T) {
	var typedNil *nativeProjectionDecoder
	_, err := NativeProjection[nativeProjectionRow](typedNil)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "invalid_projection", planErr.Code)
	invalid := nativeInvalidDecoder{}
	_, err = NativeProjection(invalid)
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "invalid_schema", planErr.Code)
	badPresence := nativeBadPresenceDecoder{}
	_, err = NativeProjection(badPresence)
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "invalid_projection", planErr.Code)
}

type nativeInvalidDecoder struct{}

func (nativeInvalidDecoder) ResultSchema() ResultSchema         { return ResultSchema{} }
func (nativeInvalidDecoder) Presence() []Presence               { return nil }
func (nativeInvalidDecoder) DecodeRow(ScanSource, *int64) error { return errors.New("unused") }

type nativeBadPresenceDecoder struct{}

func (nativeBadPresenceDecoder) ResultSchema() ResultSchema {
	sch, _ := NewResultSchema(ResultColumn{Name: "value", Type: schema.TextType{}, Nullable: true})
	return sch
}
func (nativeBadPresenceDecoder) Presence() []Presence {
	return []Presence{{component: "", columns: []string{"value"}}}
}
func (nativeBadPresenceDecoder) DecodeRow(ScanSource, *int64) error { return nil }
