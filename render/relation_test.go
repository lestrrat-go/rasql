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

func TestRenderCompoundOperatorsAndDerivedJoin(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	first, err := query.NewSelect(users, users.Column("id"), query.Project(query.Bind(7)))
	require.NoError(t, err)
	second, err := query.NewSelect(users, users.Column("id"), query.Project(query.Bind(8)))
	require.NoError(t, err)
	left, err := query.ResultOf(first,
		query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		query.ResultColumn{Name: "marker", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	right, err := query.ResultOf(second,
		query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		query.ResultColumn{Name: "marker", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	for _, testCase := range []struct {
		operator query.CompoundOperator
		text     string
	}{
		{query.Union, "UNION"}, {query.UnionAll, "UNION ALL"},
		{query.Intersect, "INTERSECT"}, {query.Except, "EXCEPT"},
	} {
		compound, err := query.CompoundQuery(left, testCase.operator, right)
		require.NoError(t, err)
		body, err := query.ResultOf(compound,
			query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
			query.ResultColumn{Name: "marker", Type: schema.IntegerType{}},
		)
		require.NoError(t, err)
		derived, err := query.Derived(body, "r")
		require.NoError(t, err)
		joined, err := query.NewJoinedSelect(users,
			[]query.Join{query.InnerJoin(derived, query.Equal(users.Column("id"), derived.Column("id")))},
			nil, users.Column("id"), derived.Column("marker"),
		)
		require.NoError(t, err)
		rendered, err := render.Select(dialect.SQLite(), joined)
		require.NoError(t, err)
		require.Contains(t, rendered.SQL(), testCase.text)
		require.Equal(t, []any{7, 8}, rendered.Args())
	}
}

func TestReusableRelationValidationBoundaries(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	base, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(base, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	_, err = query.NewInsertSelect(users, []query.ColumnRef{users.Column("id"), users.Column("id")}, result)
	require.ErrorContains(t, err, "duplicates column")
	_, err = query.ResultOf(base, query.ResultColumn{Name: "id", Type: (*schema.IntegerType)(nil)})
	require.ErrorContains(t, err, "typed nil")
	cte, err := query.CommonTable("local", result)
	require.NoError(t, err)
	ref, err := cte.Ref("")
	require.NoError(t, err)
	statement, err := query.NewSelect(ref, ref.Column("id"))
	require.NoError(t, err)
	_, err = render.Select(dialect.SQLite(), statement)
	require.ErrorContains(t, err, "not defined")
	other, err := query.CommonTable("LOCAL", result)
	require.NoError(t, err)
	statement, err = query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	statement, err = statement.WithCTEs(cte, other)
	require.NoError(t, err)
	_, err = render.Select(dialect.SQLite(), statement)
	require.ErrorContains(t, err, "collide")
}
