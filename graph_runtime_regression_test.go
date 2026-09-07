package rasql

import (
	"context"
	"database/sql/driver"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type graphRuntimeCountingExecutor struct {
	Executor
	compiler *querycompile.Compiler
	queries  atomic.Int64
}

type graphDuplicateParentExecutor struct {
	Executor
	compiler *querycompile.Compiler
}

func (e *graphDuplicateParentExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }

func (e *graphDuplicateParentExecutor) Codecs() CodecRegistry {
	provider, ok := e.Executor.(CodecProvider)
	if !ok {
		return builtinCodecs
	}
	return provider.Codecs()
}

func (e *graphDuplicateParentExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	if strings.Contains(statement.SQL(), "graph_cache_parents") {
		return &runtimeFakeRows{columns: []string{"id"}, values: [][]any{{int64(1)}, {int64(1)}}}, nil
	}
	return e.Executor.Query(ctx, statement)
}

type graphRuntimeCodec struct{ enc atomic.Int64 }

func (c *graphRuntimeCodec) Encode(value any) (driver.Value, error) {
	c.enc.Add(1)
	return value, nil
}
func (*graphRuntimeCodec) Decode(value any, destination any) error {
	switch destination := destination.(type) {
	case *any:
		*destination = value
	case *int64:
		*destination = value.(int64)
	}
	return nil
}

func (e *graphRuntimeCountingExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }

func (e *graphRuntimeCountingExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	e.queries.Add(1)
	return e.Executor.Query(ctx, statement)
}

func TestGraphPreflightValidatesSharedChildEdgesBeforeRootQuery(t *testing.T) {
	base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	executor := &graphRuntimeCountingExecutor{Executor: base, compiler: provider.queryCompiler()}

	parents := TypedRelation[graphParentRow]{source: parentSource}
	children := TypedRelation[graphChildRow]{source: childSource}
	parentID, err := BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childParent, err := BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	parentKey, err := NewGraphKey(
		KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }),
		NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }),
	)
	require.NoError(t, err)
	childKey, err := NewGraphKey(
		KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }),
		KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }),
	)
	require.NoError(t, err)
	childrenPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
	require.NoError(t, err)
	first, err := HasMany("first", parentKey, childKey, childrenPlan, EdgeOptions{}, func(*graphParent, LoadedMany[graphChild]) {})
	require.NoError(t, err)
	second, err := HasMany("second", parentKey, childKey, childrenPlan, EdgeOptions{BindLimit: 1}, func(*graphParent, LoadedMany[graphChild]) {})
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(graphParentRow) graphParent { return graphParent{} }, first, second)
	require.NoError(t, err)

	_, err = LoadGraph(t.Context(), executor, plan)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "bind_limit", planErr.Code)
	require.Zero(t, executor.queries.Load())
}

func TestGraphDirectDuplicateParentsMapFreshRows(t *testing.T) {
	fixture := graphCacheFixtureFor(t)
	executor := &graphDuplicateParentExecutor{Executor: fixture.executor, compiler: fixture.executor.compiler}
	var mapped atomic.Int64
	children, err := NewGraphPlan(fixture.childQuery, func(row graphCacheChildRow) graphCacheChild {
		call := mapped.Add(1)
		seen := row.Payload[0]
		if call == 1 {
			row.Payload[0] = 'z'
		}
		return graphCacheChild{ID: row.ID, Payload: []byte{seen}}
	})
	require.NoError(t, err)
	edge, err := HasMany("children", fixture.parentKey, fixture.childKey, children, EdgeOptions{}, func(parent *graphCacheParent, loaded LoadedMany[graphCacheChild]) {
		parent.First = loaded
	})
	require.NoError(t, err)
	plan, err := NewGraphPlan(fixture.parentQuery, func(graphCacheParentRow) graphCacheParent { return graphCacheParent{} }, edge)
	require.NoError(t, err)

	values, err := LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 2)
	require.Equal(t, int64(4), mapped.Load())
	for _, value := range values {
		require.Len(t, value.First.Values, 2)
	}
	values[0].First.Values[0].Payload[0] = 'z'
	require.Equal(t, byte('a'), values[1].First.Values[0].Payload[0])
}

func TestGraphManyThroughCacheNormalizesKeysOnce(t *testing.T) {
	fixture := graphCacheFixtureFor(t)
	parentCodec := &graphRuntimeCodec{}
	targetCodec := &graphRuntimeCodec{}
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{
		"graph.runtime.parent": parentCodec,
		"graph.runtime.target": targetCodec,
	})
	require.NoError(t, err)
	withCodecs, err := WithCodecs(fixture.executor, registry)
	require.NoError(t, err)
	provider, ok := withCodecs.(compilerProvider)
	require.True(t, ok)
	executor := &graphDuplicateParentExecutor{Executor: withCodecs, compiler: provider.queryCompiler()}

	parentKey := graphRuntimeCodecKey(fixture.parentKey, "graph.runtime.parent")
	junctionParent := graphRuntimeCodecKey(fixture.junctionKey, "graph.runtime.parent")
	junctionChild := graphRuntimeCodecKey(fixture.throughKey, "graph.runtime.target")
	childKey := graphRuntimeCodecKey(fixture.childIDKey, "graph.runtime.target")
	children, err := NewGraphPlan(fixture.childQuery, func(row graphCacheChildRow) graphCacheChild {
		return graphCacheChild{ID: row.ID, Payload: row.Payload}
	})
	require.NoError(t, err)
	first, err := ManyThrough("first", parentKey, junctionParent, junctionChild, childKey, fixture.junction, children, EdgeOptions{}, func(parent *graphCacheParent, loaded LoadedMany[graphCacheChild]) {
		parent.First = loaded
	})
	require.NoError(t, err)
	second, err := ManyThrough("second", parentKey, junctionParent, junctionChild, childKey, fixture.junction, children, EdgeOptions{}, func(parent *graphCacheParent, loaded LoadedMany[graphCacheChild]) {
		parent.Second = loaded
	})
	require.NoError(t, err)
	plan, err := NewGraphPlan(fixture.parentQuery, func(graphCacheParentRow) graphCacheParent { return graphCacheParent{} }, first, second)
	require.NoError(t, err)

	values, err := LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 2)
	require.Equal(t, int64(5), parentCodec.enc.Load())
	require.Equal(t, int64(2), targetCodec.enc.Load())
}

func graphRuntimeCodecKey[R any](base GraphKey[R], codec string) GraphKey[R] {
	part := *base.key.parts[0]
	part.codec = codec
	return GraphKey[R]{key: &graphKeySpec{parts: []*graphKeyPartSpec{&part}}}
}

func TestGraphMapperPanicReportsDecodedRowCount(t *testing.T) {
	base, _, _, _, parentQuery, _ := graphAcceptanceFixture(t, 1)
	parentQuery, err := parentQuery.Limit(1)
	require.NoError(t, err)
	var terminal Event
	observed, err := WithEventObservers(base, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
		if event.Kind != EventGraph {
			return ctx, nil
		}
		return ctx, EventCompletionFunc(func(_ context.Context, event Event) error {
			terminal = event
			return nil
		})
	}))
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(graphParentRow) graphParent { panic("mapper panic") })
	require.NoError(t, err)
	require.Panics(t, func() { _, _ = LoadGraph(t.Context(), observed, plan) })
	require.Equal(t, EventGraph, terminal.Kind)
	require.Equal(t, EventTerminal, terminal.Phase)
	require.Equal(t, int64(1), terminal.Rows)
}

func TestGraphDirectChildMapperPanicReportsDecodedRowCount(t *testing.T) {
	base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
	parents := TypedRelation[graphParentRow]{source: parentSource}
	children := TypedRelation[graphChildRow]{source: childSource}
	parentID, err := BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childParent, err := BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	parentKey, err := NewGraphKey(
		KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }),
		NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }),
	)
	require.NoError(t, err)
	childKey, err := NewGraphKey(
		KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }),
		KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }),
	)
	require.NoError(t, err)
	childrenPlan, err := NewGraphPlan(childQuery, func(graphChildRow) graphChild { panic("child mapper panic") })
	require.NoError(t, err)
	edge, err := HasMany("children", parentKey, childKey, childrenPlan, EdgeOptions{}, func(*graphParent, LoadedMany[graphChild]) {})
	require.NoError(t, err)
	parentQuery, err = parentQuery.Limit(1)
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(graphParentRow) graphParent { return graphParent{} }, edge)
	require.NoError(t, err)

	var terminal Event
	observed, err := WithEventObservers(base, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
		if event.Kind != EventGraph {
			return ctx, nil
		}
		return ctx, EventCompletionFunc(func(_ context.Context, event Event) error {
			terminal = event
			return nil
		})
	}))
	require.NoError(t, err)
	require.Panics(t, func() { _, _ = LoadGraph(t.Context(), observed, plan) })
	require.Equal(t, EventGraph, terminal.Kind)
	require.Equal(t, EventTerminal, terminal.Phase)
	require.Equal(t, int64(2), terminal.Rows)
}
