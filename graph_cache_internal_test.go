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
	"github.com/lestrrat-go/rasql/internal/graphfingerprint"
	"github.com/lestrrat-go/rasql/internal/graphkey"
	querypkg "github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestGraphCache(t *testing.T) {
	t.Run("canonical encoded values", func(t *testing.T) {
		first, err := graphkey.Normalize(float32(1.25))
		require.NoError(t, err)
		second, err := graphkey.Normalize(float64(1.25))
		require.NoError(t, err)
		firstFrame, err := graphkey.Frame(first)
		require.NoError(t, err)
		secondFrame, err := graphkey.Frame(second)
		require.NoError(t, err)
		require.Equal(t, firstFrame, secondFrame)

		stamp := time.Date(2026, 9, 7, 1, 2, 3, 4, time.UTC)
		stampFrame, err := graphkey.Frame(driver.Value(stamp))
		require.NoError(t, err)
		require.NotEmpty(t, stampFrame)
		_, err = graphkey.Frame(driver.Value(math.NaN()))
		require.Error(t, err)
	})

	t.Run("decoder compatibility includes empty entries", func(t *testing.T) {
		one := struct{ Name string }{Name: "one"}
		two := struct{ Name string }{Name: "two"}
		entry := graphCacheEntry{decoder: one}
		require.True(t, reflect.DeepEqual(entry.decoder, one))
		require.False(t, reflect.DeepEqual(entry.decoder, two))
	})

	t.Run("a snapshot copies its bytes", func(t *testing.T) {
		value := []byte("snapshot")
		copyValue := graphfingerprint.CloneEncoded(value).([]byte)
		value[0] = 'X'
		require.Equal(t, []byte("snapshot"), copyValue)
		copyValue[0] = 'Y'
		require.Equal(t, byte('X'), value[0])
	})

	t.Run("equivalent child queries share the load-graph cache", func(t *testing.T) {
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
		require.Equal(t, int64(2), fixture.counter.statements.Load())
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
	})

	t.Run("the fingerprint includes encoded values", func(t *testing.T) {
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
		require.Equal(t, int64(4), fixture.counter.statements.Load())
		require.Equal(t, int64(2), codec.enc.Load())
		require.Equal(t, int64(2), firstMapped.Load())
		require.Equal(t, int64(2), secondMapped.Load())
		require.Equal(t, int64(0), values[0].First.Values[0].Rank)
		require.Equal(t, int64(1), values[0].Second.Values[0].Rank)
	})

	t.Run("decoder and empty-entry compatibility", func(t *testing.T) {
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
		require.Equal(t, int64(1), fixture.counter.statements.Load())
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
		require.Equal(t, int64(2), fixture.counter.statements.Load())
	})

	t.Run("mapped values and bytes are never reused", func(t *testing.T) {
		fixture := graphCacheFixtureFor(t)
		var firstMapped, secondMapped atomic.Int64
		first := graphCacheChildPlan(t, graphCacheChildQuery(t, fixture, nil, "same", ""), "first", &firstMapped)
		second := graphCacheChildPlan(t, graphCacheChildQuery(t, fixture, nil, "same", ""), "second", &secondMapped)
		plan := graphCacheParentPlan(t, fixture, first, second, EdgeOptions{})
		values, err := LoadGraph(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Equal(t, int64(1), fixture.counter.statements.Load())
		require.Equal(t, int64(4), firstMapped.Load())
		require.Equal(t, int64(4), secondMapped.Load())
		require.Len(t, values[0].First.Values, 2)
		require.Len(t, values[0].Second.Values, 2)
		values[0].First.Values[0].Payload[0] = 'z'
		require.Equal(t, byte('a'), values[0].Second.Values[0].Payload[0])
	})

	t.Run("a many-through shared target clones direct bytes per attachment", func(t *testing.T) {
		fixture := graphCacheFixtureFor(t)
		var mapped atomic.Int64
		childPlan, err := NewGraphPlan(fixture.childQuery, func(row graphCacheChildRow) graphCacheChild {
			mapped.Add(1)
			return graphCacheChild{ID: row.ID, Rank: row.Rank, Payload: row.Payload}
		})
		require.NoError(t, err)
		type graph struct{ Children LoadedMany[graphCacheChild] }
		edge, err := ManyThrough("children", fixture.parentKey, fixture.junctionKey, fixture.throughKey, fixture.childIDKey, fixture.junction, childPlan, EdgeOptions{}, func(parent *graph, loaded LoadedMany[graphCacheChild]) {
			parent.Children = loaded
		})
		require.NoError(t, err)
		plan, err := NewGraphPlan(fixture.parentQuery, func(graphCacheParentRow) graph { return graph{} }, edge)
		require.NoError(t, err)
		values, err := LoadGraph(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 2)
		require.Equal(t, int64(1), fixture.counter.statements.Load())
		assert.Equal(t, int64(2), mapped.Load(), "each shared target attachment gets a fresh mapper input")
		for _, value := range values {
			require.Len(t, value.Children.Values, 1)
			require.Equal(t, int64(11), value.Children.Values[0].ID)
		}
		values[0].Children.Values[0].Payload[0] = 'z'
		require.Equal(t, byte('a'), values[1].Children.Values[0].Payload[0])
	})

	t.Run("a legacy zero-id fixed bind runs without cache reuse", func(t *testing.T) {
		fixture := graphCacheFixtureFor(t)
		fixed := querypkg.Bind(int64(0))
		predicate := Predicate{node: querypkg.Equal(fixture.childRank.node, fixed), source: fixture.childRank.source}
		firstQuery := fixture.childQuery.Where(predicate)
		secondQuery := fixture.childQuery.Where(predicate)
		compiled, err := Q1CompileQuery(graphCacheCompiler(t), firstQuery)
		require.NoError(t, err)
		require.Len(t, compiled.Slots, 1)
		require.Zero(t, compiled.Slots[0].ID)
		var firstMapped, secondMapped atomic.Int64
		first := graphCacheChildPlan(t, firstQuery, "first", &firstMapped)
		second := graphCacheChildPlan(t, secondQuery, "second", &secondMapped)
		plan := graphCacheParentPlan(t, fixture, first, second, EdgeOptions{})
		_, err = LoadGraph(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Equal(t, int64(2), fixture.counter.statements.Load())
		require.Equal(t, int64(2), firstMapped.Load())
		require.Equal(t, int64(2), secondMapped.Load())
	})

	t.Run("many-through unequal widths are rejected before execution", func(t *testing.T) {
		_, counter, _, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
		wideJunctionParent := Q1GraphKeyOf[mtJunctionRow](&graphkey.Spec{Parts: append(append([]*graphkey.PartSpec(nil), Q1GraphKeySpec(junctionParent).Parts...), Q1GraphKeySpec(junctionChild).Parts[0])})
		_, err := ManyThrough("one-to-two", parentKey, wideJunctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{}, func(*mtParentGraph, LoadedMany[mtChildGraph]) {})
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_graph_edge", planErr.Code)

		wideParent := Q1GraphKeyOf[mtParentRow](&graphkey.Spec{Parts: append(append([]*graphkey.PartSpec(nil), Q1GraphKeySpec(parentKey).Parts...), Q1GraphKeySpec(parentKey).Parts[0])})
		_, err = ManyThrough("two-to-one", wideParent, junctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{}, func(*mtParentGraph, LoadedMany[mtChildGraph]) {})
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_graph_edge", planErr.Code)

		require.Zero(t, counter.junctionStatements.Load())
		require.Zero(t, counter.targetStatements.Load())
	})
}

type graphCacheParentRow struct{ ID int64 }
type graphCacheChildRow struct {
	ID      int64
	Parent  int64
	Rank    int64
	Payload []byte
}
type graphCacheJunctionRow struct {
	Parent int64
	Child  int64
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
	return source.Scan(&value.ID, &value.Parent, &value.Rank, &value.Payload)
}

type graphCacheExecutor struct {
	Executor
	statements atomic.Int64
}

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
	// counter is the decorator that counts statements; executor is the same
	// decorator wrapped so it carries a compiler, which is what graph calls
	// need and what a decorator built from outside does not have on its own.
	counter      *graphCacheExecutor
	executor     Executor
	parentSource Source
	childSource  Source
	junction     Source
	parentQuery  Query[graphCacheParentRow]
	childQuery   Query[graphCacheChildRow]
	parentKey    GraphKey[graphCacheParentRow]
	childKey     GraphKey[graphCacheChildRow]
	childIDKey   GraphKey[graphCacheChildRow]
	junctionKey  GraphKey[graphCacheJunctionRow]
	throughKey   GraphKey[graphCacheJunctionRow]
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
CREATE TABLE graph_cache_junction (parent INTEGER NOT NULL, child INTEGER NOT NULL);
INSERT INTO graph_cache_parents VALUES (1), (2);
INSERT INTO graph_cache_children VALUES (11, 1, 0, X'61'), (12, 1, 1, X'62'), (21, 2, 0, X'63'), (22, 2, 1, X'64');
INSERT INTO graph_cache_junction VALUES (1, 11), (2, 11)`)
	require.NoError(t, err)
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	base, err := AsExecutor(db, profile)
	require.NoError(t, err)
	counter := &graphCacheExecutor{Executor: base}
	executor, err := WithEngineProfile(counter, profile)
	require.NoError(t, err)
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
	junction, err := SourceOf(MustReadTableOf[graphCacheJunctionRow](schema.TableDef{
		Name:    "graph_cache_junction",
		Columns: []schema.ColumnDef{{Name: "parent", Type: schema.IntegerType{}}, {Name: "child", Type: schema.IntegerType{}}},
	}), "j")
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
	junctionRelation := TypedRelation[graphCacheJunctionRow]{source: junction.Source()}
	junctionParent, err := BindColumn[graphCacheJunctionRow, int64](junctionRelation, "parent", "")
	require.NoError(t, err)
	junctionChild, err := BindColumn[graphCacheJunctionRow, int64](junctionRelation, "child", "")
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
	childKey := graphCacheDirectKey(childParent, func(row graphCacheChildRow) int64 { return row.Parent })
	childIDKey := graphCacheDirectKey(childID, func(row graphCacheChildRow) int64 { return row.ID })
	junctionKey, err := NewGraphKey(KeyPart(junctionParent, func(row graphCacheJunctionRow) int64 { return row.Parent }))
	require.NoError(t, err)
	throughKey, err := NewGraphKey(KeyPart(junctionChild, func(row graphCacheJunctionRow) int64 { return row.Child }))
	require.NoError(t, err)
	return graphCacheFixture{counter: counter, executor: executor, parentSource: parents.Source(), childSource: children.Source(), junction: junction.Source(), parentQuery: parentQuery, childQuery: childQuery, parentKey: parentKey, childKey: childKey, childIDKey: childIDKey, junctionKey: junctionKey, throughKey: throughKey, parentID: parentID.Expr(), childID: childID.Expr(), childParent: childParent.Expr(), childRank: childRank.Expr(), childPayload: childPayload.Expr()}
}

func graphCacheDirectKey[R any](column Column[R, int64], extract func(R) int64) GraphKey[R] {
	return GraphKey[R]{key: &graphkey.Spec{Parts: []*graphkey.PartSpec{{
		Column: column.ref, Codec: column.codec, Type: reflect.TypeOf(int64(0)),
		Extract:    func(row any) (any, bool) { return extract(row.(R)), true },
		ColumnType: graphkey.ColumnType(column.ref), Source: column.ref.Source().QualifiedName(),
	}}}}
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
		return graphCacheChild{ID: row.ID, Rank: row.Rank, Payload: row.Payload, Marker: marker}
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

// graphCacheCompiler renders for the engine the fixture runs against.
func graphCacheCompiler(t *testing.T) Compiler {
	t.Helper()
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	compiler, err := profile.Compiler(dialect.SQLite())
	require.NoError(t, err)
	return compiler
}
