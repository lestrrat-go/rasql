package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	querypkg "github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestGraphCacheCanonicalEncodedValues(t *testing.T) {
	first, err := normalizeGraphValue(float32(1.25))
	require.NoError(t, err)
	second, err := normalizeGraphValue(float64(1.25))
	require.NoError(t, err)
	firstFrame, err := frameGraphValue(first)
	require.NoError(t, err)
	secondFrame, err := frameGraphValue(second)
	require.NoError(t, err)
	require.Equal(t, firstFrame, secondFrame)

	stamp := time.Date(2026, 9, 7, 1, 2, 3, 4, time.UTC)
	stampFrame, err := frameGraphValue(driver.Value(stamp))
	require.NoError(t, err)
	require.NotEmpty(t, stampFrame)
	_, err = frameGraphValue(driver.Value(math.NaN()))
	require.Error(t, err)
}

func TestGraphCacheDecoderCompatibilityIncludesEmptyEntries(t *testing.T) {
	one := struct{ Name string }{Name: "one"}
	two := struct{ Name string }{Name: "two"}
	entry := graphCacheEntry{decoder: one}
	require.True(t, reflect.DeepEqual(entry.decoder, one))
	require.False(t, reflect.DeepEqual(entry.decoder, two))
}

func TestGraphCacheSnapshotCopiesBytes(t *testing.T) {
	value := []byte("snapshot")
	copyValue := graphCloneEncoded(value).([]byte)
	value[0] = 'X'
	require.Equal(t, []byte("snapshot"), copyValue)
	copyValue[0] = 'Y'
	require.Equal(t, byte('X'), value[0])
}

type graphCacheParentRow struct{ ID int64 }
type graphCacheChildRow struct {
	ID      int64
	Parent  int64
	Rank    int64
	Payload *[]byte
}

type graphCacheParent struct {
	First  LoadedMany[graphCacheChild]
	Second LoadedMany[graphCacheChild]
}
type graphCacheChild struct {
	ID      int64
	Rank    int64
	Payload []byte
	Marker  string
}

type graphCacheParentDecoder struct{ schema ResultSchema }

func (d graphCacheParentDecoder) ResultSchema() ResultSchema { return d.schema }
func (graphCacheParentDecoder) Presence() []Presence         { return nil }
func (d graphCacheParentDecoder) DecodeRow(source ScanSource, value *graphCacheParentRow) error {
	return source.Scan(&value.ID)
}

type graphCacheChildDecoder struct {
	schema ResultSchema
	mode   string
}

func (d graphCacheChildDecoder) ResultSchema() ResultSchema { return d.schema }
func (graphCacheChildDecoder) Presence() []Presence         { return nil }
func (d graphCacheChildDecoder) DecodeRow(source ScanSource, value *graphCacheChildRow) error {
	var payload []byte
	if err := source.Scan(&value.ID, &value.Parent, &value.Rank, &payload); err != nil {
		return err
	}
	value.Payload = &payload
	return nil
}

type graphCacheExecutor struct {
	Executor
	compiler   *querycompile.Compiler
	statements atomic.Int64
}

func (e *graphCacheExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }
func (e *graphCacheExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	if strings.Contains(statement.SQL(), "graph_cache_children") {
		e.statements.Add(1)
	}
	return e.Executor.Query(ctx, statement)
}
func (e *graphCacheExecutor) Exec(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	return e.Executor.Exec(ctx, statement)
}

type graphCacheCodec struct{ enc atomic.Int64 }

func (c *graphCacheCodec) Encode(value any) (driver.Value, error) {
	c.enc.Add(1)
	return value, nil
}
func (*graphCacheCodec) Decode(value any, destination any) error {
	return fmt.Errorf("graph cache codec does not decode result values")
}

type graphCacheFixture struct {
	executor     *graphCacheExecutor
	parentSource Source
	childSource  Source
	parentQuery  Query[graphCacheParentRow]
	childQuery   Query[graphCacheChildRow]
	parentKey    GraphKey[graphCacheParentRow]
	childKey     GraphKey[graphCacheChildRow]
	parentID     Expr[int64]
	childID      Expr[int64]
	childParent  Expr[int64]
	childRank    Expr[int64]
	childPayload Expr[[]byte]
}

func graphCacheFixtureFor(t *testing.T) graphCacheFixture {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`CREATE TABLE graph_cache_parents (id INTEGER PRIMARY KEY);
CREATE TABLE graph_cache_children (id INTEGER PRIMARY KEY, parent INTEGER NOT NULL, rank INTEGER NOT NULL, payload BLOB NOT NULL);
INSERT INTO graph_cache_parents VALUES (1), (2);
INSERT INTO graph_cache_children VALUES (11, 1, 0, X'61'), (12, 1, 1, X'62'), (21, 2, 0, X'63'), (22, 2, 1, X'64')`)
	require.NoError(t, err)
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	base, err := AsExecutor(db, profile)
	require.NoError(t, err)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	executor := &graphCacheExecutor{Executor: base, compiler: provider.queryCompiler()}
	parents, err := SourceOf(MustReadTableOf[graphCacheParentRow](schema.TableDef{
		Name: "graph_cache_parents", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	}), "p")
	require.NoError(t, err)
	children, err := SourceOf(MustReadTableOf[graphCacheChildRow](schema.TableDef{
		Name: "graph_cache_children", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}},
			{Name: "rank", Type: schema.IntegerType{}}, {Name: "payload", Type: schema.BytesType{}},
		},
	}), "c")
	require.NoError(t, err)
	parentRelation := TypedRelation[graphCacheParentRow]{source: parents.Source()}
	childRelation := TypedRelation[graphCacheChildRow]{source: children.Source()}
	parentID, err := BindColumn[graphCacheParentRow, int64](parentRelation, "id", "")
	require.NoError(t, err)
	childID, err := BindColumn[graphCacheChildRow, int64](childRelation, "id", "")
	require.NoError(t, err)
	childParent, err := BindColumn[graphCacheChildRow, int64](childRelation, "parent", "")
	require.NoError(t, err)
	childRank, err := BindColumn[graphCacheChildRow, int64](childRelation, "rank", "")
	require.NoError(t, err)
	childPayload, err := BindColumn[graphCacheChildRow, []byte](childRelation, "payload", "")
	require.NoError(t, err)
	parentSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	parentProjection, err := NewProjection([]ProjectionItem{Item("id", parentID.Expr(), schema.IntegerType{}, "")}, graphCacheParentDecoder{schema: parentSchema})
	require.NoError(t, err)
	childSchema, err := NewResultSchema(
		ResultColumn{Name: "id", Type: schema.IntegerType{}},
		ResultColumn{Name: "parent", Type: schema.IntegerType{}},
		ResultColumn{Name: "rank", Type: schema.IntegerType{}},
		ResultColumn{Name: "payload", Type: schema.BytesType{}},
	)
	require.NoError(t, err)
	childProjection, err := NewProjection([]ProjectionItem{
		Item("id", childID.Expr(), schema.IntegerType{}, ""),
		Item("parent", childParent.Expr(), schema.IntegerType{}, ""),
		Item("rank", childRank.Expr(), schema.IntegerType{}, ""),
		Item("payload", childPayload.Expr(), schema.BytesType{}, ""),
	}, graphCacheChildDecoder{schema: childSchema})
	require.NoError(t, err)
	parentQuery := Select(parents.Source(), parentProjection).OrderBy(AscExpr(parentID.Expr()))
	childQuery := Select(children.Source(), childProjection).OrderBy(AscExpr(childRank.Expr()), AscExpr(childID.Expr()))
	parentKey, err := NewGraphKey(KeyPart(parentID, func(row graphCacheParentRow) int64 { return row.ID }))
	require.NoError(t, err)
	childKey, err := NewGraphKey(KeyPart(childParent, func(row graphCacheChildRow) int64 { return row.Parent }))
	require.NoError(t, err)
	return graphCacheFixture{executor: executor, parentSource: parents.Source(), childSource: children.Source(), parentQuery: parentQuery, childQuery: childQuery, parentKey: parentKey, childKey: childKey, parentID: parentID.Expr(), childID: childID.Expr(), childParent: childParent.Expr(), childRank: childRank.Expr(), childPayload: childPayload.Expr()}
}

func graphCacheChildQuery(t *testing.T, fixture graphCacheFixture, rank *int64, mode string, codec string) Query[graphCacheChildRow] {
	t.Helper()
	projection := fixture.childQuery.Projection()
	projection.decoder = graphCacheChildDecoder{schema: projection.schema, mode: mode}
	query := fixture.childQuery
	query.projection = projection
	if rank != nil {
		value, err := ValueWithCodec(*rank, codec)
		require.NoError(t, err)
		query = query.Where(EqualExpr(fixture.childRank, value))
	}
	return query
}

func graphCacheChildPlan(t *testing.T, query Query[graphCacheChildRow], marker string, mapped *atomic.Int64) GraphPlan[graphCacheChildRow, graphCacheChild] {
	t.Helper()
	plan, err := NewGraphPlan(query, func(row graphCacheChildRow) graphCacheChild {
		mapped.Add(1)
		return graphCacheChild{ID: row.ID, Rank: row.Rank, Payload: *row.Payload, Marker: marker}
	})
	require.NoError(t, err)
	return plan
}

func graphCacheParentPlan(t *testing.T, fixture graphCacheFixture, first, second GraphPlan[graphCacheChildRow, graphCacheChild], options EdgeOptions) GraphPlan[graphCacheParentRow, graphCacheParent] {
	t.Helper()
	edge, err := HasMany("first", fixture.parentKey, fixture.childKey, first, options, func(parent *graphCacheParent, loaded LoadedMany[graphCacheChild]) { parent.First = loaded })
	require.NoError(t, err)
	secondEdge, err := HasMany("second", fixture.parentKey, fixture.childKey, second, options, func(parent *graphCacheParent, loaded LoadedMany[graphCacheChild]) { parent.Second = loaded })
	require.NoError(t, err)
	plan, err := NewGraphPlan(fixture.parentQuery, func(row graphCacheParentRow) graphCacheParent { return graphCacheParent{} }, edge, secondEdge)
	require.NoError(t, err)
	return plan
}

func graphCacheExecutorWithCodec(t *testing.T, fixture graphCacheFixture, codec *graphCacheCodec) Executor {
	t.Helper()
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"graph.cache.fixed": codec})
	require.NoError(t, err)
	executor, err := WithCodecs(fixture.executor, registry)
	require.NoError(t, err)
	return executor
}

func TestGraphSQLiteEquivalentChildQueriesShareLoadGraphCache(t *testing.T) {
	fixture := graphCacheFixtureFor(t)
	codec := &graphCacheCodec{}
	executor := graphCacheExecutorWithCodec(t, fixture, codec)
	rank := int64(0)
	firstQuery := graphCacheChildQuery(t, fixture, &rank, "same", "graph.cache.fixed")
	secondQuery := graphCacheChildQuery(t, fixture, &rank, "same", "graph.cache.fixed")
	var firstMapped, secondMapped atomic.Int64
	first := graphCacheChildPlan(t, firstQuery, "first", &firstMapped)
	second := graphCacheChildPlan(t, secondQuery, "second", &secondMapped)
	plan := graphCacheParentPlan(t, fixture, first, second, EdgeOptions{BindLimit: 2})
	values, err := LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 2)
	require.Equal(t, int64(2), fixture.executor.statements.Load())
	// Each separately prepared child stage encodes its fixed occurrence once.
	require.Equal(t, int64(2), codec.enc.Load())
	require.Equal(t, int64(2), firstMapped.Load())
	require.Equal(t, int64(2), secondMapped.Load())
	for _, value := range values {
		require.Len(t, value.First.Values, 1)
		require.Len(t, value.Second.Values, 1)
		require.Equal(t, "first", value.First.Values[0].Marker)
		require.Equal(t, "second", value.Second.Values[0].Marker)
	}
}

func TestGraphSQLiteCacheFingerprintIncludesEncodedValues(t *testing.T) {
	fixture := graphCacheFixtureFor(t)
	codec := &graphCacheCodec{}
	executor := graphCacheExecutorWithCodec(t, fixture, codec)
	zero, one := int64(0), int64(1)
	firstQuery := graphCacheChildQuery(t, fixture, &zero, "zero", "graph.cache.fixed")
	secondQuery := graphCacheChildQuery(t, fixture, &one, "one", "graph.cache.fixed")
	var firstMapped, secondMapped atomic.Int64
	first := graphCacheChildPlan(t, firstQuery, "zero", &firstMapped)
	second := graphCacheChildPlan(t, secondQuery, "one", &secondMapped)
	plan := graphCacheParentPlan(t, fixture, first, second, EdgeOptions{BindLimit: 2})
	values, err := LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 2)
	require.Equal(t, int64(4), fixture.executor.statements.Load())
	require.Equal(t, int64(2), codec.enc.Load())
	require.Equal(t, int64(2), firstMapped.Load())
	require.Equal(t, int64(2), secondMapped.Load())
	require.Equal(t, int64(0), values[0].First.Values[0].Rank)
	require.Equal(t, int64(1), values[0].Second.Values[0].Rank)
}

func TestGraphSQLiteCacheDecoderAndEmptyEntryCompatibility(t *testing.T) {
	fixture := graphCacheFixtureFor(t)
	noRows := int64(99)
	firstQuery := graphCacheChildQuery(t, fixture, &noRows, "same", "")
	secondQuery := graphCacheChildQuery(t, fixture, &noRows, "same", "")
	var firstMapped, secondMapped atomic.Int64
	first := graphCacheChildPlan(t, firstQuery, "first", &firstMapped)
	second := graphCacheChildPlan(t, secondQuery, "second", &secondMapped)
	plan := graphCacheParentPlan(t, fixture, first, second, EdgeOptions{})
	values, err := LoadGraph(t.Context(), fixture.executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 2)
	require.Equal(t, int64(1), fixture.executor.statements.Load())
	require.Zero(t, firstMapped.Load())
	require.Zero(t, secondMapped.Load())

	fixture = graphCacheFixtureFor(t)
	firstQuery = graphCacheChildQuery(t, fixture, &noRows, "first-decoder", "")
	secondQuery = graphCacheChildQuery(t, fixture, &noRows, "second-decoder", "")
	first = graphCacheChildPlan(t, firstQuery, "first", &firstMapped)
	second = graphCacheChildPlan(t, secondQuery, "second", &secondMapped)
	plan = graphCacheParentPlan(t, fixture, first, second, EdgeOptions{})
	_, err = LoadGraph(t.Context(), fixture.executor, plan)
	require.NoError(t, err)
	require.Equal(t, int64(2), fixture.executor.statements.Load())
}

func TestGraphSQLiteCacheDoesNotReuseMappedValuesOrBytes(t *testing.T) {
	fixture := graphCacheFixtureFor(t)
	var firstMapped, secondMapped atomic.Int64
	first := graphCacheChildPlan(t, graphCacheChildQuery(t, fixture, nil, "same", ""), "first", &firstMapped)
	second := graphCacheChildPlan(t, graphCacheChildQuery(t, fixture, nil, "same", ""), "second", &secondMapped)
	plan := graphCacheParentPlan(t, fixture, first, second, EdgeOptions{})
	values, err := LoadGraph(t.Context(), fixture.executor, plan)
	require.NoError(t, err)
	require.Equal(t, int64(1), fixture.executor.statements.Load())
	require.Equal(t, int64(4), firstMapped.Load())
	require.Equal(t, int64(4), secondMapped.Load())
	require.Len(t, values[0].First.Values, 2)
	require.Len(t, values[0].Second.Values, 2)
	values[0].First.Values[0].Payload[0] = 'z'
	require.Equal(t, byte('a'), values[0].Second.Values[0].Payload[0])
}

func TestGraphSQLiteLegacyZeroIDFixedBindRunsWithoutCacheReuse(t *testing.T) {
	fixture := graphCacheFixtureFor(t)
	fixed := querypkg.Bind(int64(0))
	predicate := Predicate{node: querypkg.Equal(fixture.childRank.node, fixed), source: fixture.childRank.source}
	firstQuery := fixture.childQuery.Where(predicate)
	secondQuery := fixture.childQuery.Where(predicate)
	compiled, err := compileQuery(fixture.executor.compiler, firstQuery)
	require.NoError(t, err)
	require.Len(t, compiled.bindSlots, 1)
	require.Zero(t, compiled.bindSlots[0].id)
	var firstMapped, secondMapped atomic.Int64
	first := graphCacheChildPlan(t, firstQuery, "first", &firstMapped)
	second := graphCacheChildPlan(t, secondQuery, "second", &secondMapped)
	plan := graphCacheParentPlan(t, fixture, first, second, EdgeOptions{})
	_, err = LoadGraph(t.Context(), fixture.executor, plan)
	require.NoError(t, err)
	require.Equal(t, int64(2), fixture.executor.statements.Load())
	require.Equal(t, int64(2), firstMapped.Load())
	require.Equal(t, int64(2), secondMapped.Load())
}

func TestGraphSQLiteManyThroughUnequalWidthsRejectBeforeExecution(t *testing.T) {
	executor, _, _, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
	wideJunctionParent := GraphKey[mtJunctionRow]{key: &graphKeySpec{parts: append(append([]*graphKeyPartSpec(nil), junctionParent.key.parts...), junctionChild.key.parts[0])}}
	_, err := ManyThrough("one-to-two", parentKey, wideJunctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{}, func(*mtParentGraph, LoadedMany[mtChildGraph]) {})
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "invalid_graph_edge", planErr.Code)

	wideParent := GraphKey[mtParentRow]{key: &graphKeySpec{parts: append(append([]*graphKeyPartSpec(nil), parentKey.key.parts...), parentKey.key.parts[0])}}
	_, err = ManyThrough("two-to-one", wideParent, junctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{}, func(*mtParentGraph, LoadedMany[mtChildGraph]) {})
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "invalid_graph_edge", planErr.Code)

	require.Zero(t, executor.junctionStatements.Load())
	require.Zero(t, executor.targetStatements.Load())
}
