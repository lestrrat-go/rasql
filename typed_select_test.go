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

type selectUser struct {
	ID    int64
	Email string
}

type selectUserDecoder struct{ schema rasql.ResultSchema }

func (d selectUserDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (selectUserDecoder) Presence() []rasql.Presence         { return nil }
func (selectUserDecoder) DecodeRow(source rasql.ScanSource, result *selectUser) error {
	return source.Scan(&result.ID, &result.Email)
}

type selectFixture struct {
	executor rasql.Executor
	source   rasql.TypedRelation[selectUser]
	id       rasql.Column[selectUser, int64]
	email    rasql.Column[selectUser, string]
}

func newSelectFixture(t *testing.T) selectFixture {
	t.Helper()
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE users (id INTEGER, email TEXT)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO users VALUES (1, 'ada@example.com'), (2, 'bob@example.com')`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)
	table, err := rasql.ReadTableOf[selectUser](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	source, err := rasql.SourceOf(table, "users")
	require.NoError(t, err)
	id, err := rasql.BindColumn[selectUser, int64](source, "id", "")
	require.NoError(t, err)
	email, err := rasql.BindColumn[selectUser, string](source, "email", "")
	require.NoError(t, err)
	return selectFixture{executor: executor, source: source, id: id, email: email}
}

func selectUserQuery(t *testing.T, fixture selectFixture) rasql.Query[selectUser] {
	t.Helper()
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", fixture.id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", fixture.email.Expr(), schema.TextType{}, ""),
	}, selectUserDecoder{schema: resultSchema})
	require.NoError(t, err)
	return rasql.Select(fixture.source.Source(), projection).OrderBy(rasql.AscExpr(fixture.id.Expr()))
}

func TestTypedSelect(t *testing.T) {
	fixture := newSelectFixture(t)
	query := selectUserQuery(t, fixture)
	rows, err := rasql.All(t.Context(), fixture.executor, query)
	require.NoError(t, err)
	require.Equal(t, []selectUser{{ID: 1, Email: "ada@example.com"}, {ID: 2, Email: "bob@example.com"}}, rows)

	filtered := query.Where(rasql.EqualValue(fixture.id.Expr(), int64(2)))
	value, err := rasql.One(t.Context(), fixture.executor, filtered)
	require.NoError(t, err)
	require.Equal(t, selectUser{ID: 2, Email: "bob@example.com"}, value)

	empty := query.Where(rasql.EqualValue(fixture.id.Expr(), int64(9)))
	_, err = rasql.One(t.Context(), fixture.executor, empty)
	require.ErrorIs(t, err, rasql.ErrNoRows)
}

func TestSelectAll(t *testing.T) {
	fixture := newSelectFixture(t)
	query := selectUserQuery(t, fixture)
	page, err := query.Limit(1)
	require.NoError(t, err)
	rows, err := rasql.All(t.Context(), fixture.executor, page)
	require.NoError(t, err)
	require.Len(t, rows, 1)

	count := rasql.CountQuery(query, false)
	value, err := rasql.One(t.Context(), fixture.executor, count)
	require.NoError(t, err)
	require.Equal(t, int64(2), value)
}

func TestTypedSelectExecution(t *testing.T) {
	fixture := newSelectFixture(t)
	query := selectUserQuery(t, fixture)
	derived, err := rasql.Derive(query, "users_derived")
	require.NoError(t, err)
	id, err := rasql.BindResultColumn[selectUser, int64](derived, "id")
	require.NoError(t, err)
	require.NoError(t, rasql.CountQuery(query, false).Validate())
	_ = id
}
