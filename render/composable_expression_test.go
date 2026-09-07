package render_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func composableTable(t *testing.T) query.TableRef {
	t.Helper()
	return query.MustTableRef(schema.MustTableDef("accounts", schema.Integer("id"), schema.Integer("balance"), schema.Text("email")))
}

func TestComposableExpressionsRenderAllDialects(t *testing.T) {
	table := composableTable(t)
	balance, id := table.Column("balance"), table.Column("id")
	caseExpr := query.SearchedCase(query.When(query.GreaterThan(balance, 10), "high")).Else("low")
	expr := query.Project(query.Add(balance, 1)).As("sum")
	caseProjection := query.Project(caseExpr).As("class")
	fragment := query.Project(query.TrustedSQL("{} + {}", query.IdentifierHole(query.Ident("balance")), query.Hole(2))).As("total")
	statement, err := query.NewSelect(table, expr, caseProjection, fragment)
	require.NoError(t, err)
	windowStatement, err := query.NewSelect(table, query.Project(query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(id)))).As("row"))
	require.NoError(t, err)

	tests := []struct {
		name    string
		dialect dialect.Dialect
		sql     string
		args    []any
	}{
		{"postgresql", dialect.PostgreSQL(), `SELECT ("accounts"."balance" + $1) AS "sum", (CASE WHEN ("accounts"."balance" > $2) THEN $3 ELSE $4 END) AS "class", "balance" + $5 AS "total" FROM "accounts"`, []any{1, 10, "high", "low", 2}},
		{"mysql", dialect.MySQL(), "SELECT (`accounts`.`balance` + ?) AS `sum`, (CASE WHEN (`accounts`.`balance` > ?) THEN ? ELSE ? END) AS `class`, `balance` + ? AS `total` FROM `accounts`", []any{1, 10, "high", "low", 2}},
		{"sqlite", dialect.SQLite(), `SELECT ("accounts"."balance" + ?) AS "sum", (CASE WHEN ("accounts"."balance" > ?) THEN ? ELSE ? END) AS "class", "balance" + ? AS "total" FROM "accounts"`, []any{1, 10, "high", "low", 2}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			rendered, err := render.Select(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, test.args, rendered.Args())
		})
	}
	window, err := render.Select(dialect.PostgreSQL(), windowStatement)
	require.NoError(t, err)
	require.Equal(t, `SELECT row_number() OVER (ORDER BY "accounts"."id") AS "row" FROM "accounts"`, window.SQL())
}

func TestFilterCapabilityAndNestedArguments(t *testing.T) {
	table := composableTable(t)
	id, balance := table.Column("id"), table.Column("balance")
	filtered := query.Project(query.FilterWhere(query.CountAll(), query.GreaterThan(balance, 0))).As("count")
	statement, err := query.NewSelect(table, filtered)
	require.NoError(t, err)
	postgres, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t, `SELECT COUNT(*) FILTER (WHERE ("accounts"."balance" > $1)) AS "count" FROM "accounts"`, postgres.SQL())
	require.Equal(t, []any{0}, postgres.Args())
	_, err = render.Select(dialect.MySQL(), statement)
	var unsupported *render.UnsupportedAggregateFilterError
	require.ErrorAs(t, err, &unsupported)
	require.True(t, errors.Is(err, render.ErrUnsupportedAggregateFilter))

	nested := query.Project(query.SearchedCase(
		query.When(query.GreaterThan(id, 0), query.TrustedSQL("{}", query.Hole(2))),
	).Else(query.TrustedSQL("{}", query.Hole(3)))).As("nested")
	nestedStatement, err := query.NewSelect(table, nested)
	require.NoError(t, err)
	for _, d := range []dialect.Dialect{dialect.PostgreSQL(), dialect.SQLite()} {
		rendered, err := render.Select(d, nestedStatement)
		require.NoError(t, err)
		require.Equal(t, []any{0, 2, 3}, rendered.Args())
	}
}

func TestSimpleCaseAndCastRender(t *testing.T) {
	table := composableTable(t)
	balance := table.Column("balance")
	simple := query.Project(query.SimpleCase(balance, query.When(query.Bind(10), "ten"))).As("label")
	omitted := query.Project(query.SearchedCase(query.When(query.GreaterThan(balance, 0), 1))).As("flag")
	casted := query.Project(query.CastAs(balance, schema.TextType{})).As("text_balance")
	statement, err := query.NewSelect(table, simple, omitted, casted)
	require.NoError(t, err)
	rendered, err := render.Select(dialect.PostgreSQL(), statement)
	require.NoError(t, err)
	require.Equal(t, `SELECT (CASE "accounts"."balance" WHEN $1 THEN $2 END) AS "label", (CASE WHEN ("accounts"."balance" > $3) THEN $4 END) AS "flag", CAST("accounts"."balance" AS TEXT) AS "text_balance" FROM "accounts"`, rendered.SQL())
	require.Equal(t, []any{10, "ten", 0, 1}, rendered.Args())
}
