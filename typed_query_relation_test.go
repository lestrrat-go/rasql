package rasql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type reusableUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
}

type reusableEmail struct {
	Email string `rasql:"email"`
}

func reusableUsers(t *testing.T) rasql.Table[reusableUser] {
	t.Helper()
	users, err := rasql.TableOf[reusableUser](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	return users
}

func TestTypedReusableQueryValues(t *testing.T) {
	users := reusableUsers(t)
	base := rasql.SelectFrom(users).WhereEqual(users.Column("id"), 7)
	statement, err := base.Select()
	require.NoError(t, err)
	require.Len(t, statement.Projections(), 2)
	result, err := base.Result()
	require.NoError(t, err)
	require.Len(t, result.Columns(), 2)

	dto := rasql.RebindResult[reusableEmail](base, []query.ResultColumn{{Name: "email", Type: schema.TextType{}}}, users.Column("email"))
	dtoStatement, err := dto.Select()
	require.NoError(t, err)
	require.Len(t, dtoStatement.Projections(), 1)
	dtoResult, err := dto.Result()
	require.NoError(t, err)
	require.Equal(t, "email", dtoResult.Columns()[0].Name)

	baseStatement, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	dtoSQL, err := dto.Build(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, baseStatement.Args(), []any{7})
	require.Equal(t, dtoSQL.Args(), []any{7})
	require.Contains(t, baseStatement.SQL(), `"users"."id"`)
	require.NotContains(t, dtoSQL.SQL(), `"users"."id", "users"."email"`)
	invalid := rasql.RebindResult[reusableEmail](base,
		[]query.ResultColumn{{Name: "email", Type: schema.TextType{}}},
		users.Column("id"), users.Column("email"))
	_, err = invalid.Build(dialect.PostgreSQL())
	require.ErrorContains(t, err, "result columns count")
}

func TestRenderSelectBuilderQueryAndReplaceProject(t *testing.T) {
	table := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "email", Type: schema.TextType{}}}})
	builder := render.SelectFrom(dialect.PostgreSQL(), table).Select("id").WhereEqual("id", 4)
	statement, err := builder.Query()
	require.NoError(t, err)
	require.Len(t, statement.Projections(), 1)
	replaced := builder.ReplaceProject(table.Column("email"))
	replacedStatement, err := replaced.Query()
	require.NoError(t, err)
	require.Len(t, replacedStatement.Projections(), 1)
	require.Equal(t, "id", statement.Projections()[0].(query.ColumnRef).Name())
	require.Equal(t, "email", replacedStatement.Projections()[0].(query.ColumnRef).Name())
}
