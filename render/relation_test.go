package render_test

import (
	"fmt"
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
	require.Equal(t, `SELECT "all_users"."user_id" FROM (SELECT "users"."id" FROM "users" UNION ALL SELECT "users"."id" FROM "users") AS "all_users"`, rendered.SQL())

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
		rendered, err := render.Select(dialect.PostgreSQL(), joined)
		require.NoError(t, err)
		expected := fmt.Sprintf(`SELECT "users"."id", "r"."marker" FROM "users" INNER JOIN ((SELECT "users"."id", $1 FROM "users") %s (SELECT "users"."id", $2 FROM "users")) AS "r" ON ("users"."id" = "r"."id")`, testCase.text)
		require.Equal(t, expected, rendered.SQL())
		require.Equal(t, []any{7, 8}, rendered.Args())
	}
}

func TestRenderInsertSelectSourceArgumentsPrecedeReturning(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	source, err := query.NewSelect(users, users.Column("id"), query.Project(query.Bind(3)))
	require.NoError(t, err)
	source, err = source.WithWhere(query.Equal(users.Column("id"), query.Bind(4)))
	require.NoError(t, err)
	result, err := query.ResultOf(source,
		query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		query.ResultColumn{Name: "marker", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	_, err = query.NewInsertSelect(users, []query.ColumnRef{users.Column("id"), users.Column("id")}, result)
	require.Error(t, err)
	// Use distinct target columns so the source and RETURNING argument order is tested.
	usersWithMarker := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "marker", Type: schema.IntegerType{}}}})
	insert, err := query.NewInsertSelect(usersWithMarker, []query.ColumnRef{usersWithMarker.Column("id"), usersWithMarker.Column("marker")}, result)
	require.NoError(t, err)
	insert, err = insert.WithReturning(query.Project(query.Bind(9)))
	require.NoError(t, err)
	rendered, err := render.Insert(dialect.PostgreSQL(), insert)
	require.NoError(t, err)
	require.Equal(t, `INSERT INTO "users" ("id", "marker") SELECT "users"."id", $1 FROM "users" WHERE ("users"."id" = $2) RETURNING $3`, rendered.SQL())
	require.Equal(t, []any{3, 4, 9}, rendered.Args())
}

func TestRenderReusableRelationPagingAndCount(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	base, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(base, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	derived, err := query.Derived(result, "r")
	require.NoError(t, err)
	paged, err := query.NewSelect(derived, derived.Column("id"))
	require.NoError(t, err)
	paged, err = paged.WithLimit(5)
	require.NoError(t, err)
	paged, err = paged.WithOffset(2)
	require.NoError(t, err)
	rendered, err := render.Select(dialect.PostgreSQL(), paged)
	require.NoError(t, err)
	require.Equal(t, `SELECT "r"."id" FROM (SELECT "users"."id" FROM "users") AS "r" LIMIT $1 OFFSET $2`, rendered.SQL())
	require.Equal(t, []any{5, 2}, rendered.Args())
	counted, err := render.SelectFromRelation(dialect.PostgreSQL(), derived).Select("id").BuildCount()
	require.NoError(t, err)
	require.Equal(t, `SELECT COUNT(*) AS "count" FROM (SELECT "users"."id" FROM "users") AS "r"`, counted.SQL())
}

func TestReusableResultMetadataAccessorIsolation(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	base, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(base, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	columns := result.Columns()
	columns[0].Name = "changed"
	require.Equal(t, "id", result.Columns()[0].Name)

	derived, err := query.Derived(result, "r")
	require.NoError(t, err)
	derivedColumns := derived.Columns()
	derivedColumns[0].Name = "changed"
	require.Equal(t, "id", derived.Columns()[0].Name)

	cte, err := query.CommonTable("local", result)
	require.NoError(t, err)
	cteColumns := cte.Query().Columns()
	cteColumns[0].Name = "changed"
	require.Equal(t, "id", cte.Query().Columns()[0].Name)
	ref, err := cte.Ref("")
	require.NoError(t, err)
	refColumns := ref.Columns()
	refColumns[0].Name = "changed"
	require.Equal(t, "id", ref.Columns()[0].Name)

	compound, err := query.CompoundQuery(result, query.UnionAll, result)
	require.NoError(t, err)
	leftColumns := compound.Left().Columns()
	leftColumns[0].Name = "changed"
	require.Equal(t, "id", compound.Left().Columns()[0].Name)

	insert, err := query.NewInsertSelect(users, []query.ColumnRef{users.Column("id")}, result)
	require.NoError(t, err)
	insertResult, ok := insert.SelectSource()
	require.True(t, ok)
	insertColumns := insertResult.Columns()
	insertColumns[0].Name = "changed"
	storedResult, ok := insert.SelectSource()
	require.True(t, ok)
	require.Equal(t, "id", storedResult.Columns()[0].Name)
}

func TestReusableResultPointerTypeIsolation(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	base, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	typeMetadata := &schema.IntegerType{Unsigned: true}
	result, err := query.ResultOf(base, query.ResultColumn{Name: "id", Type: typeMetadata})
	require.NoError(t, err)
	typeMetadata.Unsigned = false
	assertUnsigned := func(columns []query.ResultColumn) {
		t.Helper()
		stored, ok := columns[0].Type.(*schema.IntegerType)
		require.True(t, ok)
		require.True(t, stored.Unsigned)
	}
	assertUnsigned(result.Columns())
	derived, err := query.Derived(result, "r")
	require.NoError(t, err)
	derivedType := derived.Columns()[0].Type.(*schema.IntegerType)
	derivedType.Unsigned = false
	assertUnsigned(derived.Columns())
	cte, err := query.CommonTable("local", result)
	require.NoError(t, err)
	cteType := cte.Query().Columns()[0].Type.(*schema.IntegerType)
	cteType.Unsigned = false
	assertUnsigned(cte.Query().Columns())
	ref, err := cte.Ref("")
	require.NoError(t, err)
	refType := ref.Columns()[0].Type.(*schema.IntegerType)
	refType.Unsigned = false
	assertUnsigned(ref.Columns())
	compound, err := query.CompoundQuery(result, query.UnionAll, result)
	require.NoError(t, err)
	rightType := compound.Right().Columns()[0].Type.(*schema.IntegerType)
	rightType.Unsigned = false
	assertUnsigned(compound.Right().Columns())
	leftType := compound.Left().Columns()[0].Type.(*schema.IntegerType)
	leftType.Unsigned = false
	assertUnsigned(compound.Left().Columns())
	insert, err := query.NewInsertSelect(users, []query.ColumnRef{users.Column("id")}, result)
	require.NoError(t, err)
	insertType := mustSelectSource(t, insert).Columns()[0].Type.(*schema.IntegerType)
	insertType.Unsigned = false
	assertUnsigned(mustSelectSource(t, insert).Columns())
}

func mustSelectSource(t *testing.T, insert query.Insert) query.ResultQuery {
	t.Helper()
	result, ok := insert.SelectSource()
	require.True(t, ok)
	return result
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
	_, err = query.ResultOf(base)
	require.ErrorContains(t, err, "count")
	_, err = query.ResultOf(base, query.ResultColumn{Name: "", Type: schema.IntegerType{}})
	require.Error(t, err)
	_, err = query.ResultOf(base, query.ResultColumn{Name: "bad-name", Type: schema.IntegerType{}})
	require.Error(t, err)
	_, err = query.ResultOf(base,
		query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
	)
	require.ErrorContains(t, err, "duplicate")
	_, err = query.ResultOf(base, query.ResultColumn{Name: "id", Type: schema.DecimalType{Precision: 0, Scale: schema.NewDecimalScale(0)}})
	require.Error(t, err)
	_, err = query.ResultOf(base, query.ResultColumn{Name: "id", Type: schema.TextType{Fixed: true}})
	require.Error(t, err)
	_, err = query.Derived(result, "")
	require.Error(t, err)
	_, err = query.Derived(result, "bad-name")
	require.Error(t, err)
	derived, err := query.Derived(result, "derived")
	require.NoError(t, err)
	_, err = query.NewSelect(derived, derived.Column("missing"))
	require.Error(t, err)
	var nilBody *query.Select
	_, err = query.ResultOf(nilBody, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.Error(t, err)
	otherBase, err := query.NewSelect(users, users.Column("id"), query.Project(query.Bind(1)))
	require.NoError(t, err)
	otherResult, err := query.ResultOf(otherBase,
		query.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		query.ResultColumn{Name: "one", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	_, err = query.CompoundQuery(result, query.UnionAll, otherResult)
	require.Error(t, err)
	_, err = query.CompoundQuery(result, query.CompoundOperator(99), result)
	require.Error(t, err)
}

func TestRenderCTEVisibleToNestedBody(t *testing.T) {
	users := query.MustTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	base, err := query.NewSelect(users, users.Column("id"))
	require.NoError(t, err)
	result, err := query.ResultOf(base, query.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	cte, err := query.CommonTable("local", result)
	require.NoError(t, err)
	ref, err := cte.Ref("")
	require.NoError(t, err)
	inner, err := query.NewSelect(ref, ref.Column("id"))
	require.NoError(t, err)
	outer, err := query.NewSelect(users, query.Project(query.Scalar(inner)))
	require.NoError(t, err)
	outer, err = outer.WithCTEs(cte)
	require.NoError(t, err)
	rendered, err := render.Select(dialect.SQLite(), outer)
	require.NoError(t, err)
	require.Contains(t, rendered.SQL(), `WITH "local" AS`)
	require.Contains(t, rendered.SQL(), `FROM "local"`)
}
