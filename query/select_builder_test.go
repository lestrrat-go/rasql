package query_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/stretchr/testify/require"
)

func TestSelectBuilderBuildsStatement(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	statement, err := query.SelectFrom(users).
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
	expected, err := query.NewSelect(users, query.Project(id), query.Project(email))
	require.NoError(t, err)
	expected, err = expected.WithWhere(query.Equal(id, query.Bind(42)))
	require.NoError(t, err)
	expected, err = expected.WithOrder(query.Desc(email))
	require.NoError(t, err)
	expected, err = expected.WithLimit(20)
	require.NoError(t, err)
	expected, err = expected.WithOffset(10)
	require.NoError(t, err)

	require.Equal(t, expected, statement)
}

func TestSelectBuilderIsImmutable(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	base := query.SelectFrom(users).Select("id")
	filtered := base.WhereEqual("id", 42)

	baseStatement, err := base.Build()
	require.NoError(t, err)
	require.Nil(t, baseStatement.Where())
	filteredStatement, err := filtered.Build()
	require.NoError(t, err)
	require.NotNil(t, filteredStatement.Where())
}

func TestSelectBuilderReportsErrorsFromBuild(t *testing.T) {
	users, err := query.NewTableRef(usersTable())
	require.NoError(t, err)

	_, err = query.SelectFrom(users).Select("missing").Build()
	require.Error(t, err)
	_, err = query.SelectFrom(users).Select("id").Limit(-1).Build()
	requireQueryValidationError(t, err)
	_, err = query.SelectFrom(users).Select("id").Where(nil).Build()
	requireQueryValidationError(t, err)
}
