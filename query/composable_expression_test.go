package query_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestComposableExpressionsValidatePlacement(t *testing.T) {
	users, err := query.NewTableRef(schema.MustTableDef("users", schema.Integer("id"), schema.Integer("balance")))
	require.NoError(t, err)
	id, balance := users.Column("id"), users.Column("balance")

	filter, err := query.NewSelect(users, query.Project(query.FilterWhere(query.CountAll(), query.GreaterThan(balance, 0))))
	require.NoError(t, err)
	require.NoError(t, filter.Validate())

	window, err := query.NewSelect(users, query.Project(query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(id)))))
	require.NoError(t, err)
	require.NoError(t, window.Validate())

	_, err = query.NewSelect(users, query.Project(query.SearchedCase(query.When(query.Bind(true), 1))))
	require.ErrorContains(t, err, "must be a predicate expression")

	_, err = query.NewSelect(users, query.Project(query.SearchedCase()))
	require.ErrorContains(t, err, "requires at least one branch")

	_, err = query.NewSelect(users, query.Project(query.SearchedCase(query.When(nil, 1))))
	require.ErrorContains(t, err, "branches[0].predicate")

	_, err = query.NewSelect(users, query.Project(query.CastAs(balance, nil)))
	require.ErrorContains(t, err, "target")

	_, err = query.NewSelect(users, query.Project(query.FilterWhere(balance, query.Equal(balance, 1))))
	require.ErrorContains(t, err, "must contain an aggregate")

	_, err = query.NewSelect(users, query.Project(query.FilterWhere(query.CountAll(), query.GreaterThan(query.Count(balance), 1))))
	require.ErrorContains(t, err, "aggregate function")

	// Result aliases are rejected inside windows, not at statement level.
	projection := query.Project(query.Func("row_number"))
	windowWithAlias := query.OverWindow(query.Func("row_number"), query.Window(nil, query.AscResult(projection)))
	_, err = query.NewSelect(users, query.Project(windowWithAlias))
	require.ErrorContains(t, err, "result alias")

	_, err = query.NewSelect(users, query.Project(query.TrustedSQL("{} {}", query.Hole(1))))
	require.ErrorContains(t, err, "fragment markers")
	_, err = query.NewSelect(users, query.Project(query.TrustedSQL("{}", query.IdentifierHole(query.Ident("bad.name")))))
	require.ErrorContains(t, err, "invalid character")
	_, err = query.NewSelect(users, query.Project(query.TrustedSQL("{}", nil)))
	require.ErrorContains(t, err, "unsupported fragment part")
}

func TestComposableExpressionsCopyInputs(t *testing.T) {
	users, err := query.NewTableRef(schema.MustTableDef("users", schema.Integer("id"), schema.Integer("balance")))
	require.NoError(t, err)
	id := users.Column("id")
	partition := []query.Expression{id}
	orders := []query.Order{query.Asc(id)}
	window := query.Window(partition, orders...)
	partition[0] = query.Bind(99)
	orders[0] = query.Desc(id)
	require.Equal(t, id, window.Partition()[0])
	require.Equal(t, query.Asc(id), window.Order()[0])

}
