package rasql

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	querypkg "github.com/lestrrat-go/rasql/query"
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

func TestGraphManyThrough(t *testing.T) {
	t.Run("rejects duplicate target rows without attaching", func(t *testing.T) {
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
	})

	t.Run("rejects foreign target rows without attaching", func(t *testing.T) {
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
	})

	t.Run("the cache includes the fixed target filter", func(t *testing.T) {
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
	})

	t.Run("limits before pair dedup and copies attachments", func(t *testing.T) {
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
	})

	t.Run("rejects a foreign junction parent without attaching", func(t *testing.T) {
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
	})

	t.Run("a missing target never attaches partially", func(t *testing.T) {
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
	})

	t.Run("unequal widths execute", func(t *testing.T) {
		for _, test := range []struct {
			name                    string
			parentWidth, childWidth int
		}{
			{name: "one-to-two", parentWidth: 1, childWidth: 2},
			{name: "two-to-one", parentWidth: 2, childWidth: 1},
		} {
			t.Run(test.name, func(t *testing.T) {
				fixture := graphWidthFixtureFor(t, 2, false)
				plan := graphWidthPlan(t, fixture, test.parentWidth, test.childWidth, EdgeOptions{}, "loaded")
				values, err := LoadGraph(t.Context(), fixture.executor, plan)
				require.NoError(t, err)
				require.Len(t, values, 2)
				for index, value := range values {
					require.True(t, value.Children.Loaded)
					require.Len(t, value.Children.Values, 1, "parent %d", index)
					require.Equal(t, "loaded", value.Children.Values[0].Marker)
				}
				require.Equal(t, int64(1), fixture.executor.junctionStatements.Load())
				require.Equal(t, int64(1), fixture.executor.childStatements.Load())
			})
		}
	})

	t.Run("legacy fixed values do not share the through cache", func(t *testing.T) {
		fixture := graphWidthFixtureFor(t, 2, true)
		parentKey, err := NewGraphKey(KeyPart(fixture.parentID, func(row graphWidthParentRow) int64 { return row.ID }))
		require.NoError(t, err)
		junctionParent, err := NewGraphKey(KeyPart(fixture.junctionParentID, func(row graphWidthJunctionRow) int64 { return row.ParentID }))
		require.NoError(t, err)
		junctionChild, err := NewGraphKey(KeyPart(fixture.junctionChildID, func(row graphWidthJunctionRow) int64 { return row.ChildID }), KeyPart(fixture.junctionChildTenant, func(row graphWidthJunctionRow) int64 { return row.ChildTenant }))
		require.NoError(t, err)
		childKey, err := NewGraphKey(KeyPart(fixture.childID, func(row graphWidthChildRow) int64 { return row.ID }), KeyPart(fixture.childTenant, func(row graphWidthChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		childPlan := func(marker string) GraphPlan[graphWidthChildRow, graphWidthChild] {
			plan, planErr := NewGraphPlan(fixture.childQuery, func(row graphWidthChildRow) graphWidthChild {
				return graphWidthChild{ID: row.ID, Tenant: row.Tenant, Marker: marker}
			})
			require.NoError(t, planErr)
			return plan
		}
		rawPredicate := func(value int64) Predicate {
			expression := fixture.kind.Expr()
			return Predicate{node: querypkg.Equal(expression.node, querypkg.Bind(value)), source: expression.source}
		}
		first, err := ManyThrough("first", parentKey, junctionParent, junctionChild, childKey, fixture.junction, childPlan("first"), EdgeOptions{Where: rawPredicate(1)}, func(parent *graphWidthParent, loaded LoadedMany[graphWidthChild]) {
			if len(loaded.Values) > 0 {
				parent.Children = loaded
			}
		})
		require.NoError(t, err)
		second, err := ManyThrough("second", parentKey, junctionParent, junctionChild, childKey, fixture.junction, childPlan("second"), EdgeOptions{Where: rawPredicate(2)}, func(parent *graphWidthParent, loaded LoadedMany[graphWidthChild]) {
			if len(loaded.Values) > 0 {
				parent.Children = loaded
			}
		})
		require.NoError(t, err)
		plan, err := NewGraphPlan(fixture.parentQuery, func(graphWidthParentRow) graphWidthParent { return graphWidthParent{} }, first, second)
		require.NoError(t, err)
		values, err := LoadGraph(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 2)
		require.Equal(t, int64(2), fixture.executor.junctionStatements.Load())
		require.Equal(t, int64(2), fixture.executor.childStatements.Load())
		for _, value := range values {
			require.Len(t, value.Children.Values, 1)
		}
	})

	t.Run("a bind limit above the profile splits the batch", func(t *testing.T) {
		fixture := graphWidthFixtureFor(t, 1000, false)
		fixedValues := make([]any, 500)
		for index := range fixedValues {
			fixedValues[index] = int64(1)
		}
		kind := fixture.kind.Expr()
		options := EdgeOptions{
			BindLimit: executorCompilerProfile(fixture.executor).MaxBind + 100,
			Where:     Predicate{node: querypkg.In(kind.node, fixedValues...), source: kind.source},
		}
		plan := graphWidthPlan(t, fixture, 1, 2, options, "loaded")
		values, err := LoadGraph(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 1000)
		require.Equal(t, int64(3), fixture.executor.junctionStatements.Load())
		require.Equal(t, int64(3), fixture.executor.childStatements.Load())
	})
}

func (r *mtCountingRows) Next() bool {
	if !r.ResultRows.Next() {
		return false
	}
	r.count.Add(1)
	return true
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

type graphWidthParentRow struct{ ID, Tenant int64 }
type graphWidthChildRow struct{ ID, Tenant int64 }
type graphWidthJunctionRow struct {
	ParentID, ParentTenant int64
	ChildID, ChildTenant   int64
	Kind                   int64
}
type graphWidthChild struct {
	ID     int64
	Tenant int64
	Marker string
}
type graphWidthParent struct{ Children LoadedMany[graphWidthChild] }

type graphWidthParentDecoder struct{ schema ResultSchema }

func (d graphWidthParentDecoder) ResultSchema() ResultSchema { return d.schema }
func (graphWidthParentDecoder) Presence() []Presence         { return nil }
func (d graphWidthParentDecoder) DecodeRow(source ScanSource, value *graphWidthParentRow) error {
	return source.Scan(&value.ID, &value.Tenant)
}

type graphWidthChildDecoder struct{ schema ResultSchema }

func (d graphWidthChildDecoder) ResultSchema() ResultSchema { return d.schema }
func (graphWidthChildDecoder) Presence() []Presence         { return nil }
func (d graphWidthChildDecoder) DecodeRow(source ScanSource, value *graphWidthChildRow) error {
	return source.Scan(&value.ID, &value.Tenant)
}

type graphWidthExecutor struct {
	Executor
	compiler           *querycompile.Compiler
	junctionStatements atomic.Int64
	childStatements    atomic.Int64
}

func (e *graphWidthExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }
func (e *graphWidthExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	if strings.Contains(statement.SQL(), "r4_width_junctions") {
		e.junctionStatements.Add(1)
	}
	if strings.Contains(statement.SQL(), "r4_width_children") {
		e.childStatements.Add(1)
	}
	return e.Executor.Query(ctx, statement)
}
func (e *graphWidthExecutor) Exec(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	return e.Executor.Exec(ctx, statement)
}

type graphWidthFixture struct {
	executor                               *graphWidthExecutor
	parents                                Source
	children                               Source
	junction                               Source
	parentQuery                            Query[graphWidthParentRow]
	childQuery                             Query[graphWidthChildRow]
	parentID, parentTenant                 Column[graphWidthParentRow, int64]
	childID, childTenant                   Column[graphWidthChildRow, int64]
	junctionParentID, junctionParentTenant Column[graphWidthJunctionRow, int64]
	junctionChildID, junctionChildTenant   Column[graphWidthJunctionRow, int64]
	kind                                   Column[graphWidthJunctionRow, int64]
}

func graphWidthFixtureFor(t *testing.T, rows int, twoKinds bool) graphWidthFixture {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`CREATE TABLE r4_width_parents (id INTEGER PRIMARY KEY, tenant INTEGER NOT NULL);
CREATE TABLE r4_width_children (id INTEGER PRIMARY KEY, tenant INTEGER NOT NULL);
CREATE TABLE r4_width_junctions (parent_id INTEGER NOT NULL, parent_tenant INTEGER NOT NULL, child_id INTEGER NOT NULL, child_tenant INTEGER NOT NULL, kind INTEGER NOT NULL)`)
	require.NoError(t, err)
	for i := 1; i <= rows; i++ {
		parentTenant := int64(10000 + i)
		childID := int64(20000 + i)
		childTenant := int64(30000 + i)
		_, err = database.Exec(`INSERT INTO r4_width_parents VALUES (?, ?)`, i, parentTenant)
		require.NoError(t, err)
		_, err = database.Exec(`INSERT INTO r4_width_children VALUES (?, ?)`, childID, childTenant)
		require.NoError(t, err)
		_, err = database.Exec(`INSERT INTO r4_width_junctions VALUES (?, ?, ?, ?, 1)`, i, parentTenant, childID, childTenant)
		require.NoError(t, err)
		if twoKinds {
			secondID := int64(40000 + i)
			secondTenant := int64(50000 + i)
			_, err = database.Exec(`INSERT INTO r4_width_children VALUES (?, ?)`, secondID, secondTenant)
			require.NoError(t, err)
			_, err = database.Exec(`INSERT INTO r4_width_junctions VALUES (?, ?, ?, ?, 2)`, i, parentTenant, secondID, secondTenant)
			require.NoError(t, err)
		}
	}
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	base, err := AsExecutor(db, profile)
	require.NoError(t, err)
	provider, ok := base.(compilerProvider)
	require.True(t, ok)
	executor := &graphWidthExecutor{Executor: base, compiler: provider.queryCompiler()}
	parents, err := SourceOf(MustReadTableOf[graphWidthParentRow](schema.TableDef{
		Name: "r4_width_parents", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}},
	}), "wp")
	require.NoError(t, err)
	children, err := SourceOf(MustReadTableOf[graphWidthChildRow](schema.TableDef{
		Name: "r4_width_children", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}},
	}), "wc")
	require.NoError(t, err)
	junction, err := SourceOf(MustReadTableOf[graphWidthJunctionRow](schema.TableDef{
		Name: "r4_width_junctions", Columns: []schema.ColumnDef{
			{Name: "parent_id", Type: schema.IntegerType{}}, {Name: "parent_tenant", Type: schema.IntegerType{}},
			{Name: "child_id", Type: schema.IntegerType{}}, {Name: "child_tenant", Type: schema.IntegerType{}},
			{Name: "kind", Type: schema.IntegerType{}},
		},
	}), "wj")
	require.NoError(t, err)
	parentRelation := TypedRelation[graphWidthParentRow]{source: parents.Source()}
	childRelation := TypedRelation[graphWidthChildRow]{source: children.Source()}
	junctionRelation := TypedRelation[graphWidthJunctionRow]{source: junction.Source()}
	parentID, err := BindColumn[graphWidthParentRow, int64](parentRelation, "id", "")
	require.NoError(t, err)
	parentTenant, err := BindColumn[graphWidthParentRow, int64](parentRelation, "tenant", "")
	require.NoError(t, err)
	childID, err := BindColumn[graphWidthChildRow, int64](childRelation, "id", "")
	require.NoError(t, err)
	childTenant, err := BindColumn[graphWidthChildRow, int64](childRelation, "tenant", "")
	require.NoError(t, err)
	junctionParentID, err := BindColumn[graphWidthJunctionRow, int64](junctionRelation, "parent_id", "")
	require.NoError(t, err)
	junctionParentTenant, err := BindColumn[graphWidthJunctionRow, int64](junctionRelation, "parent_tenant", "")
	require.NoError(t, err)
	junctionChildID, err := BindColumn[graphWidthJunctionRow, int64](junctionRelation, "child_id", "")
	require.NoError(t, err)
	junctionChildTenant, err := BindColumn[graphWidthJunctionRow, int64](junctionRelation, "child_tenant", "")
	require.NoError(t, err)
	kind, err := BindColumn[graphWidthJunctionRow, int64](junctionRelation, "kind", "")
	require.NoError(t, err)
	parentSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "tenant", Type: schema.IntegerType{}})
	require.NoError(t, err)
	parentProjection, err := NewProjection([]ProjectionItem{Item("id", parentID.Expr(), schema.IntegerType{}, ""), Item("tenant", parentTenant.Expr(), schema.IntegerType{}, "")}, graphWidthParentDecoder{schema: parentSchema})
	require.NoError(t, err)
	childSchema, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}}, ResultColumn{Name: "tenant", Type: schema.IntegerType{}})
	require.NoError(t, err)
	childProjection, err := NewProjection([]ProjectionItem{Item("id", childID.Expr(), schema.IntegerType{}, ""), Item("tenant", childTenant.Expr(), schema.IntegerType{}, "")}, graphWidthChildDecoder{schema: childSchema})
	require.NoError(t, err)
	return graphWidthFixture{
		executor: executor, parents: parents.Source(), children: children.Source(), junction: junction.Source(),
		parentQuery: Select(parents.Source(), parentProjection).OrderBy(AscExpr(parentID.Expr())),
		childQuery:  Select(children.Source(), childProjection).OrderBy(AscExpr(childID.Expr())),
		parentID:    parentID, parentTenant: parentTenant, childID: childID, childTenant: childTenant,
		junctionParentID: junctionParentID, junctionParentTenant: junctionParentTenant,
		junctionChildID: junctionChildID, junctionChildTenant: junctionChildTenant, kind: kind,
	}
}

func graphWidthPlan(t *testing.T, fixture graphWidthFixture, parentWidth, childWidth int, options EdgeOptions, marker string) GraphPlan[graphWidthParentRow, graphWidthParent] {
	t.Helper()
	var parentKey GraphKey[graphWidthParentRow]
	var childKey GraphKey[graphWidthChildRow]
	var junctionParent GraphKey[graphWidthJunctionRow]
	var junctionChild GraphKey[graphWidthJunctionRow]
	var err error
	if parentWidth == 1 {
		parentKey, err = NewGraphKey(KeyPart(fixture.parentID, func(row graphWidthParentRow) int64 { return row.ID }))
		require.NoError(t, err)
		junctionParent, err = NewGraphKey(KeyPart(fixture.junctionParentID, func(row graphWidthJunctionRow) int64 { return row.ParentID }))
	} else {
		parentKey, err = NewGraphKey(KeyPart(fixture.parentID, func(row graphWidthParentRow) int64 { return row.ID }), KeyPart(fixture.parentTenant, func(row graphWidthParentRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		junctionParent, err = NewGraphKey(KeyPart(fixture.junctionParentID, func(row graphWidthJunctionRow) int64 { return row.ParentID }), KeyPart(fixture.junctionParentTenant, func(row graphWidthJunctionRow) int64 { return row.ParentTenant }))
	}
	require.NoError(t, err)
	if childWidth == 1 {
		childKey, err = NewGraphKey(KeyPart(fixture.childID, func(row graphWidthChildRow) int64 { return row.ID }))
		require.NoError(t, err)
		junctionChild, err = NewGraphKey(KeyPart(fixture.junctionChildID, func(row graphWidthJunctionRow) int64 { return row.ChildID }))
	} else {
		childKey, err = NewGraphKey(KeyPart(fixture.childID, func(row graphWidthChildRow) int64 { return row.ID }), KeyPart(fixture.childTenant, func(row graphWidthChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		junctionChild, err = NewGraphKey(KeyPart(fixture.junctionChildID, func(row graphWidthJunctionRow) int64 { return row.ChildID }), KeyPart(fixture.junctionChildTenant, func(row graphWidthJunctionRow) int64 { return row.ChildTenant }))
	}
	require.NoError(t, err)
	childPlan, err := NewGraphPlan(fixture.childQuery, func(row graphWidthChildRow) graphWidthChild {
		return graphWidthChild{ID: row.ID, Tenant: row.Tenant, Marker: marker}
	})
	require.NoError(t, err)
	edge, err := ManyThrough("children", parentKey, junctionParent, junctionChild, childKey, fixture.junction, childPlan, options, func(parent *graphWidthParent, loaded LoadedMany[graphWidthChild]) { parent.Children = loaded })
	require.NoError(t, err)
	plan, err := NewGraphPlan(fixture.parentQuery, func(graphWidthParentRow) graphWidthParent { return graphWidthParent{} }, edge)
	require.NoError(t, err)
	return plan
}
