package render_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/stretchr/testify/require"
)

func TestComposableArithmeticRendersInUpdate(t *testing.T) {
	table := composableTable(t)
	balance := table.Column("balance")
	statement, err := query.NewUpdate(table, query.Set(balance, query.Add(balance, 1)))
	require.NoError(t, err)
	statement, err = statement.WithWhere(query.Equal(table.Column("id"), 7))
	require.NoError(t, err)
	for _, test := range []struct {
		name    string
		dialect dialect.Dialect
		sql     string
	}{
		{"postgresql", dialect.PostgreSQL(), `UPDATE "accounts" SET "balance" = ("accounts"."balance" + $1) WHERE ("accounts"."id" = $2)`},
		{"mysql", dialect.MySQL(), "UPDATE `accounts` SET `balance` = (`accounts`.`balance` + ?) WHERE (`accounts`.`id` = ?)"},
		{"sqlite", dialect.SQLite(), `UPDATE "accounts" SET "balance" = ("accounts"."balance" + ?) WHERE ("accounts"."id" = ?)`},
	} {
		t.Run(test.name, func(t *testing.T) {
			rendered, err := render.Update(test.dialect, statement)
			require.NoError(t, err)
			require.Equal(t, test.sql, rendered.SQL())
			require.Equal(t, []any{1, 7}, rendered.Args())
		})
	}
}

func TestComposableArithmeticOperatorsRender(t *testing.T) {
	table := composableTable(t)
	balance := table.Column("balance")
	tests := []struct {
		name     string
		operator query.BinaryOperator
		symbol   string
	}{
		{"add", query.OperatorAdd, "+"},
		{"subtract", query.OperatorSubtract, "-"},
		{"multiply", query.OperatorMultiply, "*"},
		{"divide", query.OperatorDivide, "/"},
		{"modulo", query.OperatorModulo, "%"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			statement, err := query.NewUpdate(table, query.Set(balance, query.Compare(balance, test.operator, 2)))
			require.NoError(t, err)
			statement, err = statement.WithWhere(query.Equal(table.Column("id"), 1))
			require.NoError(t, err)
			rendered, err := render.Update(dialect.PostgreSQL(), statement)
			require.NoError(t, err)
			require.Contains(t, rendered.SQL(), `"balance"`+" "+test.symbol+" ")
		})
	}
}
