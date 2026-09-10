package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// This is the acceptance case for the typed expression language. It is one
// realistic query that a reader would expect any ORM to express, and it uses
// only the canonical typed API: no query.Expression escape hatch, no raw SQL.
//
// It exercises every operator group the typed API was missing: an ordered
// comparison, a string function, a pattern match, the SUM and AVG aggregates,
// an IN subquery, a correlated EXISTS, and an ordering that names a projected
// result by its alias. A query the typed API cannot express is a capability
// the redesign has not replaced, so this test is the standing definition of
// "the expression language is complete enough to ship".

type acceptanceOrderRow struct {
	ID       int64
	Customer string
	Amount   int64
}

type acceptanceRefundRow struct {
	ID      int64
	OrderID int64
}

// acceptanceTotalRow is one customer with their summed and averaged order
// amounts. SUM and AVG are both NULL over an empty group, so each is Nullable
// even though amount is not. Average is float64 rather than int64 because the
// average of integers is not an integer, and because both servers compute it
// as an exact decimal they then deliver as text.
type acceptanceTotalRow struct {
	Customer string
	Total    rasql.Nullable[int64]
	Average  rasql.Nullable[float64]
}

type acceptanceTotalDecoder struct{ result rasql.ResultSchema }

func (d acceptanceTotalDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d acceptanceTotalDecoder) Presence() []rasql.Presence       { return nil }
func (d acceptanceTotalDecoder) DecodeRow(src rasql.ScanSource, row *acceptanceTotalRow) error {
	return src.Scan(&row.Customer, &row.Total, &row.Average)
}

// acceptanceQuery builds:
//
//	SELECT o.customer, SUM(o.amount), AVG(o.amount)
//	FROM orders o
//	WHERE o.amount > 100
//	  AND LOWER(o.customer) LIKE 'a%'
//	  AND o.id IN (SELECT r.order_id FROM refunds r)
//	  AND EXISTS (SELECT r.order_id FROM refunds r WHERE r.order_id = o.id)
//	GROUP BY o.customer
//	ORDER BY total DESC, LOWER(o.customer)
//
// The first ordering term names the SUM result by its alias, which is the
// one position SQL allows a result name in, and the second orders by a
// recomputed expression. Having both proves the two kinds of ordering term
// compose in one statement.
func acceptanceQuery(t *testing.T) rasql.Query[acceptanceTotalRow] {
	t.Helper()

	orders, err := rasql.ReadTableOf[acceptanceOrderRow](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "customer", Type: schema.TextType{}},
		{Name: "amount", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)
	refunds, err := rasql.ReadTableOf[acceptanceRefundRow](schema.TableDef{Name: "refunds", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "order_id", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)

	o, err := rasql.SourceOf(orders, "o")
	require.NoError(t, err)
	r, err := rasql.SourceOf(refunds, "r")
	require.NoError(t, err)

	orderID, err := rasql.BindColumn[acceptanceOrderRow, int64](o, "id", "")
	require.NoError(t, err)
	customer, err := rasql.BindColumn[acceptanceOrderRow, string](o, "customer", "")
	require.NoError(t, err)
	amount, err := rasql.BindColumn[acceptanceOrderRow, int64](o, "amount", "")
	require.NoError(t, err)
	refundOrder, err := rasql.BindColumn[acceptanceRefundRow, int64](r, "order_id", "")
	require.NoError(t, err)

	// The subquery projects exactly one int64 column, so InQuery can check
	// its element type against the left-hand side at compile time.
	refunded, err := rasql.Scalar("order_id", refundOrder.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	refundQuery := rasql.Select(r.Source(), refunded)

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "customer", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true},
		rasql.ResultColumn{Name: "average", Type: schema.FloatType{}, Nullable: true},
	)
	require.NoError(t, err)
	// total is held in a variable so the ordering below can name this exact
	// projection rather than repeat its alias as a second string.
	total := rasql.NullItem("total", rasql.SumExpr(amount.Expr()), schema.IntegerType{}, "")
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("customer", customer.Expr(), schema.TextType{}, ""),
		total,
		rasql.NullItem("average", rasql.AvgExpr(amount.Expr()), schema.FloatType{}, ""),
	}, acceptanceTotalDecoder{result: result})
	require.NoError(t, err)

	// InQuery reports its error rather than deferring it, like every other
	// structural composition in this package.
	refundedOrder, err := rasql.InQuery(orderID.Expr(), refundQuery)
	require.NoError(t, err)

	// The same restriction written a second way, as a correlated EXISTS. Its
	// WHERE reads o.id, a column of the enclosing query, which is what makes
	// it correlated. It is redundant with refundedOrder on purpose: an EXISTS
	// that changed which rows survive would not prove the two forms agree.
	correlated, err := rasql.ExistsQuery(
		rasql.Select(r.Source(), refunded).
			Correlated(o.Source()).
			Where(rasql.EqualExpr(refundOrder.Expr(), orderID.Expr())),
	)
	require.NoError(t, err)

	return rasql.Select(o.Source(), projection).
		Where(rasql.And(
			rasql.GreaterValue(amount.Expr(), int64(100)),
			rasql.LikeValue(rasql.LowerExpr(customer.Expr()), "a%"),
			refundedOrder,
			correlated,
		)).
		GroupBy(rasql.Group(customer.Expr())).
		OrderBy(rasql.DescResult(total), rasql.AscExpr(rasql.LowerExpr(customer.Expr())))
}

// acceptanceSeed fills both tables. Each row is chosen so that exactly one
// operator decides its fate, which is what makes a wrong operator visible:
//
//	1 alice 150 refunded  kept
//	2 alice 200 refunded  kept
//	3 bob   300 refunded  dropped by LIKE
//	4 amy    50 refunded  dropped by the comparison
//	5 amy   500 refunded  kept
//	6 Anna  400 refunded  kept, and only LOWER lets it match 'a%'
//	7 alice 900 -         dropped by the subquery
func acceptanceSeed(t *testing.T, database *sql.DB, textType string) {
	t.Helper()
	exec := func(statement string) {
		t.Helper()
		_, err := database.ExecContext(t.Context(), statement)
		require.NoError(t, err, statement)
	}
	exec("CREATE TABLE orders (id INTEGER PRIMARY KEY, customer " + textType + " NOT NULL, amount INTEGER NOT NULL)")
	exec("CREATE TABLE refunds (id INTEGER PRIMARY KEY, order_id INTEGER NOT NULL)")
	exec(`INSERT INTO orders (id, customer, amount) VALUES
		(1, 'alice', 150), (2, 'alice', 200), (3, 'bob', 300), (4, 'amy', 50),
		(5, 'amy', 500), (6, 'Anna', 400), (7, 'alice', 900)`)
	exec(`INSERT INTO refunds (id, order_id) VALUES
		(1, 1), (2, 2), (3, 3), (4, 4), (5, 5), (6, 6)`)
}

func runAcceptance(t *testing.T, database *sql.DB, executor rasql.Executor, textType string) {
	t.Helper()
	acceptanceSeed(t, database, textType)
	rows, err := rasql.All(t.Context(), executor, acceptanceQuery(t))
	require.NoError(t, err)
	// The rows come back by descending total, which is the order the result
	// alias asks for and not the order LOWER(customer) alone would give.
	require.Equal(t, []acceptanceTotalRow{
		{
			Customer: "amy",
			Total:    rasql.Nullable[int64]{Value: 500, Valid: true},
			Average:  rasql.Nullable[float64]{Value: 500, Valid: true},
		},
		{
			Customer: "Anna",
			Total:    rasql.Nullable[int64]{Value: 400, Valid: true},
			Average:  rasql.Nullable[float64]{Value: 400, Valid: true},
		},
		{
			Customer: "alice",
			Total:    rasql.Nullable[int64]{Value: 350, Valid: true},
			Average:  rasql.Nullable[float64]{Value: 175, Valid: true},
		},
	}, rows)
}

// correlationQuery is acceptanceQuery reduced to the one restriction under
// test: the correlated EXISTS, with the IN subquery and every other operator
// removed. Order 7 is alice's, for 900, and is the only order with no refund.
// A correlated EXISTS is evaluated per row and drops it, leaving alice at 350.
// An EXISTS that lost its correlation asks only whether the refunds table has
// any row at all, which is true for every order, and alice would come back at
// 1250. The two answers differ, which is what makes this a test of the
// correlation rather than of the seed.
func correlationQuery(t *testing.T) rasql.Query[acceptanceTotalRow] {
	t.Helper()

	orders, err := rasql.ReadTableOf[acceptanceOrderRow](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "customer", Type: schema.TextType{}},
		{Name: "amount", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)
	refunds, err := rasql.ReadTableOf[acceptanceRefundRow](schema.TableDef{Name: "refunds", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "order_id", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)

	o, err := rasql.SourceOf(orders, "o")
	require.NoError(t, err)
	r, err := rasql.SourceOf(refunds, "r")
	require.NoError(t, err)

	orderID, err := rasql.BindColumn[acceptanceOrderRow, int64](o, "id", "")
	require.NoError(t, err)
	customer, err := rasql.BindColumn[acceptanceOrderRow, string](o, "customer", "")
	require.NoError(t, err)
	amount, err := rasql.BindColumn[acceptanceOrderRow, int64](o, "amount", "")
	require.NoError(t, err)
	refundOrder, err := rasql.BindColumn[acceptanceRefundRow, int64](r, "order_id", "")
	require.NoError(t, err)

	refunded, err := rasql.Scalar("order_id", refundOrder.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "customer", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true},
		rasql.ResultColumn{Name: "average", Type: schema.FloatType{}, Nullable: true},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("customer", customer.Expr(), schema.TextType{}, ""),
		rasql.NullItem("total", rasql.SumExpr(amount.Expr()), schema.IntegerType{}, ""),
		rasql.NullItem("average", rasql.AvgExpr(amount.Expr()), schema.FloatType{}, ""),
	}, acceptanceTotalDecoder{result: result})
	require.NoError(t, err)

	correlated, err := rasql.ExistsQuery(
		rasql.Select(r.Source(), refunded).
			Correlated(o.Source()).
			Where(rasql.EqualExpr(refundOrder.Expr(), orderID.Expr())),
	)
	require.NoError(t, err)

	return rasql.Select(o.Source(), projection).
		Where(correlated).
		GroupBy(rasql.Group(customer.Expr())).
		OrderBy(rasql.AscExpr(customer.Expr()))
}

func TestTypedOperator(t *testing.T) {
	t.Run("operators", func(t *testing.T) {
		t.Run("SQLite", func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			db, err := rasql.New(database, dialect.SQLite())
			require.NoError(t, err)
			profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runAcceptance(t, database, executor, "TEXT")
		})

		t.Run("PostgreSQL", func(t *testing.T) {
			database := dbtest.PostgreSQLDB(t)
			db, err := rasql.New(database, dialect.PostgreSQL())
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runAcceptance(t, database, executor, "TEXT")
		})

		t.Run("MySQL", func(t *testing.T) {
			database := dbtest.MySQLDB(t)
			db, err := rasql.New(database, dialect.MySQL())
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runAcceptance(t, database, executor, "VARCHAR(64)")
		})
	})

	t.Run("coalesce", func(t *testing.T) {
		t.Run("SQLite", func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			db, err := rasql.New(database, dialect.SQLite())
			require.NoError(t, err)
			profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runCoalesceGapAcceptance(t, database, executor)
		})

		t.Run("PostgreSQL", func(t *testing.T) {
			database := dbtest.PostgreSQLDB(t)
			db, err := rasql.New(database, dialect.PostgreSQL())
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runCoalesceGapAcceptance(t, database, executor)
		})

		t.Run("MySQL", func(t *testing.T) {
			database := dbtest.MySQLDB(t)
			db, err := rasql.New(database, dialect.MySQL())
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runCoalesceGapAcceptance(t, database, executor)
		})
	})

	t.Run("subquery", func(t *testing.T) {
		t.Run("SQLite", func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			db, err := rasql.New(database, dialect.SQLite())
			require.NoError(t, err)
			profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runSubqueryGapAcceptance(t, database, executor)
		})

		t.Run("PostgreSQL", func(t *testing.T) {
			database := dbtest.PostgreSQLDB(t)
			db, err := rasql.New(database, dialect.PostgreSQL())
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runSubqueryGapAcceptance(t, database, executor)
		})

		t.Run("MySQL", func(t *testing.T) {
			database := dbtest.MySQLDB(t)
			db, err := rasql.New(database, dialect.MySQL())
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runSubqueryGapAcceptance(t, database, executor)
		})
	})

	t.Run("subquery with nulls", func(t *testing.T) {
		t.Run("SQLite", func(t *testing.T) {
			database, err := sql.Open("sqlite", ":memory:")
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			db, err := rasql.New(database, dialect.SQLite())
			require.NoError(t, err)
			profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runSubqueryNullAcceptance(t, database, executor)
		})

		t.Run("PostgreSQL", func(t *testing.T) {
			database := dbtest.PostgreSQLDB(t)
			db, err := rasql.New(database, dialect.PostgreSQL())
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runSubqueryNullAcceptance(t, database, executor)
		})

		t.Run("MySQL", func(t *testing.T) {
			database := dbtest.MySQLDB(t)
			db, err := rasql.New(database, dialect.MySQL())
			require.NoError(t, err)
			profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
			require.NoError(t, err)
			executor, err := rasql.AsExecutor(db, profile)
			require.NoError(t, err)
			runSubqueryNullAcceptance(t, database, executor)
		})
	})

	t.Run("correlation is evaluated per row", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = database.Close() })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		acceptanceSeed(t, database, "TEXT")

		rows, err := rasql.All(t.Context(), executor, correlationQuery(t))
		require.NoError(t, err)
		totals := make(map[string]int64, len(rows))
		for _, row := range rows {
			totals[row.Customer] = row.Total.Value
		}
		require.Equal(t, int64(350), totals["alice"],
			"order 7 has no refund, so a per-row EXISTS drops it; 1250 would mean the correlation was lost",
		)
	})
}

// This proves CoalesceExpr, the typed COALESCE. It takes a nullable column
// and a never-NULL fallback and returns a plain Expr, which the test uses
// twice over: once to read the substituted value back per row, and once to
// feed that Expr into GreaterValue, an operator only a non-null Expr can
// reach — a NullExpr could not have been passed there at all, so the query
// below would fail to compile if CoalesceExpr still returned one.

type coalesceGapAccountRow struct {
	Name string
}

type coalesceGapDecoder struct{ result rasql.ResultSchema }

func (d coalesceGapDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d coalesceGapDecoder) Presence() []rasql.Presence       { return nil }
func (d coalesceGapDecoder) DecodeRow(src rasql.ScanSource, row *coalesceGapAccountRow) error {
	return src.Scan(&row.Name)
}

// coalesceGapQuery builds:
//
//	SELECT name FROM accounts
//	WHERE COALESCE(credit_limit, 100) > 40
//	ORDER BY name
//
// credit_limit is nullable; 100 is the fallback CoalesceExpr substitutes for
// a NULL row. The threshold, 40, sits strictly between the fallback and one
// seeded row's real limit, so the result set only comes out right if NULL
// rows are actually replaced with 100 rather than passed through as NULL
// (which would drop that row from a > comparison entirely) or as some other
// placeholder such as 0 (which would also drop it, since 0 is not > 40).
func coalesceGapQuery(t *testing.T) rasql.Query[coalesceGapAccountRow] {
	t.Helper()

	accounts, err := rasql.ReadTableOf[coalesceGapAccountRow](schema.TableDef{Name: "accounts", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
		{Name: "credit_limit", Type: schema.IntegerType{}, Nullable: true},
	}})
	require.NoError(t, err)
	a, err := rasql.SourceOf(accounts, "")
	require.NoError(t, err)

	name, err := rasql.BindColumn[coalesceGapAccountRow, string](a, "name", "")
	require.NoError(t, err)
	creditLimit, err := rasql.BindNullColumn[coalesceGapAccountRow, int64](a, "credit_limit", "")
	require.NoError(t, err)

	limit := rasql.CoalesceExpr(creditLimit.NullExpr(), rasql.Value(int64(100)))

	result, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "name", Type: schema.TextType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("name", name.Expr(), schema.TextType{}, ""),
	}, coalesceGapDecoder{result: result})
	require.NoError(t, err)

	return rasql.Select(a.Source(), projection).
		Where(rasql.GreaterValue(limit, int64(40))).
		OrderBy(rasql.AscExpr(name.Expr()))
}

// coalesceGapSeed seeds three accounts: alice has no credit_limit at all
// (NULL, falls back to 100, kept by > 40), bob's real limit is 30 (kept as
// 30, dropped by > 40), and carol's real limit is 5000 (kept as 5000, kept
// by > 40). Alice and carol surviving while bob does not is what a wrong
// fallback value or a fallback that only sometimes applies would get wrong.
func coalesceGapSeed(t *testing.T, database *sql.DB) {
	t.Helper()
	exec := func(statement string) {
		t.Helper()
		_, err := database.ExecContext(t.Context(), statement)
		require.NoError(t, err, statement)
	}
	exec("CREATE TABLE accounts (id INTEGER PRIMARY KEY, name VARCHAR(64) NOT NULL, credit_limit INTEGER)")
	exec(`INSERT INTO accounts (id, name, credit_limit) VALUES
		(1, 'alice', NULL), (2, 'bob', 30), (3, 'carol', 5000)`)
}

func runCoalesceGapAcceptance(t *testing.T, database *sql.DB, executor rasql.Executor) {
	t.Helper()
	coalesceGapSeed(t, database)
	rows, err := rasql.All(t.Context(), executor, coalesceGapQuery(t))
	require.NoError(t, err)
	require.Equal(t, []coalesceGapAccountRow{
		{Name: "alice"},
		{Name: "carol"},
	}, rows)
}

// This proves SubqueryExpr, which lifts a scalar subquery into an Expr[T] so
// it can stand either as an operand of a comparison or as a projected value.
// InQuery, NotInQuery, ExistsQuery and NotExistsQuery already cover
// membership and existence; SubqueryExpr is what a value comparison such as
// WHERE amount > (SELECT ...), or a projection such as
// SELECT name, (SELECT COUNT(*) ...), needed and had no typed spelling for.

type subqueryGapCustomerRow struct {
	ID   int64
	Name string
}

type subqueryGapOrderRow struct {
	ID         int64
	CustomerID int64
	Amount     int64
}

// subqueryGapRow is one customer and how many of their orders beat a
// baseline order's amount.
type subqueryGapRow struct {
	Name          string
	BigOrderCount int64
}

type subqueryGapDecoder struct{ result rasql.ResultSchema }

func (d subqueryGapDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d subqueryGapDecoder) Presence() []rasql.Presence       { return nil }
func (d subqueryGapDecoder) DecodeRow(src rasql.ScanSource, row *subqueryGapRow) error {
	return src.Scan(&row.Name, &row.BigOrderCount)
}

// subqueryGapQuery builds, for each customer:
//
//	SELECT c.name,
//	       (SELECT COUNT(*) FROM orders o
//	         WHERE o.customer_id = c.id
//	           AND o.amount > (SELECT amount FROM orders WHERE id = 1))
//	FROM customers c
//
// The outer projection reads a scalar subquery as a value (SubqueryExpr
// standing where Item wants an Expr), that subquery is correlated to the
// enclosing customer through Correlated, and its own WHERE compares an
// ordinary column against a second scalar subquery two levels down from the
// outer query — SubqueryExpr standing as the right-hand operand of
// GreaterExpr. Both positions the capability has to cover appear in one
// query, and the inner one nests inside the outer one, the way a reader
// composing this from smaller queries would naturally arrive at it.
func subqueryGapQuery(t *testing.T) rasql.Query[subqueryGapRow] {
	t.Helper()

	customers, err := rasql.ReadTableOf[subqueryGapCustomerRow](schema.TableDef{Name: "customers", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	orders, err := rasql.ReadTableOf[subqueryGapOrderRow](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "customer_id", Type: schema.IntegerType{}},
		{Name: "amount", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)

	c, err := rasql.SourceOf(customers, "c")
	require.NoError(t, err)
	o, err := rasql.SourceOf(orders, "o")
	require.NoError(t, err)
	baseline, err := rasql.SourceOf(orders, "baseline")
	require.NoError(t, err)

	customerID, err := rasql.BindColumn[subqueryGapCustomerRow, int64](c, "id", "")
	require.NoError(t, err)
	customerName, err := rasql.BindColumn[subqueryGapCustomerRow, string](c, "name", "")
	require.NoError(t, err)
	orderCustomerID, err := rasql.BindColumn[subqueryGapOrderRow, int64](o, "customer_id", "")
	require.NoError(t, err)
	orderAmount, err := rasql.BindColumn[subqueryGapOrderRow, int64](o, "amount", "")
	require.NoError(t, err)
	baselineID, err := rasql.BindColumn[subqueryGapOrderRow, int64](baseline, "id", "")
	require.NoError(t, err)
	baselineAmount, err := rasql.BindColumn[subqueryGapOrderRow, int64](baseline, "amount", "")
	require.NoError(t, err)

	// Level two: the amount of the fixed order seeded with id 1, read as a
	// plain (non-aggregate) scalar subquery. SubqueryExpr always returns
	// NullExpr, since a subquery can never promise a row regardless of what
	// it selects; CoalesceExpr turns it into the plain, ordered Expr
	// GreaterExpr requires. The fallback is never actually read here, since
	// the baseline order seeded with id 1 always exists.
	baselineProjection, err := rasql.Scalar("amount", baselineAmount.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	baselineQuery := rasql.Select(baseline.Source(), baselineProjection).
		Where(rasql.EqualValue(baselineID.Expr(), int64(1)))
	baselineSubquery, err := rasql.SubqueryExpr(baselineQuery)
	require.NoError(t, err)
	baselineExpr := rasql.CoalesceExpr(baselineSubquery, rasql.Value(int64(0)))

	// Level one: how many of this customer's orders beat the baseline.
	// Correlated(c.Source()) is what makes "this customer's" true instead of
	// "every customer's" — WithCorrelation states the same rule.
	// bigOrderCountQuery is an aggregate without GROUP BY, so it too always
	// returns exactly one row; CoalesceExpr turns SubqueryExpr's NullExpr
	// into the plain Expr Item wants, and its fallback is never actually
	// read for the same reason.
	bigOrderCountProjection, err := rasql.Scalar("big_order_count", rasql.CountRows(), schema.IntegerType{}, "")
	require.NoError(t, err)
	bigOrderCountQuery := rasql.Select(o.Source(), bigOrderCountProjection).
		Correlated(c.Source()).
		Where(rasql.And(
			rasql.EqualExpr(orderCustomerID.Expr(), customerID.Expr()),
			rasql.GreaterExpr(orderAmount.Expr(), baselineExpr),
		))
	bigOrderCountSubquery, err := rasql.SubqueryExpr(bigOrderCountQuery)
	require.NoError(t, err)
	bigOrderCountExpr := rasql.CoalesceExpr(bigOrderCountSubquery, rasql.Value(int64(0)))

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "big_order_count", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("name", customerName.Expr(), schema.TextType{}, ""),
		rasql.Item("big_order_count", bigOrderCountExpr, schema.IntegerType{}, ""),
	}, subqueryGapDecoder{result: result})
	require.NoError(t, err)

	return rasql.Select(c.Source(), projection).OrderBy(rasql.AscExpr(customerName.Expr()))
}

// subqueryGapSeed seeds two customers and five orders. Order 1 is the
// baseline, amount 50. Alice's other orders are 80 and 120, both above the
// baseline, so her big_order_count is 2. Bob's other order is 999, above
// the baseline, so his is 1; his 10 is below it and does not count.
//
// A correlation that silently fell back to counting every order in the
// table, instead of just this customer's, would report the same total for
// every customer: 3 (80, 120 and 999, every order above the baseline) for
// both Alice and Bob, instead of the correct 2 and 1. A baseline comparison
// that got dropped, leaving a bare per-customer order count, would report 3
// for Alice (all of her orders) and 2 for Bob, instead of 2 and 1. Both
// wrong answers differ from the right one in a way a passing test would
// have to notice.
func subqueryGapSeed(t *testing.T, database *sql.DB) {
	t.Helper()
	exec := func(statement string) {
		t.Helper()
		_, err := database.ExecContext(t.Context(), statement)
		require.NoError(t, err, statement)
	}
	exec("CREATE TABLE customers (id INTEGER PRIMARY KEY, name VARCHAR(64) NOT NULL)")
	exec("CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL, amount INTEGER NOT NULL)")
	exec(`INSERT INTO customers (id, name) VALUES (1, 'alice'), (2, 'bob')`)
	exec(`INSERT INTO orders (id, customer_id, amount) VALUES
		(1, 1, 50), (2, 1, 80), (3, 1, 120), (4, 2, 10), (5, 2, 999)`)
}

func runSubqueryGapAcceptance(t *testing.T, database *sql.DB, executor rasql.Executor) {
	t.Helper()
	subqueryGapSeed(t, database)
	rows, err := rasql.All(t.Context(), executor, subqueryGapQuery(t))
	require.NoError(t, err)
	require.Equal(t, []subqueryGapRow{
		{Name: "alice", BigOrderCount: 2},
		{Name: "bob", BigOrderCount: 1},
	}, rows)
}

// This is the proof the SubqueryExpr fix was missing: a scalar subquery over
// zero rows must decode as a NULL-valued Nullable[T], never fail decoding
// outright. Before SubqueryExpr returned NullExpr, projecting its result
// through Item as a non-null column compiled cleanly and then failed at
// decode time the moment the subquery's table was empty, with
// `decode column "total" failed: unexpected SQL NULL`. NullItem is the
// typed API's way to say a projected value may come back NULL, and it is
// what SubqueryExpr's NullExpr now requires a caller to use for this case
// instead of failing at runtime.

type subqueryNullCustomerRow struct {
	ID int64
}

type subqueryNullOrderRow struct {
	ID         int64
	CustomerID int64
	Amount     int64
}

// subqueryNullResultRow is one customer with the amount of an order they
// have never placed: orders stays empty, so the scalar subquery behind
// Total has nothing to select from and comes back NULL.
type subqueryNullResultRow struct {
	Total rasql.Nullable[int64]
}

type subqueryNullDecoder struct{ result rasql.ResultSchema }

func (d subqueryNullDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d subqueryNullDecoder) Presence() []rasql.Presence       { return nil }
func (d subqueryNullDecoder) DecodeRow(src rasql.ScanSource, row *subqueryNullResultRow) error {
	return src.Scan(&row.Total)
}

// subqueryNullQuery builds:
//
//	SELECT (SELECT amount FROM orders WHERE customer_id = c.id) AS total
//	FROM customers c
//
// against an orders table that stays empty, so the correlated scalar
// subquery has zero rows to return from for the one seeded customer.
func subqueryNullQuery(t *testing.T) rasql.Query[subqueryNullResultRow] {
	t.Helper()

	customers, err := rasql.ReadTableOf[subqueryNullCustomerRow](schema.TableDef{Name: "customers", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)
	orders, err := rasql.ReadTableOf[subqueryNullOrderRow](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "customer_id", Type: schema.IntegerType{}},
		{Name: "amount", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)

	c, err := rasql.SourceOf(customers, "c")
	require.NoError(t, err)
	o, err := rasql.SourceOf(orders, "o")
	require.NoError(t, err)

	customerID, err := rasql.BindColumn[subqueryNullCustomerRow, int64](c, "id", "")
	require.NoError(t, err)
	orderCustomerID, err := rasql.BindColumn[subqueryNullOrderRow, int64](o, "customer_id", "")
	require.NoError(t, err)
	orderAmount, err := rasql.BindColumn[subqueryNullOrderRow, int64](o, "amount", "")
	require.NoError(t, err)

	amountProjection, err := rasql.Scalar("amount", orderAmount.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	totalQuery := rasql.Select(o.Source(), amountProjection).
		Correlated(c.Source()).
		Where(rasql.EqualExpr(orderCustomerID.Expr(), customerID.Expr()))
	totalExpr, err := rasql.SubqueryExpr(totalQuery)
	require.NoError(t, err)

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.NullItem("total", totalExpr, schema.IntegerType{}, ""),
	}, subqueryNullDecoder{result: result})
	require.NoError(t, err)

	return rasql.Select(c.Source(), projection)
}

func subqueryNullSeed(t *testing.T, database *sql.DB) {
	t.Helper()
	exec := func(statement string) {
		t.Helper()
		_, err := database.ExecContext(t.Context(), statement)
		require.NoError(t, err, statement)
	}
	exec("CREATE TABLE customers (id INTEGER PRIMARY KEY)")
	exec("CREATE TABLE orders (id INTEGER PRIMARY KEY, customer_id INTEGER NOT NULL, amount INTEGER NOT NULL)")
	exec("INSERT INTO customers (id) VALUES (1)")
	// orders stays empty.
}

func runSubqueryNullAcceptance(t *testing.T, database *sql.DB, executor rasql.Executor) {
	t.Helper()
	subqueryNullSeed(t, database)
	rows, err := rasql.All(t.Context(), executor, subqueryNullQuery(t))
	require.NoError(t, err)
	require.Equal(t, []subqueryNullResultRow{{Total: rasql.Nullable[int64]{}}}, rows)
}

// This proves BindTypedColumn and BindNullTypedColumn, the bridge from a
// generated store accessor's query.TypedColumn into the typed Expr/Column
// layer. Today the only entry point is BindColumn, which names a column by
// string and so gives up the compile-time check that makes a renamed column
// a build failure; this is the entry point that keeps that check.

// bindTypedColumnGapRow stands in for a generated store row.
type bindTypedColumnGapRow struct {
	ID       int64
	Name     string
	Nickname rasql.Nullable[string]
}

// bindTypedColumnGapTable is shaped exactly the way rasqlgen shapes a
// generated table: an embedded Table[T] and one accessor method per column,
// returning query.TypedColumn or query.NullableColumn rather than a string.
// It is hand-written here only because this task may not touch examples/ or
// the generator while another agent is converting them onto this API;
// BindTypedColumn and BindNullTypedColumn are proved against this exact
// shape, the one a real generated store also produces.
type bindTypedColumnGapTable struct {
	rasql.Table[bindTypedColumnGapRow]
}

func (t bindTypedColumnGapTable) ID() query.TypedColumn[bindTypedColumnGapRow, int64] {
	return query.TypedColumnOf[bindTypedColumnGapRow, int64](rasql.ColumnOf(t.Table, "id"))
}
func (t bindTypedColumnGapTable) Name() query.TypedColumn[bindTypedColumnGapRow, string] {
	return query.TypedColumnOf[bindTypedColumnGapRow, string](rasql.ColumnOf(t.Table, "name"))
}
func (t bindTypedColumnGapTable) Nickname() query.NullableColumn[bindTypedColumnGapRow, string] {
	return query.NullableColumnOf[bindTypedColumnGapRow, string](rasql.ColumnOf(t.Table, "nickname"))
}

func bindTypedColumnGapUsers(t *testing.T) bindTypedColumnGapTable {
	t.Helper()
	table, err := rasql.TableOf[bindTypedColumnGapRow](schema.TableDef{Name: "gap_users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
		{Name: "nickname", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	return bindTypedColumnGapTable{Table: table}
}

type bindTypedColumnGapResultRow struct {
	Name     string
	Nickname rasql.Nullable[string]
}

type bindTypedColumnGapDecoder struct{ result rasql.ResultSchema }

func (d bindTypedColumnGapDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d bindTypedColumnGapDecoder) Presence() []rasql.Presence       { return nil }
func (d bindTypedColumnGapDecoder) DecodeRow(src rasql.ScanSource, row *bindTypedColumnGapResultRow) error {
	return src.Scan(&row.Name, &row.Nickname)
}

// bindTypedColumnGapQuery builds
//
//	SELECT name, nickname FROM gap_users WHERE id > 0 ORDER BY name
//
// naming every column through users.ID(), users.Name() and users.Nickname()
// — the query.TypedColumn and query.NullableColumn a generated accessor
// returns — bridged by BindTypedColumn and BindNullTypedColumn, never by a
// string passed to BindColumn.
func bindTypedColumnGapQuery(t *testing.T) rasql.Query[bindTypedColumnGapResultRow] {
	t.Helper()

	users := bindTypedColumnGapUsers(t)
	u, err := rasql.SourceOf(users, "")
	require.NoError(t, err)

	id, err := rasql.BindTypedColumn(users.ID())
	require.NoError(t, err)
	name, err := rasql.BindTypedColumn(users.Name())
	require.NoError(t, err)
	nickname, err := rasql.BindNullTypedColumn(users.Nickname())
	require.NoError(t, err)

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "name", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "nickname", Type: schema.TextType{}, Nullable: true},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("name", name.Expr(), schema.TextType{}, ""),
		rasql.NullItem("nickname", nickname.NullExpr(), schema.TextType{}, ""),
	}, bindTypedColumnGapDecoder{result: result})
	require.NoError(t, err)

	return rasql.Select(u.Source(), projection).
		Where(rasql.GreaterValue(id.Expr(), int64(0))).
		OrderBy(rasql.AscExpr(name.Expr()))
}

func bindTypedColumnGapSeed(t *testing.T, database *sql.DB) {
	t.Helper()
	exec := func(statement string) {
		t.Helper()
		_, err := database.ExecContext(t.Context(), statement)
		require.NoError(t, err, statement)
	}
	exec("CREATE TABLE gap_users (id INTEGER PRIMARY KEY, name VARCHAR(64) NOT NULL, nickname VARCHAR(64))")
	exec(`INSERT INTO gap_users (id, name, nickname) VALUES (1, 'alice', 'ali'), (2, 'bob', NULL)`)
}

// runBindTypedColumnGapAcceptance runs the query and checks the decoded
// rows. BindTypedColumn or BindNullTypedColumn recovering the wrong column
// (say, swapping which accessor's ColumnRef backs which typed handle) would
// either fail Validate outright or decode the wrong values into name and
// nickname; getting alice's real nickname back and bob's real NULL back is
// what a passing assertion here requires.
func runBindTypedColumnGapAcceptance(t *testing.T, database *sql.DB, executor rasql.Executor) {
	t.Helper()
	bindTypedColumnGapSeed(t, database)
	rows, err := rasql.All(t.Context(), executor, bindTypedColumnGapQuery(t))
	require.NoError(t, err)
	require.Equal(t, []bindTypedColumnGapResultRow{
		{Name: "alice", Nickname: rasql.Nullable[string]{Value: "ali", Valid: true}},
		{Name: "bob", Nickname: rasql.Nullable[string]{}},
	}, rows)
}

func TestBindTypedColumn(t *testing.T) {
	t.Run("SQLite", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = database.Close() })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		runBindTypedColumnGapAcceptance(t, database, executor)
	})

	t.Run("PostgreSQL", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)
		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		runBindTypedColumnGapAcceptance(t, database, executor)
	})

	t.Run("MySQL", func(t *testing.T) {
		database := dbtest.MySQLDB(t)
		db, err := rasql.New(database, dialect.MySQL())
		require.NoError(t, err)
		profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		runBindTypedColumnGapAcceptance(t, database, executor)
	})
}

// This proves Render, the typed counterpart of the removed
// TypedSelectBuilder.Build(dialect): a way to lower a typed Query to SQL text
// with no database involved at all. No engine is "reachable" or
// "unreachable" here on purpose — that is exactly the point of the
// capability, and every dialect it supports is exercised directly.

type renderGapRow struct {
	ID   int64
	Name string
}

type renderGapDecoder struct{ result rasql.ResultSchema }

func (d renderGapDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d renderGapDecoder) Presence() []rasql.Presence       { return nil }
func (d renderGapDecoder) DecodeRow(src rasql.ScanSource, row *renderGapRow) error {
	return src.Scan(&row.ID, &row.Name)
}

func renderGapQuery(t *testing.T) rasql.Query[renderGapRow] {
	t.Helper()

	widgets, err := rasql.ReadTableOf[renderGapRow](schema.TableDef{Name: "widgets", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	w, err := rasql.SourceOf(widgets, "")
	require.NoError(t, err)

	id, err := rasql.BindColumn[renderGapRow, int64](w, "id", "")
	require.NoError(t, err)
	name, err := rasql.BindColumn[renderGapRow, string](w, "name", "")
	require.NoError(t, err)

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "name", Type: schema.TextType{}},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("name", name.Expr(), schema.TextType{}, ""),
	}, renderGapDecoder{result: result})
	require.NoError(t, err)

	return rasql.Select(w.Source(), projection).
		Where(rasql.GreaterValue(id.Expr(), int64(5))).
		OrderBy(rasql.AscExpr(name.Expr()))
}

// TestRenderProducesDialectSpecificSQL builds one query and renders it for
// all three dialects with no database at all. A Render that ignored d and
// always produced one dialect's quoting and placeholder style would still
// pass a test that checked only one dialect; asserting the exact SQL and
// args for all three here is what a wrong implementation cannot satisfy.
func TestRenderProducesDialectSpecificSQL(t *testing.T) {
	q := renderGapQuery(t)

	sqliteStatement, err := rasql.Render(q, dialect.SQLite())
	require.NoError(t, err)
	require.Equal(t, `SELECT "widgets"."id" AS "id", "widgets"."name" AS "name" FROM "widgets" WHERE ("widgets"."id" > ?) ORDER BY "widgets"."name"`, sqliteStatement.SQL())
	require.Equal(t, []any{int64(5)}, sqliteStatement.Args())

	postgresStatement, err := rasql.Render(q, dialect.PostgreSQL())
	require.NoError(t, err)
	require.Equal(t, `SELECT "widgets"."id" AS "id", "widgets"."name" AS "name" FROM "widgets" WHERE ("widgets"."id" > $1) ORDER BY "widgets"."name"`, postgresStatement.SQL())
	require.Equal(t, []any{int64(5)}, postgresStatement.Args())

	mysqlStatement, err := rasql.Render(q, dialect.MySQL())
	require.NoError(t, err)
	require.Equal(t, "SELECT `widgets`.`id` AS `id`, `widgets`.`name` AS `name` FROM `widgets` WHERE (`widgets`.`id` > ?) ORDER BY `widgets`.`name`", mysqlStatement.SQL())
	require.Equal(t, []any{int64(5)}, mysqlStatement.Args())
}
