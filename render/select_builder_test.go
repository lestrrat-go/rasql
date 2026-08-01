package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestSelectBuilderBuildsRenderedStatement(t *testing.T) {
	users := fluentUsers(t)

	rendered, err := render.SelectFrom(dialect.PostgreSQL(), users).
		Select("id", "email").
		WhereEqual("id", 42).
		OrderDesc("email").
		Limit(20).
		Offset(10).
		Build()
	require.NoError(t, err)

	id, err := users.Column("id")
	require.NoError(t, err)
	email, err := users.Column("email")
	require.NoError(t, err)
	statement, err := query.NewSelect(users, query.Project(id), query.Project(email))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(id, query.Bind(42)))
	require.NoError(t, err)
	statement, err = statement.WithOrder(query.Desc(email))
	require.NoError(t, err)
	statement, err = statement.WithLimit(20)
	require.NoError(t, err)
	statement, err = statement.WithOffset(10)
	require.NoError(t, err)
	expected, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)

	require.Equal(t, expected.SQL(), rendered.SQL())
	require.Equal(t, expected.Args(), rendered.Args())
}

func TestSelectBuilderIsImmutable(t *testing.T) {
	users := fluentUsers(t)

	base := render.SelectFrom(dialect.PostgreSQL(), users).Select("id")
	filtered := base.WhereEqual("id", 42)

	baseStatement, err := base.Build()
	require.NoError(t, err)
	require.NotContains(t, baseStatement.SQL(), " WHERE ")
	filteredStatement, err := filtered.Build()
	require.NoError(t, err)
	require.Contains(t, filteredStatement.SQL(), " WHERE ")
}

func TestSelectBuilderReportsBuildErrors(t *testing.T) {
	users := fluentUsers(t)

	_, err := render.SelectFrom(dialect.PostgreSQL(), users).Select("missing").Build()
	require.Error(t, err)
	_, err = render.SelectFrom(dialect.PostgreSQL(), users).Select("id").Limit(-1).Build()
	require.Error(t, err)
	_, err = render.SelectFrom(dialect.PostgreSQL(), users).Select("id").Where(nil).Build()
	require.Error(t, err)
	_, err = render.SelectFrom(nil, users).Select("id").Build()
	require.Error(t, err)
}

func fluentUsers(t *testing.T) query.TableRef {
	t.Helper()
	users, err := query.NewTableRef(schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	return users
}
