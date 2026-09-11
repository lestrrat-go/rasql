package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type graphParentRow struct {
	ID     int64
	Tenant rasql.Nullable[int64]
}

type graphChildRow struct {
	ID     int64
	Parent int64
	Tenant int64
	Rank   int64
}

type graphParent struct {
	ID       int64
	Children rasql.LoadedMany[graphChild]
	Owner    rasql.LoadedOne[graphChild]
}

type graphChild struct {
	ID   int64
	Rank int64
}

type graphParentDecoder struct{ schema rasql.ResultSchema }

func (d graphParentDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (graphParentDecoder) Presence() []rasql.Presence         { return nil }
func (d graphParentDecoder) DecodeRow(source rasql.ScanSource, row *graphParentRow) error {
	var tenant sql.NullInt64
	if err := source.Scan(&row.ID, &tenant); err != nil {
		return err
	}
	row.Tenant = rasql.Nullable[int64]{Value: tenant.Int64, Valid: tenant.Valid}
	return nil
}

type graphChildDecoder struct{ schema rasql.ResultSchema }

func (d graphChildDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (graphChildDecoder) Presence() []rasql.Presence         { return nil }
func (d graphChildDecoder) DecodeRow(source rasql.ScanSource, row *graphChildRow) error {
	return source.Scan(&row.ID, &row.Parent, &row.Tenant, &row.Rank)
}

func graphAcceptanceFixture(t *testing.T, childRows int) (rasql.Executor, rasql.Source, rasql.Source, rasql.Source, rasql.Query[graphParentRow], rasql.Query[graphChildRow]) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.Exec(`CREATE TABLE graph_parents (id INTEGER NOT NULL PRIMARY KEY, tenant INTEGER)`)
	require.NoError(t, err)
	_, err = database.Exec(`CREATE TABLE graph_children (id INTEGER NOT NULL PRIMARY KEY, parent INTEGER NOT NULL, tenant INTEGER NOT NULL, rank INTEGER NOT NULL)`)
	require.NoError(t, err)
	for i := 1; i <= 500; i++ {
		var tenant any = int64(1)
		if i > 475 {
			tenant = nil
		}
		_, err = database.Exec(`INSERT INTO graph_parents(id, tenant) VALUES (?, ?)`, i, tenant)
		require.NoError(t, err)
	}
	for i := 1; i <= childRows; i++ {
		parent := (i-1)/10 + 1
		_, err = database.Exec(`INSERT INTO graph_children(id, parent, tenant, rank) VALUES (?, ?, 1, ?)`, i, parent, (i-1)%10)
		require.NoError(t, err)
	}
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	parentTable, err := rasql.ReadTableOf[graphParentRow](schema.TableDef{Name: "graph_parents", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true},
	}})
	require.NoError(t, err)
	childTable, err := rasql.ReadTableOf[graphChildRow](schema.TableDef{Name: "graph_children", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}},
		{Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)
	parents, err := rasql.SourceOf(parentTable, "p")
	require.NoError(t, err)
	children, err := rasql.SourceOf(childTable, "c")
	require.NoError(t, err)
	parentID, err := rasql.BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := rasql.BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childID, err := rasql.BindColumn[graphChildRow, int64](children, "id", "")
	require.NoError(t, err)
	childParent, err := rasql.BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := rasql.BindColumn[graphChildRow, int64](children, "tenant", "")
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
	childQuery := rasql.Select(children.Source(), childProjection).OrderBy(rasql.AscExpr(childRank.Expr()), rasql.AscExpr(childID.Expr()))
	return executor, parents.Source(), children.Source(), rasql.Source{}, parentQuery, childQuery
}

func TestGraphExecution(t *testing.T) {
	t.Run("a per-parent limit and absent composite keys", func(t *testing.T) {
		executor, _, _, _, parentQuery, childQuery := graphAcceptanceFixture(t, 4_750)
		parentTable := rasql.MustReadTableOf[graphParentRow](schema.TableDef{Name: "graph_parents", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true}}})
		childTable := rasql.MustReadTableOf[graphChildRow](schema.TableDef{Name: "graph_children", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}})
		parentRelation, err := rasql.SourceOf(parentTable, "p")
		require.NoError(t, err)
		childRelation, err := rasql.SourceOf(childTable, "c")
		require.NoError(t, err)
		parentID, err := rasql.BindColumn[graphParentRow, int64](parentRelation, "id", "")
		require.NoError(t, err)
		parentTenant, err := rasql.BindNullColumn[graphParentRow, int64](parentRelation, "tenant", "")
		require.NoError(t, err)
		childParent, err := rasql.BindColumn[graphChildRow, int64](childRelation, "parent", "")
		require.NoError(t, err)
		childTenant, err := rasql.BindColumn[graphChildRow, int64](childRelation, "tenant", "")
		require.NoError(t, err)
		parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }))
		require.NoError(t, err)
		childKey, err := rasql.NewGraphKey(rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		t.Logf("graph key widths parent=%d child=%d", len(rasql.Q1GraphKeySpec(parentKey).Parts), len(rasql.Q1GraphKeySpec(childKey).Parts))
		childPlan, err := rasql.NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID, Rank: row.Rank} })
		require.NoError(t, err)
		edge, err := rasql.HasMany("children", parentKey, childKey, childPlan, rasql.EdgeOptions{PerParentLimit: 5}, func(parent *graphParent, loaded rasql.LoadedMany[graphChild]) { parent.Children = loaded })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 500)
		for i, value := range values {
			require.True(t, value.Children.Loaded, "parent %d", i)
			require.NotNil(t, value.Children.Values, "parent %d", i)
			require.LessOrEqual(t, len(value.Children.Values), 5, "parent %d", i)
			if i >= 475 {
				require.Empty(t, value.Children.Values, "absent parent %d", i)
			}
		}
	})

	t.Run("a plan is reusable concurrently", func(t *testing.T) {
		executor, _, _, _, parentQuery, childQuery := graphAcceptanceFixture(t, 100)
		parentTable := rasql.MustReadTableOf[graphParentRow](schema.TableDef{Name: "graph_parents", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true}}})
		childTable := rasql.MustReadTableOf[graphChildRow](schema.TableDef{Name: "graph_children", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}})
		parents, err := rasql.SourceOf(parentTable, "p")
		require.NoError(t, err)
		children, err := rasql.SourceOf(childTable, "c")
		require.NoError(t, err)
		pid, err := rasql.BindColumn[graphParentRow, int64](parents, "id", "")
		require.NoError(t, err)
		cparent, err := rasql.BindColumn[graphChildRow, int64](children, "parent", "")
		require.NoError(t, err)
		ptenant, err := rasql.BindNullColumn[graphParentRow, int64](parents, "tenant", "")
		require.NoError(t, err)
		pk, err := rasql.NewGraphKey(rasql.KeyPart(pid, func(row graphParentRow) int64 { return row.ID }), rasql.NullKeyPart(ptenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }))
		require.NoError(t, err)
		ctenant, err := rasql.BindColumn[graphChildRow, int64](children, "tenant", "")
		require.NoError(t, err)
		ck, err := rasql.NewGraphKey(rasql.KeyPart(cparent, func(row graphChildRow) int64 { return row.Parent }), rasql.KeyPart(ctenant, func(row graphChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		childPlan, err := rasql.NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
		require.NoError(t, err)
		edge, err := rasql.HasMany("children", pk, ck, childPlan, rasql.EdgeOptions{}, func(parent *graphParent, loaded rasql.LoadedMany[graphChild]) { parent.Children = loaded })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.NoError(t, err)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				values, loadErr := rasql.LoadGraph(t.Context(), executor, plan)
				require.NoError(t, loadErr)
				require.Len(t, values, 500)
			}()
		}
		wg.Wait()
	})

	t.Run("has-one requires a duplicate-detection limit", func(t *testing.T) {
		_, _, _, _, parentQuery, childQuery := graphAcceptanceFixture(t, 0)
		parentTable := rasql.MustReadTableOf[graphParentRow](schema.TableDef{Name: "graph_parents", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true}}})
		childTable := rasql.MustReadTableOf[graphChildRow](schema.TableDef{Name: "graph_children", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}})
		parents, err := rasql.SourceOf(parentTable, "p")
		require.NoError(t, err)
		children, err := rasql.SourceOf(childTable, "c")
		require.NoError(t, err)
		parentID, err := rasql.BindColumn[graphParentRow, int64](parents, "id", "")
		require.NoError(t, err)
		childParent, err := rasql.BindColumn[graphChildRow, int64](children, "parent", "")
		require.NoError(t, err)
		parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }))
		require.NoError(t, err)
		childKey, err := rasql.NewGraphKey(rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }))
		require.NoError(t, err)
		childPlan, err := rasql.NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
		require.NoError(t, err)
		_, err = rasql.HasOne("owner", parentKey, childKey, childPlan, rasql.EdgeOptions{PerParentLimit: 1}, func(*graphParent, rasql.LoadedOne[graphChild]) {})
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_graph_edge", planErr.Code)
		_ = parentQuery
	})

	t.Run("ten heterogeneous levels copy their callbacks", func(t *testing.T) {
		base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
		parents := rasql.Q1TypedRelation[graphParentRow](parentSource)
		children := rasql.Q1TypedRelation[graphChildRow](childSource)
		parentID, err := rasql.BindColumn[graphParentRow, int64](parents, "id", "")
		require.NoError(t, err)
		parentTenant, err := rasql.BindNullColumn[graphParentRow, int64](parents, "tenant", "")
		require.NoError(t, err)
		childID, err := rasql.BindColumn[graphChildRow, int64](children, "id", "")
		require.NoError(t, err)
		childParent, err := rasql.BindColumn[graphChildRow, int64](children, "parent", "")
		require.NoError(t, err)
		childTenant, err := rasql.BindColumn[graphChildRow, int64](children, "tenant", "")
		require.NoError(t, err)
		rootKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }))
		require.NoError(t, err)
		childParentKey, err := rasql.NewGraphKey(rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		childIDKey, err := rasql.NewGraphKey(rasql.KeyPart(childID, func(row graphChildRow) int64 { return row.ID }), rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		p10, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep10 { return graphDeep10{} })
		require.NoError(t, err)
		e9, err := rasql.HasMany("level10", childIDKey, childParentKey, p10, rasql.EdgeOptions{}, func(parent *graphDeep9, loaded rasql.LoadedMany[graphDeep10]) { parent.Next = loaded })
		require.NoError(t, err)
		p9, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep9 { return graphDeep9{} }, e9)
		require.NoError(t, err)
		e8, err := rasql.HasMany("level9", childIDKey, childParentKey, p9, rasql.EdgeOptions{}, func(parent *graphDeep8, loaded rasql.LoadedMany[graphDeep9]) { parent.Next = loaded })
		require.NoError(t, err)
		p8, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep8 { return graphDeep8{} }, e8)
		require.NoError(t, err)
		e7, err := rasql.HasMany("level8", childIDKey, childParentKey, p8, rasql.EdgeOptions{}, func(parent *graphDeep7, loaded rasql.LoadedMany[graphDeep8]) { parent.Next = loaded })
		require.NoError(t, err)
		p7, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep7 { return graphDeep7{} }, e7)
		require.NoError(t, err)
		e6, err := rasql.HasMany("level7", childIDKey, childParentKey, p7, rasql.EdgeOptions{}, func(parent *graphDeep6, loaded rasql.LoadedMany[graphDeep7]) { parent.Next = loaded })
		require.NoError(t, err)
		p6, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep6 { return graphDeep6{} }, e6)
		require.NoError(t, err)
		e5, err := rasql.HasMany("level6", childIDKey, childParentKey, p6, rasql.EdgeOptions{}, func(parent *graphDeep5, loaded rasql.LoadedMany[graphDeep6]) { parent.Next = loaded })
		require.NoError(t, err)
		p5, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep5 { return graphDeep5{} }, e5)
		require.NoError(t, err)
		e4, err := rasql.HasMany("level5", childIDKey, childParentKey, p5, rasql.EdgeOptions{}, func(parent *graphDeep4, loaded rasql.LoadedMany[graphDeep5]) { parent.Next = loaded })
		require.NoError(t, err)
		p4, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep4 { return graphDeep4{} }, e4)
		require.NoError(t, err)
		e3, err := rasql.HasMany("level4", childIDKey, childParentKey, p4, rasql.EdgeOptions{}, func(parent *graphDeep3, loaded rasql.LoadedMany[graphDeep4]) { parent.Next = loaded })
		require.NoError(t, err)
		p3, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep3 { return graphDeep3{} }, e3)
		require.NoError(t, err)
		e2, err := rasql.HasMany("level3", childIDKey, childParentKey, p3, rasql.EdgeOptions{}, func(parent *graphDeep2, loaded rasql.LoadedMany[graphDeep3]) { parent.Next = loaded })
		require.NoError(t, err)
		p2, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep2 { return graphDeep2{} }, e2)
		require.NoError(t, err)
		e1, err := rasql.HasMany("level2", childIDKey, childParentKey, p2, rasql.EdgeOptions{}, func(parent *graphDeep1, loaded rasql.LoadedMany[graphDeep2]) { parent.Next = loaded })
		require.NoError(t, err)
		p1, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphDeep1 { return graphDeep1{} }, e1)
		require.NoError(t, err)
		rootEdge, err := rasql.HasMany("level1", rootKey, childParentKey, p1, rasql.EdgeOptions{}, func(parent *graphDeepRoot, loaded rasql.LoadedMany[graphDeep1]) { parent.Children = loaded })
		require.NoError(t, err)
		limitedParent, err := parentQuery.Limit(1)
		require.NoError(t, err)
		root, err := rasql.NewGraphPlan(limitedParent, func(graphParentRow) graphDeepRoot { return graphDeepRoot{} }, rootEdge)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), base, root)
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
	})

	t.Run("duplicate attachments own independent slices", func(t *testing.T) {
		base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 10)
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
		first, err := rasql.HasMany("first", parentKey, childKey, childPlan, rasql.EdgeOptions{}, func(parent *graphDuplicate, loaded rasql.LoadedMany[graphChild]) { parent.First = loaded })
		require.NoError(t, err)
		second, err := rasql.HasMany("second", parentKey, childKey, childPlan, rasql.EdgeOptions{}, func(parent *graphDuplicate, loaded rasql.LoadedMany[graphChild]) { parent.Second = loaded })
		require.NoError(t, err)
		limitedParent, err := parentQuery.Limit(1)
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(limitedParent, func(graphParentRow) graphDuplicate { return graphDuplicate{} }, first, second)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), base, plan)
		require.NoError(t, err)
		require.Len(t, values, 1)
		require.True(t, values[0].First.Loaded)
		require.True(t, values[0].Second.Loaded)
		require.NotSame(t, &values[0].First.Values[0], &values[0].Second.Values[0])
		values[0].First.Values[0].ID = 999
		require.NotEqual(t, int64(999), values[0].Second.Values[0].ID)
	})

	t.Run("the bind budget uses fixed and composite batches", func(t *testing.T) {
		base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 110)
		counter := &graphCountingExecutor{Executor: base}
		executor := graphProfiled(t, counter)
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
		childRank, err := rasql.BindColumn[graphChildRow, int64](children, "rank", "")
		require.NoError(t, err)
		parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }))
		require.NoError(t, err)
		childKey, err := rasql.NewGraphKey(rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		childPlan, err := rasql.NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
		require.NoError(t, err)
		where := rasql.And(rasql.EqualValue(childRank.Expr(), int64(1)), rasql.EqualValue(childRank.Expr(), int64(2)), rasql.EqualValue(childRank.Expr(), int64(3)))
		edge, err := rasql.HasMany("children", parentKey, childKey, childPlan, rasql.EdgeOptions{Where: where, BindLimit: 13}, func(parent *graphParent, loaded rasql.LoadedMany[graphChild]) { parent.Children = loaded })
		require.NoError(t, err)
		limitedParent, err := parentQuery.Limit(11)
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(limitedParent, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.NoError(t, err)
		_, err = rasql.LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Equal(t, []int{13, 13, 5}, counter.bindCounts)
	})

	t.Run("execution bounds rows at the SQL boundary", func(t *testing.T) {
		base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 5000)
		counter := &graphCountingExecutor{Executor: base}
		executor := graphProfiled(t, counter)

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
		edge, err := rasql.HasMany("children", parentKey, childKey, childPlan, rasql.EdgeOptions{PerParentLimit: 5}, func(parent *graphParent, loaded rasql.LoadedMany[graphChild]) { parent.Children = loaded })
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.NoError(t, err)
		values, err := rasql.LoadGraph(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Len(t, values, 500)
		require.LessOrEqual(t, counter.childRows.Load(), int64(2375))
		require.NotEmpty(t, counter.childStatements.Load())
		for _, value := range values {
			require.True(t, value.Children.Loaded)
			require.NotNil(t, value.Children.Values)
			require.LessOrEqual(t, len(value.Children.Values), 5)
		}
	})

	t.Run("preflight validates shared child edges before the root query", func(t *testing.T) {
		base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
		counter := &graphRuntimeCountingExecutor{Executor: base}
		executor := graphProfiled(t, counter)

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
		parentKey, err := rasql.NewGraphKey(
			rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }),
			rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }),
		)
		require.NoError(t, err)
		childKey, err := rasql.NewGraphKey(
			rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }),
			rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }),
		)
		require.NoError(t, err)
		childrenPlan, err := rasql.NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
		require.NoError(t, err)
		first, err := rasql.HasMany("first", parentKey, childKey, childrenPlan, rasql.EdgeOptions{}, func(*graphParent, rasql.LoadedMany[graphChild]) {})
		require.NoError(t, err)
		second, err := rasql.HasMany("second", parentKey, childKey, childrenPlan, rasql.EdgeOptions{BindLimit: 1}, func(*graphParent, rasql.LoadedMany[graphChild]) {})
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(graphParentRow) graphParent { return graphParent{} }, first, second)
		require.NoError(t, err)

		_, err = rasql.LoadGraph(t.Context(), executor, plan)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "bind_limit", planErr.Code)
		require.Zero(t, counter.queries.Load())
	})

	t.Run("a mapper panic reports the decoded row count", func(t *testing.T) {
		base, _, _, _, parentQuery, _ := graphAcceptanceFixture(t, 1)
		parentQuery, err := parentQuery.Limit(1)
		require.NoError(t, err)
		var terminal rasql.Event
		observed, err := rasql.WithEventObservers(base, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			if event.Kind != rasql.EventGraph {
				return ctx, nil
			}
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, event rasql.Event) error {
				terminal = event
				return nil
			})
		}))
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(graphParentRow) graphParent { panic("mapper panic") })
		require.NoError(t, err)
		require.Panics(t, func() { _, _ = rasql.LoadGraph(t.Context(), observed, plan) })
		require.Equal(t, rasql.EventGraph, terminal.Kind)
		require.Equal(t, rasql.EventTerminal, terminal.Phase)
		require.Equal(t, int64(1), terminal.Rows)
	})

	t.Run("a direct child mapper panic reports the decoded row count", func(t *testing.T) {
		base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
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
		parentKey, err := rasql.NewGraphKey(
			rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }),
			rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }),
		)
		require.NoError(t, err)
		childKey, err := rasql.NewGraphKey(
			rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }),
			rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }),
		)
		require.NoError(t, err)
		childrenPlan, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphChild { panic("child mapper panic") })
		require.NoError(t, err)
		edge, err := rasql.HasMany("children", parentKey, childKey, childrenPlan, rasql.EdgeOptions{}, func(*graphParent, rasql.LoadedMany[graphChild]) {})
		require.NoError(t, err)
		parentQuery, err = parentQuery.Limit(1)
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(parentQuery, func(graphParentRow) graphParent { return graphParent{} }, edge)
		require.NoError(t, err)

		var terminal rasql.Event
		observed, err := rasql.WithEventObservers(base, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
			if event.Kind != rasql.EventGraph {
				return ctx, nil
			}
			return ctx, rasql.EventCompletionFunc(func(_ context.Context, event rasql.Event) error {
				terminal = event
				return nil
			})
		}))
		require.NoError(t, err)
		require.Panics(t, func() { _, _ = rasql.LoadGraph(t.Context(), observed, plan) })
		require.Equal(t, rasql.EventGraph, terminal.Kind)
		require.Equal(t, rasql.EventTerminal, terminal.Phase)
		require.Equal(t, int64(2), terminal.Rows)
	})

	t.Run("has-one cardinality is checked before mapping", func(t *testing.T) {
		base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
		counter := &lifecycleExecutor{Executor: base, rows: []*runtimeFakeRows{
			{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}},
			{columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{
				{int64(11), int64(1), int64(1), int64(0)}, {int64(12), int64(1), int64(1), int64(1)},
			}},
		}}
		executor := graphProfiled(t, counter)
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
		parentKey, err := rasql.NewGraphKey(
			rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }),
			rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }),
		)
		require.NoError(t, err)
		childKey, err := rasql.NewGraphKey(
			rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }),
			rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }),
		)
		require.NoError(t, err)
		var mapped, attached atomic.Int64
		childPlan, err := rasql.NewGraphPlan(childQuery, func(graphChildRow) graphChild {
			mapped.Add(1)
			panic("has-one mapper must not run")
		})
		require.NoError(t, err)
		edge, err := rasql.HasOne("owner", parentKey, childKey, childPlan, rasql.EdgeOptions{}, func(*graphParent, rasql.LoadedOne[graphChild]) {
			attached.Add(1)
		})
		require.NoError(t, err)
		limitedParent, err := parentQuery.Limit(1)
		require.NoError(t, err)
		plan, err := rasql.NewGraphPlan(limitedParent, func(graphParentRow) graphParent { return graphParent{} }, edge)
		require.NoError(t, err)

		_, err = rasql.LoadGraph(t.Context(), executor, plan)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "cardinality", planErr.Code)
		require.Zero(t, mapped.Load())
		require.Zero(t, attached.Load())
	})
}

// graphCountingExecutor counts rows at the ResultRows boundary. Counting
// decoded slices would miss rows discarded by a client-side partition limit.
type graphCountingExecutor struct {
	rasql.Executor
	childRows       atomic.Int64
	childStatements atomic.Int64
	bindCounts      []int
}

func (e *graphCountingExecutor) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
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
	First  rasql.LoadedMany[graphChild]
	Second rasql.LoadedMany[graphChild]
}

type graphDeep1 struct{ Next rasql.LoadedMany[graphDeep2] }
type graphDeep2 struct{ Next rasql.LoadedMany[graphDeep3] }
type graphDeep3 struct{ Next rasql.LoadedMany[graphDeep4] }
type graphDeep4 struct{ Next rasql.LoadedMany[graphDeep5] }
type graphDeep5 struct{ Next rasql.LoadedMany[graphDeep6] }
type graphDeep6 struct{ Next rasql.LoadedMany[graphDeep7] }
type graphDeep7 struct{ Next rasql.LoadedMany[graphDeep8] }
type graphDeep8 struct{ Next rasql.LoadedMany[graphDeep9] }
type graphDeep9 struct{ Next rasql.LoadedMany[graphDeep10] }
type graphDeep10 struct{}
type graphDeepRoot struct{ Children rasql.LoadedMany[graphDeep1] }

func (*graphCountingExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

type graphCountingRows struct {
	rasql.ResultRows
	child *atomic.Int64
}

func (r *graphCountingRows) Next() bool {
	if !r.ResultRows.Next() {
		return false
	}
	r.child.Add(1)
	return true
}

type graphRuntimeCountingExecutor struct {
	rasql.Executor
	queries atomic.Int64
}

func (e *graphRuntimeCountingExecutor) Query(ctx context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
	e.queries.Add(1)
	return e.Executor.Query(ctx, statement)
}

// graphProfiled gives a decorator built outside this package the compiler that
// graph calls need, which a bare decorator does not carry on its own.
func graphProfiled(t *testing.T, executor rasql.Executor) rasql.Executor {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	profiled, err := rasql.WithEngineProfile(executor, profile)
	require.NoError(t, err)
	return profiled
}
