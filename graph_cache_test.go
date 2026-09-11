package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"math"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/graphfingerprint"
	"github.com/lestrrat-go/rasql/internal/graphkey"
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
		plan := graphCacheParentPlan(t, fixture, first, second, rasql.EdgeOptions{BindLimit: 2})
		values, err := rasql.LoadGraph(t.Context(), executor, plan)
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
		plan := graphCacheParentPlan(t, fixture, first, second, rasql.EdgeOptions{BindLimit: 2})
		values, err := rasql.LoadGraph(t.Context(), executor, plan)
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
		plan := graphCacheParentPlan(t, fixture, first, second, rasql.EdgeOptions{})
		values, err := rasql.LoadGraph(t.Context(), fixture.executor, plan)
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
		plan = graphCacheParentPlan(t, fixture, first, second, rasql.EdgeOptions{})
		_, err = rasql.LoadGraph(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Equal(t, int64(2), fixture.counter.statements.Load())
	})

	t.Run("mapped values and bytes are never reused", func(t *testing.T) {
		fixture := graphCacheFixtureFor(t)
		var firstMapped, secondMapped atomic.Int64
		first := graphCacheChildPlan(t, graphCacheChildQuery(t, fixture, nil, "same", ""), "first", &firstMapped)
		second := graphCacheChildPlan(t, graphCacheChildQuery(t, fixture, nil, "same", ""), "second", &secondMapped)
		plan := graphCacheParentPlan(t, fixture, first, second, rasql.EdgeOptions{})
		values, err := rasql.LoadGraph(t.Context(), fixture.executor, plan)
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
		childPlan, err := rasql.NewGraphPlan(fixture.childQuery, func(row graphCacheChildRow) graphCacheChild {
			mapped.Add(1)
			return graphCacheChild{ID: row.ID, Rank: row.Rank, Payload: row.Payload}
		})
		require.NoError(t, err)
		type graph struct {
			Children rasql.LoadedMany[graphCacheChild]
		}
		edge, err := rasql.ManyThrough("children", fixture.parentKey, fixture.junctionKey, fixture.throughKey, fixture.childIDKey, fixture.junction, childPlan, rasql.EdgeOptions{}, func(parent *graph, loaded rasql.LoadedMany[graphCacheChild]) {
			parent.Children = loaded
		})
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(fixture.parentQuery, func(graphCacheParentRow) graph { return graph{} }, edge)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), fixture.executor, plan)
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
		predicate := rasql.Q1UnadoptedPredicate(fixture.childRank, int64(0))
		firstQuery := fixture.childQuery.Where(predicate)
		secondQuery := fixture.childQuery.Where(predicate)
		compiled, err := rasql.Q1CompileQuery(graphCacheCompiler(t), firstQuery)
		require.NoError(t, err)
		require.Len(t, compiled.Slots, 1)
		require.Zero(t, compiled.Slots[0].ID)
		var firstMapped, secondMapped atomic.Int64
		first := graphCacheChildPlan(t, firstQuery, "first", &firstMapped)
		second := graphCacheChildPlan(t, secondQuery, "second", &secondMapped)
		plan := graphCacheParentPlan(t, fixture, first, second, rasql.EdgeOptions{})
		_, err = rasql.LoadGraph(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Equal(t, int64(2), fixture.counter.statements.Load())
		require.Equal(t, int64(2), firstMapped.Load())
		require.Equal(t, int64(2), secondMapped.Load())
	})

	t.Run("many-through unequal widths are rejected before execution", func(t *testing.T) {
		_, counter, _, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
		wideJunctionParent := rasql.Q1GraphKeyOf[mtJunctionRow](&graphkey.Spec{Parts: append(append([]*graphkey.PartSpec(nil), rasql.Q1GraphKeySpec(junctionParent).Parts...), rasql.Q1GraphKeySpec(junctionChild).Parts[0])})
		_, err := rasql.ManyThrough("one-to-two", parentKey, wideJunctionParent, junctionChild, childKey, junction, childPlan, rasql.EdgeOptions{}, func(*mtParentGraph, rasql.LoadedMany[mtChildGraph]) {})
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_graph_edge", planErr.Code)

		wideParent := rasql.Q1GraphKeyOf[mtParentRow](&graphkey.Spec{Parts: append(append([]*graphkey.PartSpec(nil), rasql.Q1GraphKeySpec(parentKey).Parts...), rasql.Q1GraphKeySpec(parentKey).Parts[0])})
		_, err = rasql.ManyThrough("two-to-one", wideParent, junctionParent, junctionChild, childKey, junction, childPlan, rasql.EdgeOptions{}, func(*mtParentGraph, rasql.LoadedMany[mtChildGraph]) {})
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_graph_edge", planErr.Code)

		require.Zero(t, counter.junctionStatements.Load())
		require.Zero(t, counter.targetStatements.Load())
	})
	t.Run("direct duplicate parents map fresh rows", func(t *testing.T) {
		fixture := graphCacheFixtureFor(t)
		executor := graphCacheProfiled(t, &graphDuplicateParentExecutor{Executor: fixture.executor})
		var mapped atomic.Int64
		children, err := rasql.NewGraphPlan(fixture.childQuery, func(row graphCacheChildRow) graphCacheChild {
			call := mapped.Add(1)
			seen := row.Payload[0]
			if call == 1 {
				row.Payload[0] = 'z'
			}
			return graphCacheChild{ID: row.ID, Payload: []byte{seen}}
		})
		require.NoError(t, err)
		edge, err := rasql.HasMany("children", fixture.parentKey, fixture.childKey, children, rasql.EdgeOptions{}, func(parent *graphCacheParent, loaded rasql.LoadedMany[graphCacheChild]) {
			parent.First = loaded
		})
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(fixture.parentQuery, func(graphCacheParentRow) graphCacheParent { return graphCacheParent{} }, edge)
		require.NoError(t, err)

		values, err := rasql.LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 2)
		require.Equal(t, int64(4), mapped.Load())
		for _, value := range values {
			require.Len(t, value.First.Values, 2)
		}
		values[0].First.Values[0].Payload[0] = 'z'
		require.Equal(t, byte('a'), values[1].First.Values[0].Payload[0])
	})

	t.Run("a many-through cache normalizes keys once", func(t *testing.T) {
		fixture := graphCacheFixtureFor(t)
		parentCodec := &graphCacheRuntimeCodec{}
		targetCodec := &graphCacheRuntimeCodec{}
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{
			"graph.runtime.parent": parentCodec,
			"graph.runtime.target": targetCodec,
		})
		require.NoError(t, err)
		withCodecs, err := rasql.WithCodecs(fixture.executor, registry)
		require.NoError(t, err)
		executor := graphCacheProfiled(t, &graphDuplicateParentExecutor{Executor: withCodecs})

		parentKey := graphCacheCodecKey(fixture.parentKey, "graph.runtime.parent")
		junctionParent := graphCacheCodecKey(fixture.junctionKey, "graph.runtime.parent")
		junctionChild := graphCacheCodecKey(fixture.throughKey, "graph.runtime.target")
		childKey := graphCacheCodecKey(fixture.childIDKey, "graph.runtime.target")
		children, err := rasql.NewGraphPlan(fixture.childQuery, func(row graphCacheChildRow) graphCacheChild {
			return graphCacheChild{ID: row.ID, Payload: row.Payload}
		})
		require.NoError(t, err)
		first, err := rasql.ManyThrough("first", parentKey, junctionParent, junctionChild, childKey, fixture.junction, children, rasql.EdgeOptions{}, func(parent *graphCacheParent, loaded rasql.LoadedMany[graphCacheChild]) {
			parent.First = loaded
		})
		require.NoError(t, err)
		second, err := rasql.ManyThrough("second", parentKey, junctionParent, junctionChild, childKey, fixture.junction, children, rasql.EdgeOptions{}, func(parent *graphCacheParent, loaded rasql.LoadedMany[graphCacheChild]) {
			parent.Second = loaded
		})
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(fixture.parentQuery, func(graphCacheParentRow) graphCacheParent { return graphCacheParent{} }, first, second)
		require.NoError(t, err)

		values, err := rasql.LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 2)
		require.Equal(t, int64(5), parentCodec.enc.Load())
		require.Equal(t, int64(2), targetCodec.enc.Load())
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
	First  rasql.LoadedMany[graphCacheChild]
	Second rasql.LoadedMany[graphCacheChild]
}
type graphCacheChild struct {
	ID      int64
	Rank    int64
	Payload []byte
	Marker  string
}

type graphCacheParentDecoder struct{ schema rasql.ResultSchema }

func (d graphCacheParentDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (graphCacheParentDecoder) Presence() []rasql.Presence         { return nil }
func (d graphCacheParentDecoder) DecodeRow(source rasql.ScanSource, value *graphCacheParentRow) error {
	return source.Scan(&value.ID)
}

type graphCacheChildDecoder struct {
	schema rasql.ResultSchema
	mode   string
}

func (d graphCacheChildDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (graphCacheChildDecoder) Presence() []rasql.Presence         { return nil }
func (d graphCacheChildDecoder) DecodeRow(source rasql.ScanSource, value *graphCacheChildRow) error {
	return source.Scan(&value.ID, &value.Parent, &value.Rank, &value.Payload)
}

type graphCacheExecutor struct {
	rasql.Executor
	statements atomic.Int64
}

func (e *graphCacheExecutor) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
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
	executor     rasql.Executor
	parentSource rasql.Source
	childSource  rasql.Source
	junction     rasql.Source
	parentQuery  rasql.Query[graphCacheParentRow]
	childQuery   rasql.Query[graphCacheChildRow]
	parentKey    rasql.GraphKey[graphCacheParentRow]
	childKey     rasql.GraphKey[graphCacheChildRow]
	childIDKey   rasql.GraphKey[graphCacheChildRow]
	junctionKey  rasql.GraphKey[graphCacheJunctionRow]
	throughKey   rasql.GraphKey[graphCacheJunctionRow]
	parentID     rasql.Expr[int64]
	childID      rasql.Expr[int64]
	childParent  rasql.Expr[int64]
	childRank    rasql.Expr[int64]
	childPayload rasql.Expr[[]byte]
	// childItems and childSchema rebuild the child projection with a different
	// decoder, which is what varies the cache identity across subtests.
	childItems  []rasql.ProjectionItem
	childSchema rasql.ResultSchema
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
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	base, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	counter := &graphCacheExecutor{Executor: base}
	executor, err := rasql.WithEngineProfile(counter, profile)
	require.NoError(t, err)
	parents, err := rasql.SourceOf(rasql.MustReadTableOf[graphCacheParentRow](schema.TableDef{
		Name: "graph_cache_parents", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	}), "p")
	require.NoError(t, err)
	children, err := rasql.SourceOf(rasql.MustReadTableOf[graphCacheChildRow](schema.TableDef{
		Name: "graph_cache_children", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}},
			{Name: "rank", Type: schema.IntegerType{}}, {Name: "payload", Type: schema.BytesType{}},
		},
	}), "c")
	require.NoError(t, err)
	junction, err := rasql.SourceOf(rasql.MustReadTableOf[graphCacheJunctionRow](schema.TableDef{
		Name:    "graph_cache_junction",
		Columns: []schema.ColumnDef{{Name: "parent", Type: schema.IntegerType{}}, {Name: "child", Type: schema.IntegerType{}}},
	}), "j")
	require.NoError(t, err)
	parentRelation := rasql.Q1TypedRelation[graphCacheParentRow](parents.Source())
	childRelation := rasql.Q1TypedRelation[graphCacheChildRow](children.Source())
	parentID, err := rasql.BindColumn[graphCacheParentRow, int64](parentRelation, "id", "")
	require.NoError(t, err)
	childID, err := rasql.BindColumn[graphCacheChildRow, int64](childRelation, "id", "")
	require.NoError(t, err)
	childParent, err := rasql.BindColumn[graphCacheChildRow, int64](childRelation, "parent", "")
	require.NoError(t, err)
	childRank, err := rasql.BindColumn[graphCacheChildRow, int64](childRelation, "rank", "")
	require.NoError(t, err)
	childPayload, err := rasql.BindColumn[graphCacheChildRow, []byte](childRelation, "payload", "")
	require.NoError(t, err)
	junctionRelation := rasql.Q1TypedRelation[graphCacheJunctionRow](junction.Source())
	junctionParent, err := rasql.BindColumn[graphCacheJunctionRow, int64](junctionRelation, "parent", "")
	require.NoError(t, err)
	junctionChild, err := rasql.BindColumn[graphCacheJunctionRow, int64](junctionRelation, "child", "")
	require.NoError(t, err)
	parentSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	parentProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", parentID.Expr(), schema.IntegerType{}, "")}, graphCacheParentDecoder{schema: parentSchema})
	require.NoError(t, err)
	childSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "parent", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "rank", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "payload", Type: schema.BytesType{}},
	)
	require.NoError(t, err)
	childItems := []rasql.ProjectionItem{
		rasql.Item("id", childID.Expr(), schema.IntegerType{}, ""),
		rasql.Item("parent", childParent.Expr(), schema.IntegerType{}, ""),
		rasql.Item("rank", childRank.Expr(), schema.IntegerType{}, ""),
		rasql.Item("payload", childPayload.Expr(), schema.BytesType{}, ""),
	}
	childProjection, err := rasql.NewProjection(childItems, graphCacheChildDecoder{schema: childSchema})
	require.NoError(t, err)
	parentQuery := rasql.Select(parents.Source(), parentProjection).OrderBy(rasql.AscExpr(parentID.Expr()))
	childQuery := rasql.Select(children.Source(), childProjection).OrderBy(rasql.AscExpr(childRank.Expr()), rasql.AscExpr(childID.Expr()))
	parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphCacheParentRow) int64 { return row.ID }))
	require.NoError(t, err)
	childKey := graphCacheDirectKey(t, childParent, func(row graphCacheChildRow) int64 { return row.Parent })
	childIDKey := graphCacheDirectKey(t, childID, func(row graphCacheChildRow) int64 { return row.ID })
	junctionKey, err := rasql.NewGraphKey(rasql.KeyPart(junctionParent, func(row graphCacheJunctionRow) int64 { return row.Parent }))
	require.NoError(t, err)
	throughKey, err := rasql.NewGraphKey(rasql.KeyPart(junctionChild, func(row graphCacheJunctionRow) int64 { return row.Child }))
	require.NoError(t, err)
	return graphCacheFixture{counter: counter, executor: executor, parentSource: parents.Source(), childSource: children.Source(), junction: junction.Source(), parentQuery: parentQuery, childQuery: childQuery, parentKey: parentKey, childKey: childKey, childIDKey: childIDKey, junctionKey: junctionKey, throughKey: throughKey, parentID: parentID.Expr(), childID: childID.Expr(), childParent: childParent.Expr(), childRank: childRank.Expr(), childPayload: childPayload.Expr(), childItems: childItems, childSchema: childSchema}
}

func graphCacheDirectKey[R any](t *testing.T, column rasql.Column[R, int64], extract func(R) int64) rasql.GraphKey[R] {
	t.Helper()
	key, err := rasql.NewGraphKey(rasql.KeyPart(column, extract))
	require.NoError(t, err)
	return key
}

func graphCacheChildQuery(t *testing.T, fixture graphCacheFixture, rank *int64, mode string, codec string) rasql.Query[graphCacheChildRow] {
	t.Helper()
	projection, err := rasql.NewProjection(fixture.childItems, graphCacheChildDecoder{schema: fixture.childSchema, mode: mode})
	require.NoError(t, err)
	query := rasql.Select(fixture.childSource, projection).OrderBy(rasql.AscExpr(fixture.childRank), rasql.AscExpr(fixture.childID))
	if rank != nil {
		value, err := rasql.ValueWithCodec(*rank, codec)
		require.NoError(t, err)
		query = query.Where(rasql.EqualExpr(fixture.childRank, value))
	}
	return query
}

func graphCacheChildPlan(t *testing.T, query rasql.Query[graphCacheChildRow], marker string, mapped *atomic.Int64) rasql.GraphPlan[graphCacheChildRow, graphCacheChild] {
	t.Helper()
	plan, err := rasql.NewGraphPlan(query, func(row graphCacheChildRow) graphCacheChild {
		mapped.Add(1)
		return graphCacheChild{ID: row.ID, Rank: row.Rank, Payload: row.Payload, Marker: marker}
	})
	require.NoError(t, err)
	return plan
}

func graphCacheParentPlan(t *testing.T, fixture graphCacheFixture, first, second rasql.GraphPlan[graphCacheChildRow, graphCacheChild], options rasql.EdgeOptions) rasql.GraphPlan[graphCacheParentRow, graphCacheParent] {
	t.Helper()
	edge, err := rasql.HasMany("first", fixture.parentKey, fixture.childKey, first, options, func(parent *graphCacheParent, loaded rasql.LoadedMany[graphCacheChild]) { parent.First = loaded })
	require.NoError(t, err)
	secondEdge, err := rasql.HasMany("second", fixture.parentKey, fixture.childKey, second, options, func(parent *graphCacheParent, loaded rasql.LoadedMany[graphCacheChild]) { parent.Second = loaded })
	require.NoError(t, err)
	plan, err := rasql.NewGraphPlan(fixture.parentQuery, func(row graphCacheParentRow) graphCacheParent { return graphCacheParent{} }, edge, secondEdge)
	require.NoError(t, err)
	return plan
}

func graphCacheExecutorWithCodec(t *testing.T, fixture graphCacheFixture, codec *graphCacheCodec) rasql.Executor {
	t.Helper()
	registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"graph.cache.fixed": codec})
	require.NoError(t, err)
	executor, err := rasql.WithCodecs(fixture.executor, registry)
	require.NoError(t, err)
	return executor
}

// graphCacheCompiler renders for the engine the fixture runs against.
func graphCacheCompiler(t *testing.T) rasql.Compiler {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	compiler, err := profile.Compiler(dialect.SQLite())
	require.NoError(t, err)
	return compiler
}

type graphDuplicateParentExecutor struct {
	rasql.Executor
}

func (e *graphDuplicateParentExecutor) Codecs() rasql.CodecRegistry {
	provider, ok := e.Executor.(rasql.CodecProvider)
	if !ok {
		empty, err := rasql.NewCodecRegistry(nil)
		if err != nil {
			panic(err)
		}
		return empty
	}
	return provider.Codecs()
}

func (e *graphDuplicateParentExecutor) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
	if strings.Contains(statement.SQL(), "graph_cache_parents") {
		return &runtimeFakeRows{columns: []string{"id"}, values: [][]any{{int64(1)}, {int64(1)}}}, nil
	}
	return e.Executor.Query(ctx, statement)
}

type graphCacheRuntimeCodec struct{ enc atomic.Int64 }

func (c *graphCacheRuntimeCodec) Encode(value any) (driver.Value, error) {
	c.enc.Add(1)
	return value, nil
}
func (*graphCacheRuntimeCodec) Decode(value any, destination any) error {
	switch destination := destination.(type) {
	case *any:
		*destination = value
	case *int64:
		*destination = value.(int64)
	}
	return nil
}
func graphCacheCodecKey[R any](base rasql.GraphKey[R], codec string) rasql.GraphKey[R] {
	part := *rasql.Q1GraphKeySpec(base).Parts[0]
	part.Codec = codec
	return rasql.Q1GraphKeyOf[R](&graphkey.Spec{Parts: []*graphkey.PartSpec{&part}})
}

// graphCacheProfiled gives a decorator built outside this package the compiler
// that graph calls need, which is what a bare decorator does not carry.
func graphCacheProfiled(t *testing.T, executor rasql.Executor) rasql.Executor {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	profiled, err := rasql.WithEngineProfile(executor, profile)
	require.NoError(t, err)
	return profiled
}
