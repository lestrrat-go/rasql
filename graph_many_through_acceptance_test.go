package rasql

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type mtParentRow struct{ ID int64 }
type mtChildRow struct {
	ID     int64
	Active int64
}
type mtJunctionRow struct {
	ID     int64
	Parent int64
	Target int64
	Rank   int64
}
type mtParentGraph struct{ Children LoadedMany[mtChildGraph] }
type mtChildGraph struct{ ID int64 }
type mtDualGraph struct {
	Active   LoadedMany[mtChildGraph]
	Inactive LoadedMany[mtChildGraph]
}

type mtParentDecoder struct{ schema ResultSchema }

func (d mtParentDecoder) ResultSchema() ResultSchema { return d.schema }
func (mtParentDecoder) Presence() []Presence         { return nil }
func (mtParentDecoder) DecodeRow(source ScanSource, value *mtParentRow) error {
	return source.Scan(&value.ID)
}

type mtChildDecoder struct{ schema ResultSchema }

func (d mtChildDecoder) ResultSchema() ResultSchema { return d.schema }
func (mtChildDecoder) Presence() []Presence         { return nil }
func (mtChildDecoder) DecodeRow(source ScanSource, value *mtChildRow) error {
	return source.Scan(&value.ID, &value.Active)
}

type mtCountingExecutor struct {
	Executor
	compiler           *querycompile.Compiler
	junctionStatements atomic.Int64
	targetStatements   atomic.Int64
	rows               atomic.Int64
}

func (e *mtCountingExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }
func (e *mtCountingExecutor) Query(ctx context.Context, s stmt.Statement) (ResultRows, error) {
	rows, err := e.Executor.Query(ctx, s)
	if err != nil {
		return nil, err
	}
	isJunction := strings.Contains(s.SQL(), "mt_junctions")
	if isJunction {
		e.junctionStatements.Add(1)
	} else if strings.Contains(s.SQL(), "mt_children") {
		e.targetStatements.Add(1)
	}
	if !isJunction && !strings.Contains(s.SQL(), "mt_children") {
		return rows, nil
	}
	return &mtCountingRows{ResultRows: rows, count: &e.rows}, nil
}

type mtCountingRows struct {
	ResultRows
	count *atomic.Int64
}

type mtRowsOverride struct {
	*mtCountingExecutor
	values         [][]any
	junctionValues [][]any
}

func (e *mtRowsOverride) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	if strings.Contains(statement.SQL(), "mt_junctions") && e.junctionValues != nil {
		return &runtimeFakeRows{values: e.junctionValues, columns: []string{"graph_key_0", "graph_key_1"}}, nil
	}
	if strings.Contains(statement.SQL(), "mt_children") {
		return &runtimeFakeRows{values: e.values, columns: []string{"id", "active"}}, nil
	}
	return e.mtCountingExecutor.Query(ctx, statement)
}

func TestGraphSQLiteManyThroughRejectsDuplicateTargetRowsWithoutAttachment(t *testing.T) {
	executor, _, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
	rows := &mtRowsOverride{mtCountingExecutor: executor, values: [][]any{{int64(10), int64(1)}, {int64(10), int64(1)}}}
	var callbacks atomic.Int64
	edge, err := ManyThrough("roles", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(*mtParentGraph, LoadedMany[mtChildGraph]) { callbacks.Add(1) })
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge)
	require.NoError(t, err)
	_, err = LoadGraph(t.Context(), rows, plan)
	require.Error(t, err)
	require.Zero(t, callbacks.Load())
}

func TestGraphSQLiteManyThroughRejectsForeignTargetRowsWithoutAttachment(t *testing.T) {
	executor, _, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
	rows := &mtRowsOverride{mtCountingExecutor: executor, values: [][]any{{int64(999), int64(1)}}}
	var callbacks atomic.Int64
	edge, err := ManyThrough("roles", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(*mtParentGraph, LoadedMany[mtChildGraph]) { callbacks.Add(1) })
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge)
	require.NoError(t, err)
	_, err = LoadGraph(t.Context(), rows, plan)
	require.Error(t, err)
	require.Zero(t, callbacks.Load())
}

func TestGraphSQLiteManyThroughCacheIncludesFixedTargetFilter(t *testing.T) {
	executor, _, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
	baseChild := childPlan.node.query.(graphQuery[mtChildRow, mtChildGraph]).value
	childSource := baseChild.plan.sources[0]
	active, err := BindColumn[mtChildRow, int64](TypedRelation[mtChildRow]{source: childSource}, "active", "")
	require.NoError(t, err)
	inactiveQuery := baseChild.Where(EqualValue(active.Expr(), int64(0)))
	inactivePlan, err := NewGraphPlan(inactiveQuery, func(row mtChildRow) mtChildGraph { return mtChildGraph{ID: row.ID} })
	require.NoError(t, err)
	edgeActive, err := ManyThrough("active", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(parent *mtDualGraph, loaded LoadedMany[mtChildGraph]) { parent.Active = loaded })
	require.NoError(t, err)
	edgeInactive, err := ManyThrough("inactive", parentKey, junctionParent, junctionChild, childKey, junction, inactivePlan, EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(parent *mtDualGraph, loaded LoadedMany[mtChildGraph]) { parent.Inactive = loaded })
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(mtParentRow) mtDualGraph { return mtDualGraph{} }, edgeActive, edgeInactive)
	require.NoError(t, err)
	values, err := LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 2)
	require.Len(t, values[0].Active.Values, 1)
	require.Empty(t, values[0].Inactive.Values)
	require.Equal(t, int64(2), executor.targetStatements.Load())
}

func (r *mtCountingRows) Next() bool {
	if !r.ResultRows.Next() {
		return false
	}
	r.count.Add(1)
	return true
}

func TestGraphSQLiteManyThroughLimitsBeforePairDedupAndCopiesAttachments(t *testing.T) {
	executor, _, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
	edge1, err := ManyThrough("roles_a", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(parent *mtParentGraph, loaded LoadedMany[mtChildGraph]) { parent.Children = loaded })
	require.NoError(t, err)
	edge2, err := ManyThrough("roles_b", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(parent *mtParentGraph, loaded LoadedMany[mtChildGraph]) { parent.Children = loaded })
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge1, edge2)
	require.NoError(t, err)
	values, err := LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 2)
	require.True(t, values[0].Children.Loaded)
	require.Len(t, values[0].Children.Values, 1)
	require.Equal(t, int64(10), values[0].Children.Values[0].ID)
	require.Equal(t, int64(10), values[1].Children.Values[0].ID)
	require.Equal(t, int64(1), executor.junctionStatements.Load())
	require.Equal(t, int64(1), executor.targetStatements.Load())
	require.Equal(t, int64(6), executor.rows.Load())
	values[0].Children.Values[0].ID = 99
	require.Equal(t, int64(10), values[1].Children.Values[0].ID)
}

func TestGraphSQLiteManyThroughRejectsForeignJunctionParentWithoutAttachment(t *testing.T) {
	executor, _, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
	childPlan.node.query = childPlan.node.query.withoutPredicates()
	var callbacks atomic.Int64
	edge, err := ManyThrough("roles", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(*mtParentGraph, LoadedMany[mtChildGraph]) { callbacks.Add(1) })
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge)
	require.NoError(t, err)
	rows := &mtRowsOverride{mtCountingExecutor: executor, junctionValues: [][]any{{int64(999), int64(10), int64(1), int64(1)}}}
	_, err = LoadGraph(t.Context(), rows, plan)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "foreign_key_result", planErr.Code)
	require.Zero(t, callbacks.Load())
}

func TestGraphSQLiteManyThroughMissingTargetDoesNotPartiallyAttach(t *testing.T) {
	executor, _, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
	childPlan.node.query = childPlan.node.query.withoutPredicates()
	var callbacks atomic.Int64
	edge, err := ManyThrough("roles", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(*mtParentGraph, LoadedMany[mtChildGraph]) { callbacks.Add(1) })
	require.NoError(t, err)
	plan, err := NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge)
	require.NoError(t, err)
	rows := &mtRowsOverride{mtCountingExecutor: executor, junctionValues: [][]any{{int64(1), int64(999), int64(1), int64(1)}}}
	_, err = LoadGraph(t.Context(), rows, plan)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "foreign_key_result", planErr.Code)
	require.Zero(t, callbacks.Load())
}

func mtFixture(t *testing.T) (*mtCountingExecutor, *sql.DB, Query[mtParentRow], GraphKey[mtParentRow], GraphKey[mtJunctionRow], GraphKey[mtJunctionRow], GraphKey[mtChildRow], Source, GraphPlan[mtChildRow, mtChildGraph]) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(`CREATE TABLE mt_parents (id INTEGER PRIMARY KEY); CREATE TABLE mt_children (id INTEGER PRIMARY KEY, active INTEGER NOT NULL); CREATE TABLE mt_junctions (id INTEGER PRIMARY KEY, parent INTEGER NOT NULL, target INTEGER NOT NULL, rank INTEGER NOT NULL, UNIQUE(parent, rank, id));`)
	require.NoError(t, err)
	_, err = database.Exec(`INSERT INTO mt_parents VALUES (1), (2); INSERT INTO mt_children VALUES (10, 1), (20, 1), (30, 1); INSERT INTO mt_junctions VALUES (1, 1, 10, 1), (2, 1, 10, 2), (3, 1, 20, 3), (4, 2, 10, 1), (5, 2, 20, 2);`)
	require.NoError(t, err)
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	base, err := AsExecutor(db, profile)
	require.NoError(t, err)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	executor := &mtCountingExecutor{Executor: base, compiler: provider.queryCompiler()}
	parents := MustReadTableOf[mtParentRow](schema.TableDef{Name: "mt_parents", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	children := MustReadTableOf[mtChildRow](schema.TableDef{Name: "mt_children", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "active", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	junctions := MustReadTableOf[mtJunctionRow](schema.TableDef{Name: "mt_junctions", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "target", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	parentSource, err := SourceOf(parents, "p")
	require.NoError(t, err)
	childSource, err := SourceOf(children, "c")
	require.NoError(t, err)
	junctionSource, err := SourceOf(junctions, "j")
	require.NoError(t, err)
	pid, err := BindColumn[mtParentRow, int64](parentSource, "id", "")
	require.NoError(t, err)
	cid, err := BindColumn[mtChildRow, int64](childSource, "id", "")
	require.NoError(t, err)
	active, err := BindColumn[mtChildRow, int64](childSource, "active", "")
	require.NoError(t, err)
	jpid, err := BindColumn[mtJunctionRow, int64](junctionSource, "parent", "")
	require.NoError(t, err)
	jt, err := BindColumn[mtJunctionRow, int64](junctionSource, "target", "")
	require.NoError(t, err)
	jrank, err := BindColumn[mtJunctionRow, int64](junctionSource, "rank", "")
	require.NoError(t, err)
	jid, err := BindColumn[mtJunctionRow, int64](junctionSource, "id", "")
	require.NoError(t, err)
	ps, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	pp, err := NewProjection([]ProjectionItem{Item("id", pid.Expr(), schema.IntegerType{}, "")}, mtParentDecoder{schema: ps})
	require.NoError(t, err)
	cs, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "active", Type: schema.IntegerType{}})
	require.NoError(t, err)
	cp, err := NewProjection([]ProjectionItem{Item("id", cid.Expr(), schema.IntegerType{}, ""), Item("active", active.Expr(), schema.IntegerType{}, "")}, mtChildDecoder{schema: cs})
	require.NoError(t, err)
	parentQuery := Select(parentSource.source, pp)
	childQuery := Select(childSource.source, cp).Where(EqualValue(active.Expr(), int64(1)))
	childPlan, err := NewGraphPlan(childQuery, func(row mtChildRow) mtChildGraph { return mtChildGraph{ID: row.ID} })
	require.NoError(t, err)
	parentKey, err := NewGraphKey(KeyPart(pid, func(row mtParentRow) int64 { return row.ID }))
	require.NoError(t, err)
	junctionParent, err := NewGraphKey(KeyPart(jpid, func(row mtJunctionRow) int64 { return row.Parent }))
	require.NoError(t, err)
	junctionChild, err := NewGraphKey(KeyPart(jt, func(row mtJunctionRow) int64 { return row.Target }))
	require.NoError(t, err)
	childKey, err := NewGraphKey(KeyPart(cid, func(row mtChildRow) int64 { return row.ID }))
	require.NoError(t, err)
	_ = jid
	_ = jrank
	return executor, database, parentQuery, parentKey, junctionParent, junctionChild, childKey, junctionSource.source, childPlan
}

func mtJunctionOrder(source Source) []OrderTerm {
	relation := TypedRelation[mtJunctionRow]{source: source}
	rank, _ := BindColumn[mtJunctionRow, int64](relation, "rank", "")
	id, _ := BindColumn[mtJunctionRow, int64](relation, "id", "")
	return []OrderTerm{AscExpr(rank.Expr()), AscExpr(id.Expr())}
}
