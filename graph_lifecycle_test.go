package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/graphkey"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type lifecycleExecutor struct {
	rasql.Executor
	rows    []*runtimeFakeRows
	calls   atomic.Int64
	onQuery func(context.Context)
}

func (e *lifecycleExecutor) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
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

func lifecycleGraphPlan(t *testing.T, base rasql.Executor, one bool, attach func(*graphParent, rasql.LoadedMany[graphChild])) rasql.GraphPlan[graphParentRow, graphParent] {
	t.Helper()
	_, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
	var err error
	parents := rasql.Q1TypedRelation[graphParentRow](parentSource)
	children := rasql.Q1TypedRelation[graphChildRow](childSource)
	parentID, err := rasql.BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := rasql.BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childParent, err := rasql.BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := rasql.BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }))
	require.NoError(t, err)
	childKey, err := rasql.NewGraphKey(rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	childPlan, err := rasql.NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID, Rank: row.Rank} })
	require.NoError(t, err)
	edge, err := rasql.HasMany("children", parentKey, childKey, childPlan, rasql.EdgeOptions{}, attach)
	if one {
		edge, err = rasql.HasOne("child", parentKey, childKey, childPlan, rasql.EdgeOptions{}, func(parent *graphParent, loaded rasql.LoadedOne[graphChild]) {
			if loaded.Present {
				attach(parent, rasql.LoadedMany[graphChild]{Loaded: loaded.Loaded, Values: []graphChild{*loaded.Value}})
				return
			}
			attach(parent, rasql.LoadedMany[graphChild]{Loaded: loaded.Loaded})
		})
	}
	require.NoError(t, err)
	limitedParent, err := parentQuery.Limit(1)
	require.NoError(t, err)
	plan, err := rasql.NewGraphPlan(limitedParent, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
	require.NoError(t, err)
	return plan
}

func TestGraphLifecycle(t *testing.T) {
	t.Run("emits ordered logical and statement events", func(t *testing.T) {
		executor, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 4)
		var err error
		parentQuery, err = parentQuery.Limit(1)
		require.NoError(t, err)
		parents := rasql.Q1TypedRelation[graphParentRow](parentSource)
		children := rasql.Q1TypedRelation[graphChildRow](childSource)
		parentID, err := rasql.BindColumn[graphParentRow, int64](parents, "id", "")
		require.NoError(t, err)
		parentTenant, err := rasql.BindNullColumn[graphParentRow, int64](parents, "tenant", "")
		require.NoError(t, err)
		childParent, err := rasql.BindColumn[graphChildRow, int64](children, "parent", "")
		require.NoError(t, err)
		childTenant, err := rasql.BindColumn[graphChildRow, int64](children, "tenant", "")
		require.NoError(t, err)
		parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }))
		require.NoError(t, err)
		childKey, err := rasql.NewGraphKey(rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		childPlan, err := rasql.NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
		require.NoError(t, err)
		edge, err := rasql.HasMany("children", parentKey, childKey, childPlan, rasql.EdgeOptions{PerParentLimit: 2}, func(parent *graphParent, loaded rasql.LoadedMany[graphChild]) { parent.Children = loaded })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.NoError(t, err)
		type contextKey struct{}
		var marker contextKey
		var mu sync.Mutex
		var events []rasql.Event
		terminalDerived := false
		var observerError = errors.New("observer completion failed")
		executor, err = rasql.WithEventObservers(executor,
			rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}),
			rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
				mu.Lock()
				events = append(events, event)
				mu.Unlock()
				return context.WithValue(ctx, marker, true), rasql.EventCompletionFunc(func(completionCtx context.Context, terminal rasql.Event) error {
					if completionCtx.Value(marker) == true {
						terminalDerived = true
					}
					mu.Lock()
					events = append(events, terminal)
					mu.Unlock()
					if terminal.Kind == rasql.EventGraph && terminal.Phase == rasql.EventTerminal && terminal.Err == nil {
						return observerError
					}
					return nil
				})
			}),
		)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 1)

		mu.Lock()
		defer mu.Unlock()
		require.NotEmpty(t, events)
		require.Equal(t, rasql.EventGraph, events[0].Kind)
		require.Equal(t, rasql.EventStart, events[0].Phase)
		graphID := events[0].LogicalID
		var statementStarts, statementTerminals int
		for _, event := range events {
			if event.Kind == rasql.EventStatement && event.Phase == rasql.EventStart {
				require.Equal(t, graphID, event.ParentID)
				require.Equal(t, statementStarts, event.StatementIndex)
				statementStarts++
			}
			if event.Kind == rasql.EventStatement && event.Phase == rasql.EventTerminal {
				statementTerminals++
			}
		}
		require.Positive(t, statementStarts)
		require.Equal(t, statementStarts, statementTerminals)
		require.True(t, terminalDerived)
	})

	t.Run("a decode failure stops later queries and joins cleanup", func(t *testing.T) {
		base, _, _, _, _, _ := graphAcceptanceFixture(t, 1)
		closeErr := errors.New("child close")
		finishErr := errors.New("child finish")
		childRows := &runtimeFakeRows{columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{{int64(11)}}, finishErr: errors.Join(closeErr, finishErr)}
		// The short child row makes the real decoder return the primary scan error.
		raw := &lifecycleExecutor{Executor: base, rows: []*runtimeFakeRows{
			{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}}, childRows,
		}}
		executor := graphProfiled(t, raw)
		plan := lifecycleGraphPlan(t, base, false, func(*graphParent, rasql.LoadedMany[graphChild]) {})
		_, err := rasql.LoadGraph(t.Context(), executor, plan)
		require.Error(t, err)
		require.ErrorIs(t, err, sql.ErrNoRows)
		require.ErrorIs(t, err, closeErr)
		require.ErrorIs(t, err, finishErr)
		require.Equal(t, int64(2), raw.calls.Load())
		require.Equal(t, 1, childRows.closed)
		require.Equal(t, 1, childRows.finished)
	})

	t.Run("cancellation stops later breadth levels", func(t *testing.T) {
		base, _, _, _, _, _ := graphAcceptanceFixture(t, 1)
		cancelCtx, cancel := context.WithCancel(t.Context())
		defer cancel()
		raw := &lifecycleExecutor{Executor: base, rows: []*runtimeFakeRows{
			{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}}, {columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{{int64(11), int64(1), int64(1), int64(0)}}},
		}}
		raw.onQuery = func(context.Context) {
			if raw.calls.Load() == 1 {
				cancel()
			}
		}
		executor := graphProfiled(t, raw)
		plan := lifecycleGraphPlan(t, base, false, func(*graphParent, rasql.LoadedMany[graphChild]) {})
		_, err := rasql.LoadGraph(cancelCtx, executor, plan)
		require.ErrorIs(t, err, context.Canceled)
		require.Equal(t, int64(1), raw.calls.Load())
	})

	t.Run("a second has-one row stops the attachment", func(t *testing.T) {
		base, _, _, _, _, _ := graphAcceptanceFixture(t, 1)
		childRows := &runtimeFakeRows{columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{{int64(11), int64(1), int64(1), int64(0)}, {int64(12), int64(1), int64(1), int64(1)}}}
		raw := &lifecycleExecutor{Executor: base, rows: []*runtimeFakeRows{
			{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}}, childRows,
		}}
		executor := graphProfiled(t, raw)
		plan := lifecycleGraphPlan(t, base, true, func(*graphParent, rasql.LoadedMany[graphChild]) {})
		_, err := rasql.LoadGraph(t.Context(), executor, plan)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "cardinality", planErr.Code)
		require.Equal(t, int64(2), raw.calls.Load())
		require.Equal(t, 1, childRows.closed)
		require.Equal(t, 1, childRows.finished)
	})

	t.Run("mapper and attachment panics propagate", func(t *testing.T) {
		base, _, _, _, parentQuery, _ := graphAcceptanceFixture(t, 1)
		newExecutor := func() rasql.Executor {
			return graphProfiled(t, &lifecycleExecutor{Executor: base, rows: []*runtimeFakeRows{{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}}}})
		}
		newAttachmentExecutor := func() rasql.Executor {
			return graphProfiled(t, &lifecycleExecutor{Executor: base, rows: []*runtimeFakeRows{
				{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}},
				{columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{{int64(11), int64(1), int64(1), int64(0)}}},
			}})
		}
		parents := parentQuery
		limitedParents, err := parents.Limit(1)
		require.NoError(t, err)
		mapperPlan, err := rasql.NewGraphPlan(limitedParents, func(graphParentRow) graphParent { panic("mapper panic") })
		require.NoError(t, err)
		require.Panics(t, func() { _, _ = rasql.LoadGraph(t.Context(), newExecutor(), mapperPlan) })
		plan := lifecycleGraphPlan(t, base, false, func(*graphParent, rasql.LoadedMany[graphChild]) { panic("attachment panic") })
		require.Panics(t, func() { _, _ = rasql.LoadGraph(t.Context(), newAttachmentExecutor(), plan) })
	})
}

type graphContractCodec struct{ enc *atomic.Int64 }

func (c graphContractCodec) Encode(value any) (driver.Value, error) {
	c.enc.Add(1)
	return value, nil
}
func (graphContractCodec) Decode(source any, destination any) error { return nil }

func graphContractPlan(t *testing.T, order bool) (rasql.Executor, rasql.GraphPlan[graphParentRow, graphParent], rasql.GraphPlan[graphChildRow, graphChild], rasql.Query[graphParentRow], rasql.GraphKey[graphParentRow], rasql.GraphKey[graphChildRow]) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	parentTable := rasql.MustReadTableOf[graphParentRow](schema.TableDef{Name: "contract_parents", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true}}, PrimaryKey: []string{"id"}})
	childTable := rasql.MustReadTableOf[graphChildRow](schema.TableDef{Name: "contract_children", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	parents, err := rasql.SourceOf(parentTable, "p")
	require.NoError(t, err)
	children, err := rasql.SourceOf(childTable, "c")
	require.NoError(t, err)
	parentID, err := rasql.BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := rasql.BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childParent, err := rasql.BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := rasql.BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	childID, err := rasql.BindColumn[graphChildRow, int64](children, "id", "")
	require.NoError(t, err)
	childRank, err := rasql.BindColumn[graphChildRow, int64](children, "rank", "")
	require.NoError(t, err)
	parentSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "tenant", Type: schema.IntegerType{}, Nullable: true})
	require.NoError(t, err)
	parentProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", parentID.Expr(), schema.IntegerType{}, ""), rasql.NullItem("tenant", parentTenant.NullExpr(), schema.IntegerType{}, "")}, graphParentDecoder{schema: parentSchema})
	require.NoError(t, err)
	childSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "parent", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "tenant", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "rank", Type: schema.IntegerType{}})
	require.NoError(t, err)
	childProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", childID.Expr(), schema.IntegerType{}, ""), rasql.Item("parent", childParent.Expr(), schema.IntegerType{}, ""), rasql.Item("tenant", childTenant.Expr(), schema.IntegerType{}, ""), rasql.Item("rank", childRank.Expr(), schema.IntegerType{}, "")}, graphChildDecoder{schema: childSchema})
	require.NoError(t, err)
	parentQuery := rasql.Select(parents.Source(), parentProjection).OrderBy(rasql.AscExpr(parentID.Expr()))
	childQuery := rasql.Select(children.Source(), childProjection)
	if order {
		childQuery = childQuery.OrderBy(rasql.AscExpr(childRank.Expr()), rasql.AscExpr(childID.Expr()))
	} else {
		childQuery = childQuery.OrderBy(rasql.AscExpr(childRank.Expr()))
	}
	parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }))
	require.NoError(t, err)
	childKey, err := rasql.NewGraphKey(rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	childPlan, err := rasql.NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID, Rank: row.Rank} })
	require.NoError(t, err)
	edge, err := rasql.HasMany("children", parentKey, childKey, childPlan, rasql.EdgeOptions{PerParentLimit: 5}, func(parent *graphParent, loaded rasql.LoadedMany[graphChild]) { parent.Children = loaded })
	require.NoError(t, err)
	plan, err := rasql.NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
	require.NoError(t, err)
	return executor, plan, childPlan, parentQuery, parentKey, childKey
}

func TestGraphContract(t *testing.T) {
	t.Run("rejects metadata and order violations", func(t *testing.T) {
		_, _, childPlan, parentQuery, parentKey, childKey := graphContractPlan(t, true)
		unorderedExecutor, unordered, _, _, _, _ := graphContractPlan(t, false)
		_, err := rasql.LoadGraph(t.Context(), unorderedExecutor, unordered)
		var orderErr *rasql.PlanError
		require.ErrorAs(t, err, &orderErr)
		require.Equal(t, "order_not_unique", orderErr.Code)
		_, err = rasql.NewGraphKey[graphParentRow]()
		require.Error(t, err)

		shortKey := rasql.Q1GraphKeyOf[graphChildRow](&graphkey.Spec{Parts: rasql.Q1GraphKeySpec(childKey).Parts[:1]})
		edge, err := rasql.HasMany("wrong-width", parentKey, shortKey, childPlan, rasql.EdgeOptions{}, func(*graphParent, rasql.LoadedMany[graphChild]) {})
		require.NoError(t, err)
		_, err = rasql.NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.Error(t, err)

		wrongType := *rasql.Q1GraphKeySpec(childKey).Parts[0]
		wrongType.Type = reflect.TypeOf("")
		wrongKey := rasql.Q1GraphKeyOf[graphChildRow](&graphkey.Spec{Parts: []*graphkey.PartSpec{&wrongType, rasql.Q1GraphKeySpec(childKey).Parts[1]}})
		edge, err = rasql.HasMany("wrong-type", parentKey, wrongKey, childPlan, rasql.EdgeOptions{}, func(*graphParent, rasql.LoadedMany[graphChild]) {})
		require.NoError(t, err)
		_, err = rasql.NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Contains(t, []string{"invalid_graph_plan", "graph_key_mismatch"}, planErr.Code)

		// A predicate on the parent relation names a source the child edge does
		// not select from, which is what the option check rejects.
		badWhere := rasql.EqualValue(rasql.Q1ProjectedExpr[graphParentRow, int64](parentQuery, 0), int64(1))
		badEdge, err := rasql.HasMany("wrong-option-source", parentKey, childKey, childPlan, rasql.EdgeOptions{Where: badWhere}, func(*graphParent, rasql.LoadedMany[graphChild]) {})
		require.NoError(t, err)
		_, err = rasql.NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, badEdge)
		var optionErr *rasql.PlanError
		require.ErrorAs(t, err, &optionErr)
	})

	t.Run("preflight rejects before any call or event", func(t *testing.T) {
		executor, plan, _, _, _, _ := graphContractPlan(t, true)
		var starts atomic.Int64
		executor, err := rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			if event.Phase == rasql.EventStart {
				starts.Add(1)
			}
			return ctx, nil
		}))
		require.NoError(t, err)
		rasql.Q1GraphEdgeChildKey(plan, 0).Parts[0].Codec = "missing-contract-codec"
		_, err = rasql.LoadGraph(t.Context(), executor, plan)
		require.Error(t, err)
		require.Zero(t, starts.Load())
	})

	t.Run("an identity cycle and a shared DAG", func(t *testing.T) {
		executor, plan, _, _, _, _ := graphContractPlan(t, true)
		var err error
		root, child, shared := rasql.Q1GraphAddCycle(plan)
		require.NotNil(t, root)
		require.NotNil(t, child)
		require.NotSame(t, root, child)
		require.NotSame(t, child, shared)
		_, err = rasql.LoadGraph(t.Context(), executor, plan)
		var cycle *rasql.PlanError
		require.ErrorAs(t, err, &cycle)
		require.Equal(t, "graph_cycle", cycle.Code)
	})

	t.Run("the canonical tuple codec runs once", func(t *testing.T) {
		var count atomic.Int64
		registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"contract": graphContractCodec{enc: &count}})
		require.NoError(t, err)
		key := &graphkey.Spec{Parts: []*graphkey.PartSpec{{Type: reflect.TypeOf(int64(0)), Codec: "contract", Extract: func(any) (any, bool) { return int64(7), true }}}}
		first, present, err := key.Tuple(struct{}{}, graphContractEncoder{codecs: registry})
		require.NoError(t, err)
		require.True(t, present)
		second, present, err := key.Tuple(struct{}{}, graphContractEncoder{codecs: registry})
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, first.Identity, second.Identity)
		require.Equal(t, int64(2), count.Load())
	})
}

// graphContractEncoder is what graphkey asks a caller for: one call that turns
// a value into what a driver carries, for one named codec.
type graphContractEncoder struct{ codecs rasql.CodecRegistry }

func (e graphContractEncoder) EncodeGraphKey(codec string, value any) (driver.Value, error) {
	found, ok := e.codecs.Lookup(rasql.CodecID(codec))
	if !ok {
		return nil, errors.New("rasql: codec is not registered")
	}
	return found.Encode(value)
}
