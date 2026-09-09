package rasql_test

import (
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type reusableUser struct {
	ID    int64
	Email string
}

type reusableEmail struct{ Email string }

type reusableUserDecoder struct{ schema rasql.ResultSchema }

func (d reusableUserDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (reusableUserDecoder) Presence() []rasql.Presence         { return nil }
func (reusableUserDecoder) DecodeRow(source rasql.ScanSource, result *reusableUser) error {
	return source.Scan(&result.ID, &result.Email)
}

type reusableEmailDecoder struct{ schema rasql.ResultSchema }

func (d reusableEmailDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (reusableEmailDecoder) Presence() []rasql.Presence         { return nil }
func (reusableEmailDecoder) DecodeRow(source rasql.ScanSource, result *reusableEmail) error {
	return source.Scan(&result.Email)
}

func reusableUsers(t *testing.T) (rasql.TypedRelation[reusableUser], rasql.Column[reusableUser, int64], rasql.Column[reusableUser, string]) {
	t.Helper()
	table, err := rasql.ReadTableOf[reusableUser](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "users")
	require.NoError(t, err)
	id, err := rasql.BindColumn[reusableUser, int64](relation, "id", "")
	require.NoError(t, err)
	email, err := rasql.BindColumn[reusableUser, string](relation, "email", "")
	require.NoError(t, err)
	return relation, id, email
}

func reusableUserQuery(t *testing.T) rasql.Query[reusableUser] {
	t.Helper()
	relation, id, email := reusableUsers(t)
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "email", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("email", email.Expr(), schema.TextType{}, ""),
	}, reusableUserDecoder{schema: resultSchema})
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection).Where(rasql.EqualExpr(id.Expr(), rasql.Value(int64(7))))
}

func TestTypedReusableQueryValues(t *testing.T) {
	base := reusableUserQuery(t)
	require.NoError(t, base.Validate())
	require.Len(t, base.Schema().Columns(), 2)

	page, err := base.Limit(2)
	require.NoError(t, err)
	page, err = page.Offset(1)
	require.NoError(t, err)
	require.NoError(t, page.Validate())

	derived, err := rasql.Derive(page, "users_page")
	require.NoError(t, err)
	email, err := rasql.BindResultColumn[reusableUser, string](derived, "email")
	require.NoError(t, err)
	emailSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "email", Type: schema.TextType{}})
	require.NoError(t, err)
	emailProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("email", email.Expr(), schema.TextType{}, ""),
	}, reusableEmailDecoder{schema: emailSchema})
	require.NoError(t, err)
	emailQuery := rasql.Select(derived.Source(), emailProjection)
	require.NoError(t, emailQuery.Validate())

	id, err := rasql.BindResultColumn[reusableUser, int64](derived, "id")
	require.NoError(t, err)
	count := rasql.CountQuery(page, true)
	require.NoError(t, count.Validate())
	countByID := rasql.Select(derived.Source(), emailProjection).Where(rasql.EqualExpr(id.Expr(), rasql.Value(int64(7))))
	require.NoError(t, countByID.Validate())

	_, err = rasql.BindResultColumn[reusableUser, string](derived, "missing")
	require.ErrorContains(t, err, "not a member")
	_, err = rasql.SourceOf[reusableUser](nil, "users")
	require.Error(t, err)
}

func TestRenderSelectBuilderQueryAndReplaceProject(t *testing.T) {
	base := reusableUserQuery(t)
	derived, err := rasql.Derive(base, "users_derived")
	require.NoError(t, err)
	projected, err := rasql.BindResultColumn[reusableUser, string](derived, "email")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "email", Type: schema.TextType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("email", projected.Expr(), schema.TextType{}, ""),
	}, reusableEmailDecoder{schema: resultSchema})
	require.NoError(t, err)
	require.NoError(t, rasql.Select(derived.Source(), projection).Validate())
}
