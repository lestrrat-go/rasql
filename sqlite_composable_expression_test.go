package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteComposableExpressionsInTransaction(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	_, err = database.ExecContext(t.Context(), `CREATE TABLE accounts (id INTEGER PRIMARY KEY, balance INTEGER NOT NULL)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO accounts (id, balance) VALUES (1, 10), (2, 20)`)
	require.NoError(t, err)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	tx, err := db.Begin(t.Context(), nil)
	require.NoError(t, err)
	defer func() { _ = tx.Rollback() }()

	table := query.MustTableRef(schema.MustTableDef("accounts", schema.Integer("id"), schema.Integer("balance")))
	id, balance := table.Column("id"), table.Column("balance")
	update, err := query.NewUpdate(table, query.Set(balance, query.Add(balance, 1)))
	require.NoError(t, err)
	update, err = update.WithWhere(query.Equal(id, 1))
	require.NoError(t, err)
	require.NoError(t, executeUpdate(t, tx, update))

	label := query.SearchedCase(query.When(query.GreaterThan(balance, 10), "large")).Else("small")
	fragment := query.TrustedSQL("{} + {} + {}", query.IdentifierHole(query.Ident("balance")), query.Hole(4), query.Hole(5))
	selectStatement, err := query.NewSelect(table,
		id,
		query.Project(label).As("label"),
		query.Project(query.CastAs(balance, schema.IntegerType{})).As("cast_balance"),
		query.Project(fragment).As("fragment_total"),
	)
	require.NoError(t, err)
	rendered, err := render.Select(dialect.SQLite(), selectStatement)
	require.NoError(t, err)
	require.Equal(t, []any{10, "large", "small", 4, 5}, rendered.Args())
	rows, err := tx.QueryRendered(t.Context(), rendered)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	type result struct {
		id            int64
		label         string
		castBalance   int64
		fragmentTotal int64
	}
	var results []result
	for rows.Next() {
		var current result
		require.NoError(t, rows.Scan(&current.id, &current.label, &current.castBalance, &current.fragmentTotal))
		results = append(results, current)
	}
	require.NoError(t, rows.Err())
	require.Equal(t, []result{{1, "large", 11, 20}, {2, "large", 20, 29}}, results)

	windowStatement, err := query.NewSelect(table,
		query.Project(query.OverWindow(query.Func("row_number"), query.Window(nil, query.Asc(id)))).As("row_number"))
	require.NoError(t, err)
	windowRendered, err := render.Select(dialect.SQLite(), windowStatement)
	require.NoError(t, err)
	windowRows, err := tx.QueryRendered(t.Context(), windowRendered)
	require.NoError(t, err)
	defer func() { _ = windowRows.Close() }()
	var windowResults []int64
	for windowRows.Next() {
		var rowNumber int64
		require.NoError(t, windowRows.Scan(&rowNumber))
		windowResults = append(windowResults, rowNumber)
	}
	require.NoError(t, windowRows.Err())
	require.Equal(t, []int64{1, 2}, windowResults)
	require.NoError(t, tx.Commit())

	var stored int64
	require.NoError(t, database.QueryRowContext(t.Context(), `SELECT balance FROM accounts WHERE id = 1`).Scan(&stored))
	require.Equal(t, int64(11), stored)
}

func executeUpdate(t *testing.T, db rasql.DB, statement query.Update) error {
	t.Helper()
	return rasqlExec(t, db, statement)
}

func rasqlExec(t *testing.T, db rasql.DB, statement query.WriteStatement) error {
	t.Helper()
	_, err := rasql.Exec(t.Context(), db, statement)
	return err
}
