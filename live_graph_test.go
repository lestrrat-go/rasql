//go:build unix

package rasql_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type graphLiveParent struct{ ID int64 }
type graphLiveChild struct {
	ID       int64
	ParentID int64
	Rank     int64
}
type graphLiveValue struct {
	ID       int64
	Children rasql.LoadedMany[graphLiveChild]
}

type graphLiveDecoder struct{ schema rasql.ResultSchema }

func (d graphLiveDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (d graphLiveDecoder) Presence() []rasql.Presence       { return nil }
func (d graphLiveDecoder) DecodeRow(source rasql.ScanSource, row *graphLiveParent) error {
	return source.Scan(&row.ID)
}

type graphLiveChildDecoder struct{ schema rasql.ResultSchema }

func (d graphLiveChildDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (d graphLiveChildDecoder) Presence() []rasql.Presence       { return nil }
func (d graphLiveChildDecoder) DecodeRow(source rasql.ScanSource, row *graphLiveChild) error {
	return source.Scan(&row.ID, &row.ParentID, &row.Rank)
}

func TestGraphLiveExecution(t *testing.T) {
	t.Run("PostgreSQL", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)
		testLiveGraph(t, database, dialect.PostgreSQL(), "postgresql-17")
	})

	t.Run("MySQL", func(t *testing.T) {
		database := dbtest.MySQLDB(t)
		testLiveGraph(t, database, dialect.MySQL(), "mysql-8.4")
	})
}

func testLiveGraph(t *testing.T, database *sql.DB, d dialect.Dialect, profileID string) {
	t.Helper()
	parentsName := dbtest.UniqueName(t, "r4_graph_parents")
	childrenName := dbtest.UniqueName(t, "r4_graph_children")
	createParent := "CREATE TABLE " + parentsName + " (id BIGINT PRIMARY KEY)"
	createChild := "CREATE TABLE " + childrenName + " (id BIGINT PRIMARY KEY, parent_id BIGINT NOT NULL, rank_value BIGINT NOT NULL)"
	if d.Name() == "mysql" {
		createParent = "CREATE TABLE `" + parentsName + "` (id BIGINT PRIMARY KEY)"
		createChild = "CREATE TABLE `" + childrenName + "` (id BIGINT PRIMARY KEY, parent_id BIGINT NOT NULL, rank_value BIGINT NOT NULL)"
	}
	_, err := database.ExecContext(t.Context(), createParent)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), createChild)
	require.NoError(t, err)
	t.Cleanup(func() {
		_, _ = database.ExecContext(context.Background(), "DROP TABLE "+childrenName)
		_, _ = database.ExecContext(context.Background(), "DROP TABLE "+parentsName)
	})
	_, err = database.ExecContext(t.Context(), "INSERT INTO "+parentsName+" (id) VALUES (1), (2)")
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO "+childrenName+" (id, parent_id, rank_value) VALUES (11,1,2), (12,1,1), (21,2,1)")
	require.NoError(t, err)

	db, err := rasql.New(database, d)
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion(profileID, 17, 0, 0)
	if d.Name() == "mysql" {
		profile, err = rasql.EngineProfileFromVersion(profileID, 8, 4, 0)
	}
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)

	parentTable, err := rasql.ReadTableOf[graphLiveParent](schema.TableDef{Name: parentsName, PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	childTable, err := rasql.ReadTableOf[graphLiveChild](schema.TableDef{Name: childrenName, PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "parent_id", Type: schema.IntegerType{}}, {Name: "rank_value", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	parents, err := rasql.SourceOf(parentTable, "")
	require.NoError(t, err)
	children, err := rasql.SourceOf(childTable, "")
	require.NoError(t, err)
	parentID, err := rasql.BindColumn[graphLiveParent, int64](parents, "id", "")
	require.NoError(t, err)
	childID, err := rasql.BindColumn[graphLiveChild, int64](children, "id", "")
	require.NoError(t, err)
	childParent, err := rasql.BindColumn[graphLiveChild, int64](children, "parent_id", "")
	require.NoError(t, err)
	childRank, err := rasql.BindColumn[graphLiveChild, int64](children, "rank_value", "")
	require.NoError(t, err)
	parentSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	childSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "parent_id", Type: schema.IntegerType{}}, rasql.ResultColumn{Name: "rank_value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	parentProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", parentID.Expr(), schema.IntegerType{}, "")}, graphLiveDecoder{schema: parentSchema})
	require.NoError(t, err)
	childProjection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", childID.Expr(), schema.IntegerType{}, ""), rasql.Item("parent_id", childParent.Expr(), schema.IntegerType{}, ""), rasql.Item("rank_value", childRank.Expr(), schema.IntegerType{}, "")}, graphLiveChildDecoder{schema: childSchema})
	require.NoError(t, err)
	parentQuery := rasql.Select(parents.Source(), parentProjection)
	childQuery := rasql.Select(children.Source(), childProjection)
	childQuery = childQuery.OrderBy(rasql.AscExpr(childRank.Expr()), rasql.AscExpr(childID.Expr()))
	parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphLiveParent) int64 { return row.ID }))
	require.NoError(t, err)
	childKey, err := rasql.NewGraphKey(rasql.KeyPart(childParent, func(row graphLiveChild) int64 { return row.ParentID }))
	require.NoError(t, err)
	childPlan, err := rasql.NewGraphPlan(childQuery, func(row graphLiveChild) graphLiveChild { return row })
	require.NoError(t, err)
	edge, err := rasql.HasMany("children", parentKey, childKey, childPlan, rasql.EdgeOptions{PerParentLimit: 1}, func(parent *graphLiveValue, loaded rasql.LoadedMany[graphLiveChild]) { parent.Children = loaded })
	require.NoError(t, err)
	plan, err := rasql.NewGraphPlan(parentQuery, func(row graphLiveParent) graphLiveValue { return graphLiveValue{ID: row.ID} }, edge)
	require.NoError(t, err)
	values, err := rasql.LoadGraph(t.Context(), executor, plan)
	require.NoError(t, err)
	require.Len(t, values, 2)
	require.Equal(t, int64(1), values[0].ID)
	require.Equal(t, int64(2), values[1].ID)
	require.True(t, values[0].Children.Loaded)
	require.Len(t, values[0].Children.Values, 1)
	require.Equal(t, int64(12), values[0].Children.Values[0].ID)
	require.Len(t, values[1].Children.Values, 1)
	require.Equal(t, int64(21), values[1].Children.Values[0].ID)
}
