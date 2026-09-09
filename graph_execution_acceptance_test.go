package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

// graphCountingExecutor counts rows at the ResultRows boundary. Counting
// decoded slices would miss rows discarded by a client-side partition limit.
type graphCountingExecutor struct {
	Executor
	compiler        *querycompile.Compiler
	childRows       atomic.Int64
	childStatements atomic.Int64
	bindCounts      []int
}

func (e *graphCountingExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }

func (e *graphCountingExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	rows, err := e.Executor.Query(ctx, statement)
	if err != nil {
		return nil, err
	}
	if strings.Contains(statement.SQL(), "graph_children") {
		e.childStatements.Add(1)
		e.bindCounts = append(e.bindCounts, len(statement.Args()))
		return &graphCountingRows{ResultRows: rows, child: &e.childRows}, nil
	}
	return rows, nil
}

type graphDuplicate struct {
	First  LoadedMany[graphChild]
	Second LoadedMany[graphChild]
}

type graphDeep1 struct{ Next LoadedMany[graphDeep2] }
type graphDeep2 struct{ Next LoadedMany[graphDeep3] }
type graphDeep3 struct{ Next LoadedMany[graphDeep4] }
type graphDeep4 struct{ Next LoadedMany[graphDeep5] }
type graphDeep5 struct{ Next LoadedMany[graphDeep6] }
type graphDeep6 struct{ Next LoadedMany[graphDeep7] }
type graphDeep7 struct{ Next LoadedMany[graphDeep8] }
type graphDeep8 struct{ Next LoadedMany[graphDeep9] }
type graphDeep9 struct{ Next LoadedMany[graphDeep10] }
type graphDeep10 struct{}
type graphDeepRoot struct{ Children LoadedMany[graphDeep1] }

func TestGraphSQLiteTenLevelHeterogeneousCopiedCallbacks(t *testing.T) {
	base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
	parents := TypedRelation[graphParentRow]{source: parentSource}
	children := TypedRelation[graphChildRow]{source: childSource}
	parentID, err := BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childID, err := BindColumn[graphChildRow, int64](children, "id", "")
	require.NoError(t, err)
	childParent, err := BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	rootKey, err := NewGraphKey(KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }))
	require.NoError(t, err)
	childParentKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	childIDKey, err := NewGraphKey(KeyPart(childID, func(row graphChildRow) int64 { return row.ID }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	p10, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep10 { return graphDeep10{} })
	require.NoError(t, err)
	e9, err := HasMany("level10", childIDKey, childParentKey, p10, EdgeOptions{}, func(parent *graphDeep9, loaded LoadedMany[graphDeep10]) { parent.Next = loaded })
	require.NoError(t, err)
	p9, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep9 { return graphDeep9{} }, e9)
	require.NoError(t, err)
	e8, err := HasMany("level9", childIDKey, childParentKey, p9, EdgeOptions{}, func(parent *graphDeep8, loaded LoadedMany[graphDeep9]) { parent.Next = loaded })
	require.NoError(t, err)
	p8, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep8 { return graphDeep8{} }, e8)
	require.NoError(t, err)
	e7, err := HasMany("level8", childIDKey, childParentKey, p8, EdgeOptions{}, func(parent *graphDeep7, loaded LoadedMany[graphDeep8]) { parent.Next = loaded })
	require.NoError(t, err)
	p7, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep7 { return graphDeep7{} }, e7)
	require.NoError(t, err)
	e6, err := HasMany("level7", childIDKey, childParentKey, p7, EdgeOptions{}, func(parent *graphDeep6, loaded LoadedMany[graphDeep7]) { parent.Next = loaded })
	require.NoError(t, err)
	p6, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep6 { return graphDeep6{} }, e6)
	require.NoError(t, err)
	e5, err := HasMany("level6", childIDKey, childParentKey, p6, EdgeOptions{}, func(parent *graphDeep5, loaded LoadedMany[graphDeep6]) { parent.Next = loaded })
	require.NoError(t, err)
	p5, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep5 { return graphDeep5{} }, e5)
	require.NoError(t, err)
	e4, err := HasMany("level5", childIDKey, childParentKey, p5, EdgeOptions{}, func(parent *graphDeep4, loaded LoadedMany[graphDeep5]) { parent.Next = loaded })
	require.NoError(t, err)
	p4, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep4 { return graphDeep4{} }, e4)
	require.NoError(t, err)
	e3, err := HasMany("level4", childIDKey, childParentKey, p4, EdgeOptions{}, func(parent *graphDeep3, loaded LoadedMany[graphDeep4]) { parent.Next = loaded })
	require.NoError(t, err)
	p3, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep3 { return graphDeep3{} }, e3)
	require.NoError(t, err)
	e2, err := HasMany("level3", childIDKey, childParentKey, p3, EdgeOptions{}, func(parent *graphDeep2, loaded LoadedMany[graphDeep3]) { parent.Next = loaded })
	require.NoError(t, err)
	p2, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep2 { return graphDeep2{} }, e2)
	require.NoError(t, err)
	e1, err := HasMany("level2", childIDKey, childParentKey, p2, EdgeOptions{}, func(parent *graphDeep1, loaded LoadedMany[graphDeep2]) { parent.Next = loaded })
	require.NoError(t, err)
	p1, err := NewGraphPlan(childQuery, func(graphChildRow) graphDeep1 { return graphDeep1{} }, e1)
	require.NoError(t, err)
	rootEdge, err := HasMany("level1", rootKey, childParentKey, p1, EdgeOptions{}, func(parent *graphDeepRoot, loaded LoadedMany[graphDeep1]) { parent.Children = loaded })
	require.NoError(t, err)
	limitedParent, err := parentQuery.Limit(1)
	require.NoError(t, err)
	root, err := NewGraphPlan(limitedParent, func(graphParentRow) graphDeepRoot { return graphDeepRoot{} }, rootEdge)
	require.NoError(t, err)
	values, err := LoadGraph(t.Context(), base, root)
	require.NoError(t, err)
	require.Len(t, values, 1)
	level1 := values[0].Children
	require.True(t, level1.Loaded)
	require.Len(t, level1.Values, 1)
	level2 := level1.Values[0].Next
	require.True(t, level2.Loaded)
	require.Len(t, level2.Values, 1)
	level3 := level2.Values[0].Next
	require.True(t, level3.Loaded)
	require.Len(t, level3.Values, 1)
	level4 := level3.Values[0].Next
	require.True(t, level4.Loaded)
	require.Len(t, level4.Values, 1)
	level5 := level4.Values[0].Next
	require.True(t, level5.Loaded)
	require.Len(t, level5.Values, 1)
	level6 := level5.Values[0].Next
	require.True(t, level6.Loaded)
	require.Len(t, level6.Values, 1)
	level7 := level6.Values[0].Next
	require.True(t, level7.Loaded)
	require.Len(t, level7.Values, 1)
	level8 := level7.Values[0].Next
	require.True(t, level8.Loaded)
	require.Len(t, level8.Values, 1)
	level9 := level8.Values[0].Next
	require.True(t, level9.Loaded)
	require.Len(t, level9.Values, 1)
	level10 := level9.Values[0].Next
	require.True(t, level10.Loaded)
	require.Len(t, level10.Values, 1)
}

func TestGraphSQLiteDuplicateAttachmentsOwnIndependentSlices(t *testing.T) {
	base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 10)
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
	first, err := HasMany("first", parentKey, childKey, childPlan, EdgeOptions{}, func(parent *graphDuplicate, loaded LoadedMany[graphChild]) { parent.First = loaded })
	require.NoError(t, err)
	second, err := HasMany("second", parentKey, childKey, childPlan, EdgeOptions{}, func(parent *graphDuplicate, loaded LoadedMany[graphChild]) { parent.Second = loaded })
	require.NoError(t, err)
	limitedParent, err := parentQuery.Limit(1)
	require.NoError(t, err)
	plan, err := NewGraphPlan(limitedParent, func(graphParentRow) graphDuplicate { return graphDuplicate{} }, first, second)
	require.NoError(t, err)
	values, err := LoadGraph(t.Context(), base, plan)
	require.NoError(t, err)
	require.Len(t, values, 1)
	require.True(t, values[0].First.Loaded)
	require.True(t, values[0].Second.Loaded)
	require.NotSame(t, &values[0].First.Values[0], &values[0].Second.Values[0])
	values[0].First.Values[0].ID = 999
	require.NotEqual(t, int64(999), values[0].Second.Values[0].ID)
}

func TestGraphSQLiteBindBudgetUsesFixedAndCompositeBatches(t *testing.T) {
	base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 110)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	executor := &graphCountingExecutor{Executor: base, compiler: provider.queryCompiler()}
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
	childRank, err := BindColumn[graphChildRow, int64](children, "rank", "")
	require.NoError(t, err)
	parentKey, err := NewGraphKey(KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }))
	require.NoError(t, err)
	childKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	childPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
	require.NoError(t, err)
	where := And(EqualValue(childRank.Expr(), int64(1)), EqualValue(childRank.Expr(), int64(2)), EqualValue(childRank.Expr(), int64(3)))
	edge, err := HasMany("children", parentKey, childKey, childPlan, EdgeOptions{Where: where, BindLimit: 13}, func(parent *graphParent, loaded LoadedMany[graphChild]) { parent.Children = loaded })
	require.NoError(t, err)
	limitedParent, err := parentQuery.Limit(11)
	require.NoError(t, err)
	plan, err := NewGraphPlan(limitedParent, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
	require.NoError(t, err)
	_, err = LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Equal(t, []int{13, 13, 5}, executor.bindCounts)
}

func (*graphCountingExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

type graphCountingRows struct {
	ResultRows
	child *atomic.Int64
}

func (r *graphCountingRows) Next() bool {
	if !r.ResultRows.Next() {
		return false
	}
	r.child.Add(1)
	return true
}

func TestGraphSQLiteExecutionBoundsRowsAtSQLBoundary(t *testing.T) {
	base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 5000)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	executor := &graphCountingExecutor{Executor: base, compiler: provider.queryCompiler()}

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
	edge, err := HasMany("children", parentKey, childKey, childPlan, EdgeOptions{PerParentLimit: 5}, func(parent *graphParent, loaded LoadedMany[graphChild]) { parent.Children = loaded })
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
	require.NoError(t, err)
	values, err := LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 500)
	require.LessOrEqual(t, executor.childRows.Load(), int64(2375))
	require.NotEmpty(t, executor.childStatements.Load())
	for _, value := range values {
		require.True(t, value.Children.Loaded)
		require.NotNil(t, value.Children.Values)
		require.LessOrEqual(t, len(value.Children.Values), 5)
	}
}
