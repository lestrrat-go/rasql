package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

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

func TestGraphContractRejectsMetadataAndOrderViolations(t *testing.T) {
	executor, plan, parentKey, childKey, parentSource, childSource := graphContractPlan(t, true)
	require.NotNil(t, plan.node)
	_, unordered, _, _, _, _ := graphContractPlan(t, false)
	_, err := LoadGraph(t.Context(), executor, unordered)
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
}

func TestGraphContractPreflightRejectsBeforeCallsAndEvents(t *testing.T) {
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
}

func TestGraphContractIdentityCycleAndSharedDAG(t *testing.T) {
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
}

func TestGraphContractCanonicalTupleCodecRunsOnce(t *testing.T) {
	count := new(atomic.Int64)
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"contract": graphContractCodec{enc: count}})
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
}
