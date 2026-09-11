package rasql_test

import (
	"context"
	"database/sql"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
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
type mtParentGraph struct {
	Children rasql.LoadedMany[mtChildGraph]
}
type mtChildGraph struct{ ID int64 }
type mtDualGraph struct {
	Active   rasql.LoadedMany[mtChildGraph]
	Inactive rasql.LoadedMany[mtChildGraph]
}

type mtParentDecoder struct{ schema rasql.ResultSchema }

func (d mtParentDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (mtParentDecoder) Presence() []rasql.Presence         { return nil }
func (mtParentDecoder) DecodeRow(source rasql.ScanSource, value *mtParentRow) error {
	return source.Scan(&value.ID)
}

type mtChildDecoder struct{ schema rasql.ResultSchema }

func (d mtChildDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (mtChildDecoder) Presence() []rasql.Presence         { return nil }
func (mtChildDecoder) DecodeRow(source rasql.ScanSource, value *mtChildRow) error {
	return source.Scan(&value.ID, &value.Active)
}

type mtCountingExecutor struct {
	rasql.Executor
	junctionStatements atomic.Int64
	targetStatements   atomic.Int64
	rows               atomic.Int64
}

func (e *mtCountingExecutor) Query(ctx context.Context, s stmt.Statement) (rasql.ResultRows, error) {
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
	rasql.ResultRows
	count *atomic.Int64
}

type mtRowsOverride struct {
	*mtCountingExecutor
	values         [][]any
	junctionValues [][]any
}

func (e *mtRowsOverride) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
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
		_, counter, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
		rows := &mtRowsOverride{mtCountingExecutor: counter, values: [][]any{{int64(10), int64(1)}, {int64(10), int64(1)}}}
		var callbacks atomic.Int64
		edge, err := rasql.ManyThrough("roles", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, rasql.EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(*mtParentGraph, rasql.LoadedMany[mtChildGraph]) { callbacks.Add(1) })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge)
		require.NoError(t, err)
		_, err = rasql.LoadGraph(t.Context(), mtProfiled(t, rows), plan)
		require.Error(t, err)
		require.Zero(t, callbacks.Load())
	})

	t.Run("rejects foreign target rows without attaching", func(t *testing.T) {
		_, counter, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
		rows := &mtRowsOverride{mtCountingExecutor: counter, values: [][]any{{int64(999), int64(1)}}}
		var callbacks atomic.Int64
		edge, err := rasql.ManyThrough("roles", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, rasql.EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(*mtParentGraph, rasql.LoadedMany[mtChildGraph]) { callbacks.Add(1) })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge)
		require.NoError(t, err)
		_, err = rasql.LoadGraph(t.Context(), mtProfiled(t, rows), plan)
		require.Error(t, err)
		require.Zero(t, callbacks.Load())
	})

	t.Run("the cache includes the fixed target filter", func(t *testing.T) {
		executor, counter, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
		baseChild := rasql.Q1GraphChildQuery[mtChildRow, mtChildGraph, mtChildRow, mtChildGraph](childPlan)
		childSource := rasql.Q1PlanSources(baseChild.Plan())[0]
		active, err := rasql.BindColumn[mtChildRow, int64](rasql.Q1TypedRelation[mtChildRow](childSource), "active", "")
		require.NoError(t, err)
		inactiveQuery := baseChild.Where(rasql.EqualValue(active.Expr(), int64(0)))
		inactivePlan, err := rasql.NewGraphPlan(inactiveQuery, func(row mtChildRow) mtChildGraph { return mtChildGraph{ID: row.ID} })
		require.NoError(t, err)
		edgeActive, err := rasql.ManyThrough("active", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, rasql.EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(parent *mtDualGraph, loaded rasql.LoadedMany[mtChildGraph]) { parent.Active = loaded })
		require.NoError(t, err)
		edgeInactive, err := rasql.ManyThrough("inactive", parentKey, junctionParent, junctionChild, childKey, junction, inactivePlan, rasql.EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(parent *mtDualGraph, loaded rasql.LoadedMany[mtChildGraph]) { parent.Inactive = loaded })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(mtParentRow) mtDualGraph { return mtDualGraph{} }, edgeActive, edgeInactive)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 2)
		require.Len(t, values[0].Active.Values, 1)
		require.Empty(t, values[0].Inactive.Values)
		require.Equal(t, int64(2), counter.targetStatements.Load())
	})

	t.Run("limits before pair dedup and copies attachments", func(t *testing.T) {
		executor, counter, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
		edge1, err := rasql.ManyThrough("roles_a", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, rasql.EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(parent *mtParentGraph, loaded rasql.LoadedMany[mtChildGraph]) { parent.Children = loaded })
		require.NoError(t, err)
		edge2, err := rasql.ManyThrough("roles_b", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, rasql.EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(parent *mtParentGraph, loaded rasql.LoadedMany[mtChildGraph]) { parent.Children = loaded })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge1, edge2)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 2)
		require.True(t, values[0].Children.Loaded)
		require.Len(t, values[0].Children.Values, 1)
		require.Equal(t, int64(10), values[0].Children.Values[0].ID)
		require.Equal(t, int64(10), values[1].Children.Values[0].ID)
		require.Equal(t, int64(1), counter.junctionStatements.Load())
		require.Equal(t, int64(1), counter.targetStatements.Load())
		require.Equal(t, int64(6), counter.rows.Load())
		values[0].Children.Values[0].ID = 99
		require.Equal(t, int64(10), values[1].Children.Values[0].ID)
	})

	t.Run("rejects a foreign junction parent without attaching", func(t *testing.T) {
		_, counter, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
		childPlan = rasql.Q1WithoutPredicates(childPlan)
		var callbacks atomic.Int64
		edge, err := rasql.ManyThrough("roles", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, rasql.EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(*mtParentGraph, rasql.LoadedMany[mtChildGraph]) { callbacks.Add(1) })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge)
		require.NoError(t, err)
		rows := &mtRowsOverride{mtCountingExecutor: counter, junctionValues: [][]any{{int64(999), int64(10), int64(1), int64(1)}}}
		_, err = rasql.LoadGraph(t.Context(), mtProfiled(t, rows), plan)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "foreign_key_result", planErr.Code)
		require.Zero(t, callbacks.Load())
	})

	t.Run("a missing target never attaches partially", func(t *testing.T) {
		_, counter, parentQuery, parentKey, junctionParent, junctionChild, childKey, junction, childPlan := mtFixture(t)
		childPlan = rasql.Q1WithoutPredicates(childPlan)
		var callbacks atomic.Int64
		edge, err := rasql.ManyThrough("roles", parentKey, junctionParent, junctionChild, childKey, junction, childPlan, rasql.EdgeOptions{Order: mtJunctionOrder(junction), PerParentLimit: 2}, func(*mtParentGraph, rasql.LoadedMany[mtChildGraph]) { callbacks.Add(1) })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(mtParentRow) mtParentGraph { return mtParentGraph{} }, edge)
		require.NoError(t, err)
		rows := &mtRowsOverride{mtCountingExecutor: counter, junctionValues: [][]any{{int64(1), int64(999), int64(1), int64(1)}}}
		_, err = rasql.LoadGraph(t.Context(), mtProfiled(t, rows), plan)
		var planErr *rasql.PlanError
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
				plan := graphWidthPlan(t, fixture, test.parentWidth, test.childWidth, rasql.EdgeOptions{}, "loaded")
				values, err := rasql.LoadGraph(t.Context(), fixture.executor, plan)
				require.NoError(t, err)
				require.Len(t, values, 2)
				for index, value := range values {
					require.True(t, value.Children.Loaded)
					require.Len(t, value.Children.Values, 1, "parent %d", index)
					require.Equal(t, "loaded", value.Children.Values[0].Marker)
				}
				require.Equal(t, int64(1), fixture.counter.junctionStatements.Load())
				require.Equal(t, int64(1), fixture.counter.childStatements.Load())
			})
		}
	})

	t.Run("legacy fixed values do not share the through cache", func(t *testing.T) {
		fixture := graphWidthFixtureFor(t, 2, true)
		parentKey, err := rasql.NewGraphKey(rasql.KeyPart(fixture.parentID, func(row graphWidthParentRow) int64 { return row.ID }))
		require.NoError(t, err)
		junctionParent, err := rasql.NewGraphKey(rasql.KeyPart(fixture.junctionParentID, func(row graphWidthJunctionRow) int64 { return row.ParentID }))
		require.NoError(t, err)
		junctionChild, err := rasql.NewGraphKey(rasql.KeyPart(fixture.junctionChildID, func(row graphWidthJunctionRow) int64 { return row.ChildID }), rasql.KeyPart(fixture.junctionChildTenant, func(row graphWidthJunctionRow) int64 { return row.ChildTenant }))
		require.NoError(t, err)
		childKey, err := rasql.NewGraphKey(rasql.KeyPart(fixture.childID, func(row graphWidthChildRow) int64 { return row.ID }), rasql.KeyPart(fixture.childTenant, func(row graphWidthChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		childPlan := func(marker string) rasql.GraphPlan[graphWidthChildRow, graphWidthChild] {
			plan, planErr := rasql.NewGraphPlan(fixture.childQuery, func(row graphWidthChildRow) graphWidthChild {
				return graphWidthChild{ID: row.ID, Tenant: row.Tenant, Marker: marker}
			})
			require.NoError(t, planErr)
			return plan
		}
		rawPredicate := func(value int64) rasql.Predicate {
			return rasql.Q1UnadoptedPredicate(fixture.kind.Expr(), value)
		}
		first, err := rasql.ManyThrough("first", parentKey, junctionParent, junctionChild, childKey, fixture.junction, childPlan("first"), rasql.EdgeOptions{Where: rawPredicate(1)}, func(parent *graphWidthParent, loaded rasql.LoadedMany[graphWidthChild]) {
			if len(loaded.Values) > 0 {
				parent.Children = loaded
			}
		})
		require.NoError(t, err)
		second, err := rasql.ManyThrough("second", parentKey, junctionParent, junctionChild, childKey, fixture.junction, childPlan("second"), rasql.EdgeOptions{Where: rawPredicate(2)}, func(parent *graphWidthParent, loaded rasql.LoadedMany[graphWidthChild]) {
			if len(loaded.Values) > 0 {
				parent.Children = loaded
			}
		})
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(fixture.parentQuery, func(graphWidthParentRow) graphWidthParent { return graphWidthParent{} }, first, second)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 2)
		require.Equal(t, int64(2), fixture.counter.junctionStatements.Load())
		require.Equal(t, int64(2), fixture.counter.childStatements.Load())
		for _, value := range values {
			require.Len(t, value.Children.Values, 1)
		}
	})

	t.Run("a bind limit above the profile splits the batch", func(t *testing.T) {
		fixture := graphWidthFixtureFor(t, 1000, false)
		fixedRest := make([]int64, 499)
		for index := range fixedRest {
			fixedRest[index] = int64(1)
		}
		kind := fixture.kind.Expr()
		options := rasql.EdgeOptions{
			BindLimit: rasql.Q1ExecutorProfile(fixture.executor).MaxBind + 100,
			Where:     rasql.InValues(kind, int64(1), fixedRest...),
		}
		plan := graphWidthPlan(t, fixture, 1, 2, options, "loaded")
		values, err := rasql.LoadGraph(t.Context(), fixture.executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 1000)
		require.Equal(t, int64(3), fixture.counter.junctionStatements.Load())
		require.Equal(t, int64(3), fixture.counter.childStatements.Load())
	})
}

func (r *mtCountingRows) Next() bool {
	if !r.ResultRows.Next() {
		return false
	}
	r.count.Add(1)
	return true
}

func mtFixture(t *testing.T) (rasql.Executor, *mtCountingExecutor, rasql.Query[mtParentRow], rasql.GraphKey[mtParentRow], rasql.GraphKey[mtJunctionRow], rasql.GraphKey[mtJunctionRow], rasql.GraphKey[mtChildRow], rasql.Source, rasql.GraphPlan[mtChildRow, mtChildGraph]) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.Exec(`CREATE TABLE mt_parents (id INTEGER PRIMARY KEY); CREATE TABLE mt_children (id INTEGER PRIMARY KEY, active INTEGER NOT NULL); CREATE TABLE mt_junctions (id INTEGER PRIMARY KEY, parent INTEGER NOT NULL, target INTEGER NOT NULL, rank INTEGER NOT NULL, UNIQUE(parent, rank, id));`)
	require.NoError(t, err)
	_, err = database.Exec(`INSERT INTO mt_parents VALUES (1), (2); INSERT INTO mt_children VALUES (10, 1), (20, 1), (30, 1); INSERT INTO mt_junctions VALUES (1, 1, 10, 1), (2, 1, 10, 2), (3, 1, 20, 3), (4, 2, 10, 1), (5, 2, 20, 2);`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	base, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	counter := &mtCountingExecutor{Executor: base}
	// WithEngineProfile re-attaches the compiler the decorator does not carry.
	executor, err := rasql.WithEngineProfile(counter, profile)
	require.NoError(t, err)
	parents := rasql.MustReadTableOf[mtParentRow](schema.TableDef{Name: "mt_parents", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	children := rasql.MustReadTableOf[mtChildRow](schema.TableDef{Name: "mt_children", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "active", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	junctions := rasql.MustReadTableOf[mtJunctionRow](schema.TableDef{Name: "mt_junctions", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "target", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}})
	parentSource, err := rasql.SourceOf(parents, "p")
	require.NoError(t, err)
	childSource, err := rasql.SourceOf(children, "c")
	require.NoError(t, err)
	junctionSource, err := rasql.SourceOf(junctions, "j")
	require.NoError(t, err)
	pid, err := rasql.BindColumn[mtParentRow, int64](parentSource, "id", "")
	require.NoError(t, err)
	cid, err := rasql.BindColumn[mtChildRow, int64](childSource, "id", "")
	require.NoError(t, err)
	active, err := rasql.BindColumn[mtChildRow, int64](childSource, "active", "")
	require.NoError(t, err)
	jpid, err := rasql.BindColumn[mtJunctionRow, int64](junctionSource, "parent", "")
	require.NoError(t, err)
	jt, err := rasql.BindColumn[mtJunctionRow, int64](junctionSource, "target", "")
	require.NoError(t, err)
	jrank, err := rasql.BindColumn[mtJunctionRow, int64](junctionSource, "rank", "")
	require.NoError(t, err)
	jid, err := rasql.BindColumn[mtJunctionRow, int64](junctionSource, "id", "")
	require.NoError(t, err)
	ps, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	pp, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", pid.Expr(), schema.IntegerType{}, "")}, mtParentDecoder{schema: ps})
	require.NoError(t, err)
	cs, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "active", Type: schema.IntegerType{}})
	require.NoError(t, err)
	cp, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", cid.Expr(), schema.IntegerType{}, ""), rasql.Item("active", active.Expr(), schema.IntegerType{}, "")}, mtChildDecoder{schema: cs})
	require.NoError(t, err)
	parentQuery := rasql.Select(parentSource.Source(), pp)
	childQuery := rasql.Select(childSource.Source(), cp).Where(rasql.EqualValue(active.Expr(), int64(1)))
	childPlan, err := rasql.NewGraphPlan(childQuery, func(row mtChildRow) mtChildGraph { return mtChildGraph{ID: row.ID} })
	require.NoError(t, err)
	parentKey, err := rasql.NewGraphKey(rasql.KeyPart(pid, func(row mtParentRow) int64 { return row.ID }))
	require.NoError(t, err)
	junctionParent, err := rasql.NewGraphKey(rasql.KeyPart(jpid, func(row mtJunctionRow) int64 { return row.Parent }))
	require.NoError(t, err)
	junctionChild, err := rasql.NewGraphKey(rasql.KeyPart(jt, func(row mtJunctionRow) int64 { return row.Target }))
	require.NoError(t, err)
	childKey, err := rasql.NewGraphKey(rasql.KeyPart(cid, func(row mtChildRow) int64 { return row.ID }))
	require.NoError(t, err)
	_ = jid
	_ = jrank
	return executor, counter, parentQuery, parentKey, junctionParent, junctionChild, childKey, junctionSource.Source(), childPlan
}

func mtJunctionOrder(source rasql.Source) []rasql.OrderTerm {
	relation := rasql.Q1TypedRelation[mtJunctionRow](source)
	rank, _ := rasql.BindColumn[mtJunctionRow, int64](relation, "rank", "")
	id, _ := rasql.BindColumn[mtJunctionRow, int64](relation, "id", "")
	return []rasql.OrderTerm{rasql.AscExpr(rank.Expr()), rasql.AscExpr(id.Expr())}
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
type graphWidthParent struct {
	Children rasql.LoadedMany[graphWidthChild]
}

type graphWidthParentDecoder struct{ schema rasql.ResultSchema }

func (d graphWidthParentDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (graphWidthParentDecoder) Presence() []rasql.Presence         { return nil }
func (d graphWidthParentDecoder) DecodeRow(source rasql.ScanSource, value *graphWidthParentRow) error {
	return source.Scan(&value.ID, &value.Tenant)
}

type graphWidthChildDecoder struct{ schema rasql.ResultSchema }

func (d graphWidthChildDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (graphWidthChildDecoder) Presence() []rasql.Presence         { return nil }
func (d graphWidthChildDecoder) DecodeRow(source rasql.ScanSource, value *graphWidthChildRow) error {
	return source.Scan(&value.ID, &value.Tenant)
}

type graphWidthExecutor struct {
	rasql.Executor
	junctionStatements atomic.Int64
	childStatements    atomic.Int64
}

func (e *graphWidthExecutor) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
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
	// counter is the decorator that counts statements; executor is the same
	// decorator wrapped so it carries a compiler.
	counter                                *graphWidthExecutor
	executor                               rasql.Executor
	parents                                rasql.Source
	children                               rasql.Source
	junction                               rasql.Source
	parentQuery                            rasql.Query[graphWidthParentRow]
	childQuery                             rasql.Query[graphWidthChildRow]
	parentID, parentTenant                 rasql.Column[graphWidthParentRow, int64]
	childID, childTenant                   rasql.Column[graphWidthChildRow, int64]
	junctionParentID, junctionParentTenant rasql.Column[graphWidthJunctionRow, int64]
	junctionChildID, junctionChildTenant   rasql.Column[graphWidthJunctionRow, int64]
	kind                                   rasql.Column[graphWidthJunctionRow, int64]
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
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	base, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	counter := &graphWidthExecutor{Executor: base}
	executor, err := rasql.WithEngineProfile(counter, profile)
	require.NoError(t, err)
	parents, err := rasql.SourceOf(rasql.MustReadTableOf[graphWidthParentRow](schema.TableDef{
		Name: "r4_width_parents", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}},
	}), "wp")
	require.NoError(t, err)
	children, err := rasql.SourceOf(rasql.MustReadTableOf[graphWidthChildRow](schema.TableDef{
		Name: "r4_width_children", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}},
	}), "wc")
	require.NoError(t, err)
	junction, err := rasql.SourceOf(rasql.MustReadTableOf[graphWidthJunctionRow](schema.TableDef{
		Name: "r4_width_junctions", Columns: []schema.ColumnDef{
			{Name: "parent_id", Type: schema.IntegerType{}}, {Name: "parent_tenant", Type: schema.IntegerType{}},
			{Name: "child_id", Type: schema.IntegerType{}}, {Name: "child_tenant", Type: schema.IntegerType{}},
			{Name: "kind", Type: schema.IntegerType{}},
		},
	}), "wj")
	require.NoError(t, err)
	parentRelation := rasql.Q1TypedRelation[graphWidthParentRow](parents.Source())
	childRelation := rasql.Q1TypedRelation[graphWidthChildRow](children.Source())
	junctionRelation := rasql.Q1TypedRelation[graphWidthJunctionRow](junction.Source())
	parentID, err := rasql.BindColumn[graphWidthParentRow, int64](parentRelation, "id", "")
	require.NoError(t, err)
	parentTenant, err := rasql.BindColumn[graphWidthParentRow, int64](parentRelation, "tenant", "")
	require.NoError(t, err)
	childID, err := rasql.BindColumn[graphWidthChildRow, int64](childRelation, "id", "")
	require.NoError(t, err)
	childTenant, err := rasql.BindColumn[graphWidthChildRow, int64](childRelation, "tenant", "")
	require.NoError(t, err)
	junctionParentID, err := rasql.BindColumn[graphWidthJunctionRow, int64](junctionRelation, "parent_id", "")
	require.NoError(t, err)
	junctionParentTenant, err := rasql.BindColumn[graphWidthJunctionRow, int64](junctionRelation, "parent_tenant", "")
	require.NoError(t, err)
	junctionChildID, err := rasql.BindColumn[graphWidthJunctionRow, int64](junctionRelation, "child_id", "")
	require.NoError(t, err)
	junctionChildTenant, err := rasql.BindColumn[graphWidthJunctionRow, int64](junctionRelation, "child_tenant", "")
	require.NoError(t, err)
	kind, err := rasql.BindColumn[graphWidthJunctionRow, int64](junctionRelation, "kind", "")
	require.NoError(t, err)
	parentSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "tenant", Type: schema.IntegerType{}})
	require.NoError(t, err)
	parentProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", parentID.Expr(), schema.IntegerType{}, ""), rasql.Item("tenant", parentTenant.Expr(), schema.IntegerType{}, "")}, graphWidthParentDecoder{schema: parentSchema})
	require.NoError(t, err)
	childSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "tenant", Type: schema.IntegerType{}})
	require.NoError(t, err)
	childProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", childID.Expr(), schema.IntegerType{}, ""), rasql.Item("tenant", childTenant.Expr(), schema.IntegerType{}, "")}, graphWidthChildDecoder{schema: childSchema})
	require.NoError(t, err)
	return graphWidthFixture{
		counter: counter, executor: executor, parents: parents.Source(), children: children.Source(), junction: junction.Source(),
		parentQuery: rasql.Select(parents.Source(), parentProjection).OrderBy(rasql.AscExpr(parentID.Expr())),
		childQuery:  rasql.Select(children.Source(), childProjection).OrderBy(rasql.AscExpr(childID.Expr())),
		parentID:    parentID, parentTenant: parentTenant, childID: childID, childTenant: childTenant,
		junctionParentID: junctionParentID, junctionParentTenant: junctionParentTenant,
		junctionChildID: junctionChildID, junctionChildTenant: junctionChildTenant, kind: kind,
	}
}

func graphWidthPlan(t *testing.T, fixture graphWidthFixture, parentWidth, childWidth int, options rasql.EdgeOptions, marker string) rasql.GraphPlan[graphWidthParentRow, graphWidthParent] {
	t.Helper()
	var parentKey rasql.GraphKey[graphWidthParentRow]
	var childKey rasql.GraphKey[graphWidthChildRow]
	var junctionParent rasql.GraphKey[graphWidthJunctionRow]
	var junctionChild rasql.GraphKey[graphWidthJunctionRow]
	var err error
	if parentWidth == 1 {
		parentKey, err = rasql.NewGraphKey(rasql.KeyPart(fixture.parentID, func(row graphWidthParentRow) int64 { return row.ID }))
		require.NoError(t, err)
		junctionParent, err = rasql.NewGraphKey(rasql.KeyPart(fixture.junctionParentID, func(row graphWidthJunctionRow) int64 { return row.ParentID }))
	} else {
		parentKey, err = rasql.NewGraphKey(rasql.KeyPart(fixture.parentID, func(row graphWidthParentRow) int64 { return row.ID }), rasql.KeyPart(fixture.parentTenant, func(row graphWidthParentRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		junctionParent, err = rasql.NewGraphKey(rasql.KeyPart(fixture.junctionParentID, func(row graphWidthJunctionRow) int64 { return row.ParentID }), rasql.KeyPart(fixture.junctionParentTenant, func(row graphWidthJunctionRow) int64 { return row.ParentTenant }))
	}
	require.NoError(t, err)
	if childWidth == 1 {
		childKey, err = rasql.NewGraphKey(rasql.KeyPart(fixture.childID, func(row graphWidthChildRow) int64 { return row.ID }))
		require.NoError(t, err)
		junctionChild, err = rasql.NewGraphKey(rasql.KeyPart(fixture.junctionChildID, func(row graphWidthJunctionRow) int64 { return row.ChildID }))
	} else {
		childKey, err = rasql.NewGraphKey(rasql.KeyPart(fixture.childID, func(row graphWidthChildRow) int64 { return row.ID }), rasql.KeyPart(fixture.childTenant, func(row graphWidthChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		junctionChild, err = rasql.NewGraphKey(rasql.KeyPart(fixture.junctionChildID, func(row graphWidthJunctionRow) int64 { return row.ChildID }), rasql.KeyPart(fixture.junctionChildTenant, func(row graphWidthJunctionRow) int64 { return row.ChildTenant }))
	}
	require.NoError(t, err)
	childPlan, err := rasql.NewGraphPlan(fixture.childQuery, func(row graphWidthChildRow) graphWidthChild {
		return graphWidthChild{ID: row.ID, Tenant: row.Tenant, Marker: marker}
	})
	require.NoError(t, err)
	edge, err := rasql.ManyThrough("children", parentKey, junctionParent, junctionChild, childKey, fixture.junction, childPlan, options, func(parent *graphWidthParent, loaded rasql.LoadedMany[graphWidthChild]) { parent.Children = loaded })
	require.NoError(t, err)
	plan, err := rasql.NewGraphPlan(fixture.parentQuery, func(graphWidthParentRow) graphWidthParent { return graphWidthParent{} }, edge)
	require.NoError(t, err)
	return plan
}

// mtProfiled re-attaches the compiler a decorator does not carry, which is how
// a wrapper sits in the chain from outside the package.
func mtProfiled(t *testing.T, executor rasql.Executor) rasql.Executor {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	profiled, err := rasql.WithEngineProfile(executor, profile)
	require.NoError(t, err)
	return profiled
}
