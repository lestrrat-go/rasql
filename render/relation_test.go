package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestRenderReusableRelationsAndInsertSelect(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{
		Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.TextType{}}},
	})
	base, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(base, query.ResultColumn{Name: "user_id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	derived, err := query.Derived(result, "u")
	require.NoError(t, err)
	outer, err := query.NewSelect(derived, derived.Column("user_id"))
	require.NoError(t, err)
	rendered, err := render.Select(dialect.SQLite(), outer)
	require.NoError(t, err)
	require.Equal(t, `SELECT "u"."user_id" FROM (SELECT "users"."id" FROM "users") AS "u"`, rendered.SQL())

	compound, err := query.CompoundQuery(result, query.UnionAll, result)
	require.NoError(t, err)
	compoundResult, err := query.ResultOf(compound, query.ResultColumn{Name: "user_id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	compoundRelation, err := query.Derived(compoundResult, "all_users")
	require.NoError(t, err)
	compoundSelect, err := query.NewSelect(compoundRelation, compoundRelation.Column("user_id"))
	require.NoError(t, err)
	rendered, err = render.Select(dialect.SQLite(), compoundSelect)
	require.NoError(t, err)
	require.Equal(t, `SELECT "all_users"."user_id" FROM ((SELECT "users"."id" FROM "users") UNION ALL (SELECT "users"."id" FROM "users")) AS "all_users"`, rendered.SQL())

	insert, err := query.NewInsertSelect(users, []query.ColumnRef{users.Column("id")}, result)
	require.NoError(t, err)
	rendered, err = render.Insert(dialect.SQLite(), insert)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" ("id") SELECT "users"."id" FROM "users"`, rendered.SQL())
}

func TestRenderCTE(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	base, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(base, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	cte, err := query.CommonTable("filtered", result)
	require.NoError(t, err)
	ref, err := cte.Ref("")
	require.NoError(t, err)
	statement, err := query.NewSelect(ref, ref.Column("id"))
	require.NoError(t, err)
	statement, err = statement.WithCTEs(cte)
	require.NoError(t, err)
	rendered, err := render.Select(dialect.SQLite(), statement)
	require.NoError(t, err)
	require.Equal(t, `WITH "filtered" AS (SELECT "users"."id" FROM "users") SELECT "filtered"."id" FROM "filtered"`, rendered.SQL())
}
