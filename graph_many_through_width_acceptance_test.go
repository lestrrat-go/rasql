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

func TestGraphSQLiteManyThroughUnequalWidthsExecutes(t *testing.T) {
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
}

func TestGraphSQLiteManyThroughLegacyFixedValuesDoNotShareThroughCache(t *testing.T) {
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
}

func TestGraphSQLiteManyThroughBindLimitAboveProfileSplits(t *testing.T) {
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
}
