package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type countUser struct {
	ID       int64
	Tenant   int64
	Category *string
}

type countUserDecoder struct{ schema rasql.ResultSchema }

func (d countUserDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (countUserDecoder) Presence() []rasql.Presence         { return nil }
func (countUserDecoder) DecodeRow(source rasql.ScanSource, result *countUser) error {
	var category sql.NullString
	if err := source.Scan(&result.ID, &result.Tenant, &category); err != nil {
		return err
	}
	if category.Valid {
		result.Category = &category.String
	}
	return nil
}

func countUsersQuery(t *testing.T) (rasql.Query[countUser], rasql.Executor) {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE users (id INTEGER, tenant INTEGER, category TEXT)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO users VALUES (1, 1, 'a'), (2, 1, NULL), (3, 1, NULL), (4, 2, 'b')`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	table, err := rasql.ReadTableOf[countUser](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "tenant", Type: schema.IntegerType{}},
		{Name: "category", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "users")
	require.NoError(t, err)
	id, err := rasql.BindColumn[countUser, int64](relation, "id", "")
	require.NoError(t, err)
	tenant, err := rasql.BindColumn[countUser, int64](relation, "tenant", "")
	require.NoError(t, err)
	category, err := rasql.BindNullColumn[countUser, string](relation, "category", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "tenant", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "category", Type: schema.TextType{}, Nullable: true},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("tenant", tenant.Expr(), schema.IntegerType{}, ""),
		rasql.NullItem("category", category.NullExpr(), schema.TextType{}, ""),
	}, countUserDecoder{schema: resultSchema})
	require.NoError(t, err)
	query := rasql.Select(relation.Source(), projection).Where(rasql.EqualExpr(tenant.Expr(), rasql.Value(int64(1)))).OrderBy(rasql.AscExpr(id.Expr()))
	return query, executor
}

func TestSQLiteTypedReusableCounts(t *testing.T) {
	base, executor := countUsersQuery(t)
	page, err := base.Limit(2)
	require.NoError(t, err)
	page, err = page.Offset(1)
	require.NoError(t, err)
	rows, err := rasql.All(t.Context(), executor, page)
	require.NoError(t, err)
	require.Equal(t, []countUser{{ID: 2, Tenant: 1}, {ID: 3, Tenant: 1}}, rows)

	count := rasql.CountQuery(base, false)
	value, err := rasql.One(t.Context(), executor, count)
	require.NoError(t, err)
	require.Equal(t, int64(3), value)
	pageCount := rasql.CountQuery(page, true)
	value, err = rasql.One(t.Context(), executor, pageCount)
	require.NoError(t, err)
	require.Equal(t, int64(2), value)
	beyond, err := base.Limit(2)
	require.NoError(t, err)
	beyond, err = beyond.Offset(20)
	require.NoError(t, err)
	value, err = rasql.One(t.Context(), executor, rasql.CountQuery(beyond, true))
	require.NoError(t, err)
	require.Equal(t, int64(0), value)

	derived, err := rasql.Derive(page, "users_page")
	require.NoError(t, err)
	id, err := rasql.BindResultColumn[countUser, int64](derived, "id")
	require.NoError(t, err)
	_ = id
}
