package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type lifecycleExecutor struct {
	Executor
	compiler *querycompile.Compiler
	rows     []*runtimeFakeRows
	calls    atomic.Int64
	onQuery  func(context.Context)
}

func (e *lifecycleExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }
func (e *lifecycleExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	index := int(e.calls.Add(1)) - 1
	if e.onQuery != nil {
		e.onQuery(ctx)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if index >= len(e.rows) {
		return nil, errors.New("unexpected later graph query")
	}
	return e.rows[index], nil
}

func lifecycleGraphPlan(t *testing.T, base Executor, one bool, attach func(*graphParent, LoadedMany[graphChild])) GraphPlan[graphParentRow, graphParent] {
	t.Helper()
	_, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
	var err error
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
	parentKey, err := NewGraphKey(KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }))
	require.NoError(t, err)
	childKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	childPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID, Rank: row.Rank} })
	require.NoError(t, err)
	edge, err := HasMany("children", parentKey, childKey, childPlan, EdgeOptions{}, attach)
	if one {
		edge, err = HasOne("child", parentKey, childKey, childPlan, EdgeOptions{}, func(parent *graphParent, loaded LoadedOne[graphChild]) {
			if loaded.Present {
				attach(parent, LoadedMany[graphChild]{Loaded: loaded.Loaded, Values: []graphChild{*loaded.Value}})
				return
			}
			attach(parent, LoadedMany[graphChild]{Loaded: loaded.Loaded})
		})
	}
	require.NoError(t, err)
	limitedParent, err := parentQuery.Limit(1)
	require.NoError(t, err)
	plan, err := NewGraphPlan(limitedParent, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
	require.NoError(t, err)
	return plan
}

func TestGraphLifecycle(t *testing.T) {
	t.Run("emits ordered logical and statement events", func(t *testing.T) {
		executor, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 4)
		var err error
		parentQuery, err = parentQuery.Limit(1)
		require.NoError(t, err)
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
		parentKey, err := NewGraphKey(KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }))
		require.NoError(t, err)
		childKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		childPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
		require.NoError(t, err)
		edge, err := HasMany("children", parentKey, childKey, childPlan, EdgeOptions{PerParentLimit: 2}, func(parent *graphParent, loaded LoadedMany[graphChild]) { parent.Children = loaded })
		require.NoError(t, err)
		plan, err := NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.NoError(t, err)
		type contextKey struct{}
		var marker contextKey
		var mu sync.Mutex
		var events []Event
		terminalDerived := false
		var observerError = errors.New("observer completion failed")
		executor, err = WithEventObservers(executor,
			ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}),
			EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
				return context.WithValue(ctx, marker, true), EventCompletionFunc(func(completionCtx context.Context, terminal Event) error {
					if completionCtx.Value(marker) == true {
						terminalDerived = true
					}
					mu.Lock()
					events = append(events, terminal)
					mu.Unlock()
					if terminal.Kind == EventGraph && terminal.Phase == EventTerminal && terminal.Err == nil {
						return observerError
					}
					return nil
				})
			}),
		)
		require.NoError(t, err)
		values, err := LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 1)

		mu.Lock()
		defer mu.Unlock()
		require.NotEmpty(t, events)
		require.Equal(t, EventGraph, events[0].Kind)
		require.Equal(t, EventStart, events[0].Phase)
		graphID := events[0].LogicalID
		var statementStarts, statementTerminals int
		for _, event := range events {
			if event.Kind == EventStatement && event.Phase == EventStart {
				require.Equal(t, graphID, event.ParentID)
				require.Equal(t, statementStarts, event.StatementIndex)
				statementStarts++
			}
			if event.Kind == EventStatement && event.Phase == EventTerminal {
				statementTerminals++
			}
		}
		require.Positive(t, statementStarts)
		require.Equal(t, statementStarts, statementTerminals)
		require.True(t, terminalDerived)
	})

	t.Run("a decode failure stops later queries and joins cleanup", func(t *testing.T) {
		base, _, _, _, _, _ := graphAcceptanceFixture(t, 1)
		provider, ok := base.(compilerProvider)
		require.True(t, ok)
		closeErr := errors.New("child close")
		finishErr := errors.New("child finish")
		childRows := &runtimeFakeRows{columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{{int64(11)}}, finishErr: errors.Join(closeErr, finishErr)}
		// The short child row makes the real decoder return the primary scan error.
		executor := &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{
			{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}}, childRows,
		}}
		plan := lifecycleGraphPlan(t, base, false, func(*graphParent, LoadedMany[graphChild]) {})
		_, err := LoadGraph(t.Context(), executor, plan)
		require.Error(t, err)
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.ErrorIs(t, err, closeErr)
		require.ErrorIs(t, err, finishErr)
		require.Equal(t, int64(2), executor.calls.Load())
		require.Equal(t, 1, childRows.closed)
		require.Equal(t, 1, childRows.finished)
	})

	t.Run("cancellation stops later breadth levels", func(t *testing.T) {
		base, _, _, _, _, _ := graphAcceptanceFixture(t, 1)
		provider, ok := base.(compilerProvider)
		require.True(t, ok)
		cancelCtx, cancel := context.WithCancel(t.Context())
		defer cancel()
		executor := &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{
			{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}}, {columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{{int64(11), int64(1), int64(1), int64(0)}}},
		}}
		executor.onQuery = func(ctx context.Context) {
			if executor.calls.Load() == 1 {
				cancel()
			}
		}
		plan := lifecycleGraphPlan(t, base, false, func(*graphParent, LoadedMany[graphChild]) {})
		_, err := LoadGraph(cancelCtx, executor, plan)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, int64(1), executor.calls.Load())
	})

	t.Run("a second has-one row stops the attachment", func(t *testing.T) {
		base, _, _, _, _, _ := graphAcceptanceFixture(t, 1)
		provider, ok := base.(compilerProvider)
		require.True(t, ok)
		childRows := &runtimeFakeRows{columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{{int64(11), int64(1), int64(1), int64(0)}, {int64(12), int64(1), int64(1), int64(1)}}}
		executor := &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{
			{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}}, childRows,
		}}
		plan := lifecycleGraphPlan(t, base, true, func(*graphParent, LoadedMany[graphChild]) {})
		_, err := LoadGraph(t.Context(), executor, plan)
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "cardinality", planErr.Code)
		require.Equal(t, int64(2), executor.calls.Load())
		require.Equal(t, 1, childRows.closed)
		require.Equal(t, 1, childRows.finished)
	})

	t.Run("mapper and attachment panics propagate", func(t *testing.T) {
		base, _, _, _, parentQuery, _ := graphAcceptanceFixture(t, 1)
		provider, ok := base.(compilerProvider)
		require.True(t, ok)
		newExecutor := func() *lifecycleExecutor {
			return &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}}}}
		}
		newAttachmentExecutor := func() *lifecycleExecutor {
			return &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{
				{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}},
				{columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{{int64(11), int64(1), int64(1), int64(0)}}},
			}}
		}
		parents := parentQuery
		limitedParents, err := parents.Limit(1)
		require.NoError(t, err)
		mapperPlan, err := NewGraphPlan(limitedParents, func(graphParentRow) graphParent { panic("mapper panic") })
		require.NoError(t, err)
		require.Panics(t, func() { _, _ = LoadGraph(t.Context(), newExecutor(), mapperPlan) })
		plan := lifecycleGraphPlan(t, base, false, func(*graphParent, LoadedMany[graphChild]) { panic("attachment panic") })
		require.Panics(t, func() { _, _ = LoadGraph(t.Context(), newAttachmentExecutor(), plan) })
	})
}

type graphContractCodec struct{ enc *atomic.Int64 }

func (c graphContractCodec) Encode(value any) (driver.Value, error) {
	c.enc.Add(1)
	return value, nil
}
func (graphContractCodec) Decode(source any, destination any) error { return nil }

func graphContractPlan(t *testing.T, order bool) (Executor, GraphPlan[graphParentRow, graphParent], GraphKey[graphParentRow], GraphKey[graphChildRow], Source, Source) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	parentTable := MustReadTableOf[graphParentRow](schema.TableDef{Name: "contract_parents", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true}}, PrimaryKey: []string{"id"}})
	childTable := MustReadTableOf[graphChildRow](schema.TableDef{Name: "contract_children", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	parents, err := SourceOf(parentTable, "p")
	require.NoError(t, err)
	children, err := SourceOf(childTable, "c")
	require.NoError(t, err)
	parentID, err := BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childParent, err := BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	childID, err := BindColumn[graphChildRow, int64](children, "id", "")
	require.NoError(t, err)
	childRank, err := BindColumn[graphChildRow, int64](children, "rank", "")
	require.NoError(t, err)
	parentSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "tenant", Type: schema.IntegerType{}, Nullable: true})
	require.NoError(t, err)
	parentProjection, err := NewProjection([]ProjectionItem{Item("id", parentID.Expr(), schema.IntegerType{}, ""), NullItem("tenant", parentTenant.NullExpr(), schema.IntegerType{}, "")}, graphParentDecoder{schema: parentSchema})
	require.NoError(t, err)
	childSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "parent", Type: schema.IntegerType{}}, ResultColumn{Name: "tenant", Type: schema.IntegerType{}}, ResultColumn{Name: "rank", Type: schema.IntegerType{}})
	require.NoError(t, err)
	childProjection, err := NewProjection([]ProjectionItem{Item("id", childID.Expr(), schema.IntegerType{}, ""), Item("parent", childParent.Expr(), schema.IntegerType{}, ""), Item("tenant", childTenant.Expr(), schema.IntegerType{}, ""), Item("rank", childRank.Expr(), schema.IntegerType{}, "")}, graphChildDecoder{schema: childSchema})
	require.NoError(t, err)
	parentQuery := Select(parents.Source(), parentProjection).OrderBy(AscExpr(parentID.Expr()))
	childQuery := Select(children.Source(), childProjection)
	if order {
		childQuery = childQuery.OrderBy(AscExpr(childRank.Expr()), AscExpr(childID.Expr()))
	} else {
		childQuery = childQuery.OrderBy(AscExpr(childRank.Expr()))
	}
	parentKey, err := NewGraphKey(KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }))
	require.NoError(t, err)
	childKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	childPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID, Rank: row.Rank} })
	require.NoError(t, err)
	edge, err := HasMany("children", parentKey, childKey, childPlan, EdgeOptions{PerParentLimit: 5}, func(parent *graphParent, loaded LoadedMany[graphChild]) { parent.Children = loaded })
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
	require.NoError(t, err)
	return executor, plan, parentKey, childKey, parents.Source(), children.Source()
}

func TestGraphContract(t *testing.T) {
	t.Run("rejects metadata and order violations", func(t *testing.T) {
		_, plan, parentKey, childKey, parentSource, childSource := graphContractPlan(t, true)
		require.NotNil(t, plan.node)
		unorderedExecutor, unordered, _, _, _, _ := graphContractPlan(t, false)
		_, err := LoadGraph(t.Context(), unorderedExecutor, unordered)
		var orderErr *PlanError
		require.ErrorAs(t, err, &orderErr)
		require.Equal(t, "order_not_unique", orderErr.Code)
		_, err = NewGraphKey[graphParentRow]()
		require.Error(t, err)

		shortKey := GraphKey[graphChildRow]{key: &graphKeySpec{parts: childKey.key.parts[:1]}}
		childPlan := GraphPlan[graphChildRow, graphChild]{node: plan.node.edges[0].child}
		edge, err := HasMany("wrong-width", parentKey, shortKey, childPlan, EdgeOptions{}, func(*graphParent, LoadedMany[graphChild]) {})
		require.NoError(t, err)
		_, err = NewGraphPlan(plan.node.query.(graphQuery[graphParentRow, graphParent]).value, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.Error(t, err)

		wrongType := *childKey.key.parts[0]
		wrongType.typ = reflect.TypeOf("")
		wrongKey := GraphKey[graphChildRow]{key: &graphKeySpec{parts: []*graphKeyPartSpec{&wrongType, childKey.key.parts[1]}}}
		edge, err = HasMany("wrong-type", parentKey, wrongKey, childPlan, EdgeOptions{}, func(*graphParent, LoadedMany[graphChild]) {})
		require.NoError(t, err)
		_, err = NewGraphPlan(plan.node.query.(graphQuery[graphParentRow, graphParent]).value, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Contains(t, []string{"invalid_graph_plan", "graph_key_mismatch"}, planErr.Code)

		badWhere := Predicate{source: "contract.other"}
		badEdge, err := HasMany("wrong-option-source", parentKey, childKey, childPlan, EdgeOptions{Where: badWhere}, func(*graphParent, LoadedMany[graphChild]) {})
		require.NoError(t, err)
		_, err = NewGraphPlan(plan.node.query.(graphQuery[graphParentRow, graphParent]).value, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, badEdge)
		var optionErr *PlanError
		require.ErrorAs(t, err, &optionErr)
		_ = parentSource
		_ = childSource
	})

	t.Run("preflight rejects before any call or event", func(t *testing.T) {
		executor, plan, _, _, _, _ := graphContractPlan(t, true)
		var starts atomic.Int64
		executor, err := WithEventObservers(executor, ExtensionErrorHandlerFunc(func(context.Context, ExtensionError) {}), EventObserverFunc(func(ctx context.Context, event Event) (context.Context, EventCompletion) {
			if event.Phase == EventStart {
				starts.Add(1)
			}
			return ctx, nil
		}))
		require.NoError(t, err)
		plan.node.edges[0].childKey.parts[0].codec = "missing-contract-codec"
		_, err = LoadGraph(t.Context(), executor, plan)
		require.Error(t, err)
		require.Zero(t, starts.Load())
	})

	t.Run("an identity cycle and a shared DAG", func(t *testing.T) {
		executor, plan, _, _, _, _ := graphContractPlan(t, true)
		var err error
		child := plan.node.edges[0].child
		require.NotNil(t, plan.node.id)
		require.NotNil(t, child.id)
		require.NotSame(t, plan.node.id, child.id)
		shared := *child
		shared.id = &graphPlanIdentity{marker: 1}
		require.NotSame(t, child.id, shared.id)
		child.edges = append(child.edges, &graphEdgeSpec{kind: graphHasMany, name: "shared", parentKey: plan.node.edges[0].parentKey, childKey: plan.node.edges[0].childKey, child: &shared, attach: plan.node.edges[0].attach})
		child.edges = append(child.edges, &graphEdgeSpec{kind: graphHasMany, name: "cycle", parentKey: plan.node.edges[0].parentKey, childKey: plan.node.edges[0].childKey, child: plan.node, attach: plan.node.edges[0].attach})
		_, err = LoadGraph(t.Context(), executor, plan)
		var cycle *PlanError
		require.ErrorAs(t, err, &cycle)
		require.Equal(t, "graph_cycle", cycle.Code)
	})

	t.Run("the canonical tuple codec runs once", func(t *testing.T) {
		var count atomic.Int64
		registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"contract": graphContractCodec{enc: &count}})
		require.NoError(t, err)
		key := &graphKeySpec{parts: []*graphKeyPartSpec{{typ: reflect.TypeOf(int64(0)), codec: "contract", extract: func(any) (any, bool) { return int64(7), true }}}}
		first, present, err := key.tuple(struct{}{}, registry)
		require.NoError(t, err)
		require.True(t, present)
		second, present, err := key.tuple(struct{}{}, registry)
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, first.identity, second.identity)
		require.Equal(t, int64(2), count.Load())
	})
}
