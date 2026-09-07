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
	empty := rasql.RebindResult[reusableEmail](base, nil, users.Column("email"))
	_, err = empty.Build(dialect.PostgreSQL())
	require.ErrorContains(t, err, "result columns count")

	page := base.OrderAsc(users.Column("id")).Limit(2).Offset(1)
	pageStatement, err := page.Select()
	require.NoError(t, err)
	pageSQL, err := render.Select(dialect.PostgreSQL(), pageStatement)
	require.NoError(t, err)
	require.Equal(t, []any{7, 2, 1}, pageSQL.Args())
	require.Contains(t, pageSQL.SQL(), `ORDER BY`)
	renderBuilder := render.SelectFrom(dialect.PostgreSQL(), users.Ref()).Select("id").
		WhereEqual("id", 7).OrderAsc("id").Limit(2).Offset(1)
	countSelect, err := renderBuilder.QueryForCount(true)
	require.NoError(t, err)
	countSQL, err := render.Select(dialect.PostgreSQL(), countSelect)
	require.NoError(t, err)
	require.Contains(t, countSQL.SQL(), `ORDER BY "users"."id"`)
	require.Equal(t, []any{7, 2, 1}, countSQL.Args())

	type pointerMetadata struct{ ID int64 }
	columnType := &schema.IntegerType{}
	rebound := rasql.RebindResult[pointerMetadata](base,
		[]query.ResultColumn{{Name: "id", Type: columnType}}, users.Column("id"))
	columnType.Unsigned = true
	reboundResult, err := rebound.Result()
	require.NoError(t, err)
	clonedType, ok := reboundResult.Columns()[0].Type.(*schema.IntegerType)
	require.True(t, ok)
	require.False(t, clonedType.Unsigned)
	clonedType.Unsigned = true
	originalResult, err := rebound.Result()
	require.NoError(t, err)
	require.False(t, originalResult.Columns()[0].Type.(*schema.IntegerType).Unsigned)

	idsBuilder := rasql.DecodeFromRef[reusableUser](users.Ref()).WhereEqual(users.Column("id"), 7).Project(users.Column("id"))
	ids, err := idsBuilder.Select()
	require.NoError(t, err)
	outer := rasql.SelectFrom(users).Where(query.InSelect(users.Column("id"), ids))
	outerSQL, err := outer.Build(dialect.PostgreSQL())
	require.NoError(t, err)
	require.Contains(t, outerSQL.SQL(), `IN (SELECT`)
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
