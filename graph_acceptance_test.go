package rasql

import (
	"database/sql"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
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

func TestGraphSQLitePerParentLimitAndAbsentCompositeKeys(t *testing.T) {
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
	t.Logf("graph key widths parent=%d child=%d", len(parentKey.key.parts), len(childKey.key.parts))
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
}

func TestGraphPlanConcurrentReuse(t *testing.T) {
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
}

func TestGraphHasOneRequiresDuplicateDetectionLimit(t *testing.T) {
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
}
