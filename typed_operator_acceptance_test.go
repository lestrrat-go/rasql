//go:build unix

package rasql

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// This is the acceptance case for the typed expression language. It is one
// realistic query that a reader would expect any ORM to express, and it uses
// only the canonical typed API: no query.Expression escape hatch, no raw SQL.
//
// It exercises the five operator groups the typed API was missing:
// an ordered comparison, a string function, a pattern match, a SUM aggregate,
// and an IN subquery. A query the typed API cannot express is a capability
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
	Total    Nullable[int64]
	Average  Nullable[float64]
}

type acceptanceTotalDecoder struct{ result ResultSchema }

func (d acceptanceTotalDecoder) ResultSchema() ResultSchema { return d.result }
func (d acceptanceTotalDecoder) Presence() []Presence       { return nil }
func (d acceptanceTotalDecoder) DecodeRow(src ScanSource, row *acceptanceTotalRow) error {
	return src.Scan(&row.Customer, &row.Total, &row.Average)
}

// acceptanceQuery builds:
//
//	SELECT o.customer, SUM(o.amount), AVG(o.amount)
//	FROM orders o
//	WHERE o.amount > 100
//	  AND LOWER(o.customer) LIKE 'a%'
//	  AND o.id IN (SELECT r.order_id FROM refunds r)
//	GROUP BY o.customer
//	ORDER BY LOWER(o.customer)
func acceptanceQuery(t *testing.T) Query[acceptanceTotalRow] {
	t.Helper()

	orders, err := ReadTableOf[acceptanceOrderRow](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "customer", Type: schema.TextType{}},
		{Name: "amount", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)
	refunds, err := ReadTableOf[acceptanceRefundRow](schema.TableDef{Name: "refunds", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "order_id", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)

	o, err := SourceOf(orders, "o")
	require.NoError(t, err)
	r, err := SourceOf(refunds, "r")
	require.NoError(t, err)

	orderID, err := BindColumn[acceptanceOrderRow, int64](o, "id", "")
	require.NoError(t, err)
	customer, err := BindColumn[acceptanceOrderRow, string](o, "customer", "")
	require.NoError(t, err)
	amount, err := BindColumn[acceptanceOrderRow, int64](o, "amount", "")
	require.NoError(t, err)
	refundOrder, err := BindColumn[acceptanceRefundRow, int64](r, "order_id", "")
	require.NoError(t, err)

	// The subquery projects exactly one int64 column, so InQuery can check
	// its element type against the left-hand side at compile time.
	refunded, err := Scalar("order_id", refundOrder.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	refundQuery := Select(r.Source(), refunded)

	result, err := NewResultSchema(
		ResultColumn{Name: "customer", Type: schema.TextType{}},
		ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true},
		ResultColumn{Name: "average", Type: schema.FloatType{}, Nullable: true},
	)
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{
		Item("customer", customer.Expr(), schema.TextType{}, ""),
		NullItem("total", SumExpr(amount.Expr()), schema.IntegerType{}, ""),
		NullItem("average", AvgExpr(amount.Expr()), schema.FloatType{}, ""),
	}, acceptanceTotalDecoder{result: result})
	require.NoError(t, err)

	// InQuery reports its error rather than deferring it, like every other
	// structural composition in this package.
	refundedOrder, err := InQuery(orderID.Expr(), refundQuery)
	require.NoError(t, err)

	return Select(o.Source(), projection).
		Where(And(
			GreaterValue(amount.Expr(), int64(100)),
			LikeValue(LowerExpr(customer.Expr()), "a%"),
			refundedOrder,
		)).
		GroupBy(Group(customer.Expr())).
		OrderBy(AscExpr(LowerExpr(customer.Expr())))
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

func runAcceptance(t *testing.T, database *sql.DB, executor Executor, textType string) {
	t.Helper()
	acceptanceSeed(t, database, textType)
	rows, err := All(t.Context(), executor, acceptanceQuery(t))
	require.NoError(t, err)
	require.Equal(t, []acceptanceTotalRow{
		{
			Customer: "alice",
			Total:    Nullable[int64]{Value: 350, Valid: true},
			Average:  Nullable[float64]{Value: 175, Valid: true},
		},
		{
			Customer: "amy",
			Total:    Nullable[int64]{Value: 500, Valid: true},
			Average:  Nullable[float64]{Value: 500, Valid: true},
		},
		{
			Customer: "Anna",
			Total:    Nullable[int64]{Value: 400, Valid: true},
			Average:  Nullable[float64]{Value: 400, Valid: true},
		},
	}, rows)
}

func TestTypedOperatorAcceptanceSQLite(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runAcceptance(t, database, executor, "TEXT")
}

func TestTypedOperatorAcceptancePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	db, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runAcceptance(t, database, executor, "TEXT")
}

func TestTypedOperatorAcceptanceMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	db, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runAcceptance(t, database, executor, "VARCHAR(64)")
}
