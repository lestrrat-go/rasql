package rasql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"strings"
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

type graphParentRow struct {
	ID     int64
	Tenant Nullable[int64]
}

type graphChildRow struct {
	ID     int64
	Parent int64
	Tenant int64
	Rank   int64
}

type graphParent struct {
	ID       int64
	Children LoadedMany[graphChild]
	Owner    LoadedOne[graphChild]
}

type graphChild struct {
	ID   int64
	Rank int64
}

type graphParentDecoder struct{ schema ResultSchema }

func (d graphParentDecoder) ResultSchema() ResultSchema { return d.schema }
func (graphParentDecoder) Presence() []Presence         { return nil }
func (d graphParentDecoder) DecodeRow(source ScanSource, row *graphParentRow) error {
	var tenant sql.NullInt64
	if err := source.Scan(&row.ID, &tenant); err != nil {
		return err
	}
	row.Tenant = Nullable[int64]{Value: tenant.Int64, Valid: tenant.Valid}
	return nil
}

type graphChildDecoder struct{ schema ResultSchema }

func (d graphChildDecoder) ResultSchema() ResultSchema { return d.schema }
func (graphChildDecoder) Presence() []Presence         { return nil }
func (d graphChildDecoder) DecodeRow(source ScanSource, row *graphChildRow) error {
	return source.Scan(&row.ID, &row.Parent, &row.Tenant, &row.Rank)
}

func graphAcceptanceFixture(t *testing.T, childRows int) (Executor, Source, Source, Source, Query[graphParentRow], Query[graphChildRow]) {
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
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	parentTable, err := ReadTableOf[graphParentRow](schema.TableDef{Name: "graph_parents", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true},
	}})
	require.NoError(t, err)
	childTable, err := ReadTableOf[graphChildRow](schema.TableDef{Name: "graph_children", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}},
		{Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)
	parents, err := SourceOf(parentTable, "p")
	require.NoError(t, err)
	children, err := SourceOf(childTable, "c")
	require.NoError(t, err)
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
	childQuery := Select(children.Source(), childProjection).OrderBy(AscExpr(childRank.Expr()), AscExpr(childID.Expr()))
	return executor, parents.Source(), children.Source(), Source{}, parentQuery, childQuery
}

func TestGraphExecution(t *testing.T) {
	t.Run("a per-parent limit and absent composite keys", func(t *testing.T) {
		executor, _, _, _, parentQuery, childQuery := graphAcceptanceFixture(t, 4_750)
		parentTable := MustReadTableOf[graphParentRow](schema.TableDef{Name: "graph_parents", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true}}})
		childTable := MustReadTableOf[graphChildRow](schema.TableDef{Name: "graph_children", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}})
		parentRelation, err := SourceOf(parentTable, "p")
		require.NoError(t, err)
		childRelation, err := SourceOf(childTable, "c")
		require.NoError(t, err)
		parentID, err := BindColumn[graphParentRow, int64](parentRelation, "id", "")
		require.NoError(t, err)
		parentTenant, err := BindNullColumn[graphParentRow, int64](parentRelation, "tenant", "")
		require.NoError(t, err)
		childParent, err := BindColumn[graphChildRow, int64](childRelation, "parent", "")
		require.NoError(t, err)
		childTenant, err := BindColumn[graphChildRow, int64](childRelation, "tenant", "")
		require.NoError(t, err)
		parentKey, err := NewGraphKey(KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), NullKeyPart(parentTenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }))
		require.NoError(t, err)
		childKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		t.Logf("graph key widths parent=%d child=%d", len(parentKey.key.Parts), len(childKey.key.Parts))
		childPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID, Rank: row.Rank} })
		require.NoError(t, err)
		edge, err := HasMany("children", parentKey, childKey, childPlan, EdgeOptions{PerParentLimit: 5}, func(parent *graphParent, loaded LoadedMany[graphChild]) { parent.Children = loaded })
		require.NoError(t, err)
		plan, err := NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.NoError(t, err)
		values, err := LoadGraph(t.Context(), executor, plan)
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
		parentTable := MustReadTableOf[graphParentRow](schema.TableDef{Name: "graph_parents", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true}}})
		childTable := MustReadTableOf[graphChildRow](schema.TableDef{Name: "graph_children", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}})
		parents, err := SourceOf(parentTable, "p")
		require.NoError(t, err)
		children, err := SourceOf(childTable, "c")
		require.NoError(t, err)
		pid, err := BindColumn[graphParentRow, int64](parents, "id", "")
		require.NoError(t, err)
		cparent, err := BindColumn[graphChildRow, int64](children, "parent", "")
		require.NoError(t, err)
		ptenant, err := BindNullColumn[graphParentRow, int64](parents, "tenant", "")
		require.NoError(t, err)
		pk, err := NewGraphKey(KeyPart(pid, func(row graphParentRow) int64 { return row.ID }), NullKeyPart(ptenant, func(row graphParentRow) Nullable[int64] { return row.Tenant }))
		require.NoError(t, err)
		ctenant, err := BindColumn[graphChildRow, int64](children, "tenant", "")
		require.NoError(t, err)
		ck, err := NewGraphKey(KeyPart(cparent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(ctenant, func(row graphChildRow) int64 { return row.Tenant }))
		require.NoError(t, err)
		childPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
		require.NoError(t, err)
		edge, err := HasMany("children", pk, ck, childPlan, EdgeOptions{}, func(parent *graphParent, loaded LoadedMany[graphChild]) { parent.Children = loaded })
		require.NoError(t, err)
		plan, err := NewGraphPlan(parentQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
		require.NoError(t, err)
		var wg sync.WaitGroup
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				values, loadErr := LoadGraph(t.Context(), executor, plan)
				require.NoError(t, loadErr)
				require.Len(t, values, 500)
			}()
		}
		wg.Wait()
	})

	t.Run("has-one requires a duplicate-detection limit", func(t *testing.T) {
		_, _, _, _, parentQuery, childQuery := graphAcceptanceFixture(t, 0)
		parentTable := MustReadTableOf[graphParentRow](schema.TableDef{Name: "graph_parents", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}, Nullable: true}}})
		childTable := MustReadTableOf[graphChildRow](schema.TableDef{Name: "graph_children", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent", Type: schema.IntegerType{}}, {Name: "tenant", Type: schema.IntegerType{}}, {Name: "rank", Type: schema.IntegerType{}}}})
		parents, err := SourceOf(parentTable, "p")
		require.NoError(t, err)
		children, err := SourceOf(childTable, "c")
		require.NoError(t, err)
		parentID, err := BindColumn[graphParentRow, int64](parents, "id", "")
		require.NoError(t, err)
		childParent, err := BindColumn[graphChildRow, int64](children, "parent", "")
		require.NoError(t, err)
		parentKey, err := NewGraphKey(KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }))
		require.NoError(t, err)
		childKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }))
		require.NoError(t, err)
		childPlan, err := NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
		require.NoError(t, err)
		_, err = HasOne("owner", parentKey, childKey, childPlan, EdgeOptions{PerParentLimit: 1}, func(*graphParent, LoadedOne[graphChild]) {})
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_graph_edge", planErr.Code)
		_ = parentQuery
	})

	t.Run("ten heterogeneous levels copy their callbacks", func(t *testing.T) {
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
	})

	t.Run("duplicate attachments own independent slices", func(t *testing.T) {
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
	})

	t.Run("the bind budget uses fixed and composite batches", func(t *testing.T) {
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
	})

	t.Run("execution bounds rows at the SQL boundary", func(t *testing.T) {
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
	})

	t.Run("preflight validates shared child edges before the root query", func(t *testing.T) {
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
	})

	t.Run("a mapper panic reports the decoded row count", func(t *testing.T) {
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
	})

	t.Run("a direct child mapper panic reports the decoded row count", func(t *testing.T) {
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
	})

	t.Run("has-one cardinality is checked before mapping", func(t *testing.T) {
		base, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
		provider, ok := base.(compilerProvider)
		require.True(t, ok)
		executor := &lifecycleExecutor{Executor: base, compiler: provider.queryCompiler(), rows: []*runtimeFakeRows{
			{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}},
			{columns: []string{"id", "parent", "tenant", "rank"}, values: [][]any{
				{int64(11), int64(1), int64(1), int64(0)}, {int64(12), int64(1), int64(1), int64(1)},
			}},
		}}
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
		var mapped, attached atomic.Int64
		childPlan, err := NewGraphPlan(childQuery, func(graphChildRow) graphChild {
			mapped.Add(1)
			panic("has-one mapper must not run")
		})
		require.NoError(t, err)
		edge, err := HasOne("owner", parentKey, childKey, childPlan, EdgeOptions{}, func(*graphParent, LoadedOne[graphChild]) {
			attached.Add(1)
		})
		require.NoError(t, err)
		limitedParent, err := parentQuery.Limit(1)
		require.NoError(t, err)
		plan, err := NewGraphPlan(limitedParent, func(graphParentRow) graphParent { return graphParent{} }, edge)
		require.NoError(t, err)

		_, err = LoadGraph(t.Context(), executor, plan)
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "cardinality", planErr.Code)
		require.Zero(t, mapped.Load())
		require.Zero(t, attached.Load())
	})
}

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

type graphRuntimeCountingExecutor struct {
	Executor
	compiler *querycompile.Compiler
	queries  atomic.Int64
}

func (e *graphRuntimeCountingExecutor) queryCompiler() *querycompile.Compiler { return e.compiler }

func (e *graphRuntimeCountingExecutor) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	e.queries.Add(1)
	return e.Executor.Query(ctx, statement)
}
