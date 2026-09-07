package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	querypkg "github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type r5CountingCursorCodec struct{ enc atomic.Int64 }

func (c *r5CountingCursorCodec) Encode(value any) (driver.Value, error) {
	c.enc.Add(1)
	return value, nil
}
func (*r5CountingCursorCodec) Decode(value any, destination any) error {
	*destination.(*int64) = value.(int64)
	return nil
}
func (*r5CountingCursorCodec) EncodeCursor(value any) ([]byte, error) {
	return encodeBuiltinCursor(value)
}
func (*r5CountingCursorCodec) DecodeCursor(value []byte) (any, error) {
	return decodeBuiltinCursor(value, reflect.TypeOf(int64(0)))
}

func TestR5PageAfterCountsRealNamedCodecOccurrencesWithoutFingerprintOrRowsReencoding(t *testing.T) {
	table, err := ReadTableOf[int64](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "i")
	require.NoError(t, err)
	id, err := BindColumn[int64, int64](relation, "id", "")
	require.NoError(t, err)
	resultSchema, err := NewResultSchema(ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("value", id.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	baseQuery := Select(relation.Source(), projection)
	filterID := bindID(atomic.AddUint64(&nextBindID, 1))
	filter := Expr[int64]{node: querypkg.Bind(bindToken{id: filterID, value: sql.Named("filter", int64(1)), copy: func() (any, error) { return sql.Named("filter", int64(1)), nil }})}
	baseQuery = baseQuery.Where(Predicate{node: querypkg.Equal(id.Expr().node, filter.node)}).Where(Predicate{node: querypkg.Equal(id.Expr().node, filter.node)})
	codec := &r5CountingCursorCodec{}
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"count.page": codec})
	require.NoError(t, err)
	orderExpr, err := ValueWithCodec(int64(7), "count.page")
	require.NoError(t, err)
	orderKey := AscKey[int64](orderExpr, func(int64) int64 { return 7 })
	idKey := AscKey[int64](id.Expr(), func(value int64) int64 { return value })
	spec, err := NewPageSpec([]PageKey[int64]{orderKey, idKey}, idKey)
	require.NoError(t, err)
	raw := &runtimeFakeExecutor{rows: [][]any{{int64(1)}, {int64(2)}}, dialect: dialect.SQLite()}
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	baseExecutor, err := WithEngineProfile(raw, profile)
	require.NoError(t, err)
	executor, err := WithCodecs(baseExecutor, registry)
	require.NoError(t, err)
	first, err := PageAfter(context.Background(), executor, baseQuery, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 3}, PageRequest{Limit: 1})
	require.NoError(t, err)
	require.True(t, first.HasMore)
	require.NotEmpty(t, first.Next)
	firstCount := codec.enc.Load()
	require.Equal(t, int64(1), firstCount)
	second, err := PageAfter(context.Background(), executor, baseQuery, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 3}, PageRequest{Limit: 1, After: first.Next})
	require.NoError(t, err)
	require.NotEmpty(t, second.Values)
	require.Equal(t, firstCount+2, codec.enc.Load())
	require.Equal(t, int64(2), raw.calls.Load())
	changedFilter := baseQuery.Where(Predicate{node: querypkg.Equal(id.Expr().node, querypkg.Bind(int64(99)))})
	before := raw.calls.Load()
	_, err = PageAfter(context.Background(), executor, changedFilter, spec, PagePolicy{DefaultLimit: 1, MaxLimit: 3}, PageRequest{Limit: 1, After: first.Next})
	require.ErrorIs(t, err, ErrInvalidCursor)
	require.Equal(t, before, raw.calls.Load())
}

func TestR5BaseOccurrencesMatchOrderedSubsequenceByIdentityAndCodec(t *testing.T) {
	first := bindID(11)
	second := bindID(12)
	base := compiledQuery{
		statement: stmt.New(sqltext.Text("SELECT ? WHERE x = ? ORDER BY y = ?"), sql.Named("filter", int64(1)), sql.Named("order", int64(2)), int64(3)),
		bindSlots: []bindSlot{{id: first, codec: "filter.codec"}, {id: second, codec: "order.codec"}, {id: second, codec: "order.codec"}},
	}
	paged := compiledQuery{
		statement: stmt.New(sqltext.Text("SELECT ? WHERE x = ? AND key > ? ORDER BY y = ? AND z = ?"), int64(99), sql.Named("filter", int64(1)), int64(9), sql.Named("order", int64(2)), sql.Named("order", int64(2))),
		bindSlots: []bindSlot{{id: bindID(99)}, {id: first, codec: "filter.codec"}, {id: bindID(98)}, {id: second, codec: "order.codec"}, {id: second, codec: "order.codec"}},
	}
	indexes, err := matchBaseOccurrences(base, paged)
	require.NoError(t, err)
	require.Equal(t, []int{1, 3, 4}, indexes)
	require.Equal(t, "filter", paged.statement.Args()[indexes[0]].(sql.NamedArg).Name)
	require.Equal(t, "order", paged.statement.Args()[indexes[1]].(sql.NamedArg).Name)
}

func TestR5BaseOccurrencesDistinguishCodecAndReorderedBinds(t *testing.T) {
	base := compiledQuery{statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: []bindSlot{{id: bindID(21), codec: "a"}}}
	wrongCodec := compiledQuery{statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: []bindSlot{{id: bindID(21), codec: "b"}}}
	_, err := matchBaseOccurrences(base, wrongCodec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "codec differs")
	missing := compiledQuery{statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: []bindSlot{{id: bindID(22), codec: "a"}}}
	_, err = matchBaseOccurrences(base, missing)
	require.Error(t, err)
	require.Contains(t, err.Error(), "missing or reordered")
	zero := compiledQuery{statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: []bindSlot{{id: 0}}}
	_, err = matchBaseOccurrences(zero, wrongCodec)
	require.Error(t, err)
	require.Contains(t, err.Error(), "no identity")
}

func TestR5FingerprintChangesFilterButIgnoresCursorAndLimitBinds(t *testing.T) {
	statement := stmt.New(sqltext.Text("SELECT id FROM items WHERE category = ? AND id > ? LIMIT ?"), "books", int64(7), int64(3))
	keys := []*pageKey[int]{&pageKey[int]{direction: PageAscending, term: OrderTerm{source: "items"}}}
	first, err := pageFingerprint("sqlite", "SELECT id FROM items WHERE category = ? ORDER BY id", mustRuntimeSchema(t, ResultColumn{Name: "id", Type: schema.IntegerType{}}), statement, keys, []int{0})
	require.NoError(t, err)
	changed := stmt.New(sqltext.Text(statement.SQL()), "music", int64(7), int64(99))
	second, err := pageFingerprint("sqlite", "SELECT id FROM items WHERE category = ? ORDER BY id", mustRuntimeSchema(t, ResultColumn{Name: "id", Type: schema.IntegerType{}}), changed, keys, []int{0})
	require.NoError(t, err)
	require.NotEqual(t, first, second)
	require.False(t, strings.Contains(statement.SQL(), "cursor"))
}
