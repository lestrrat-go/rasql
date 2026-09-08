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

type subqueryGapDecoder struct{ result ResultSchema }

func (d subqueryGapDecoder) ResultSchema() ResultSchema { return d.result }
func (d subqueryGapDecoder) Presence() []Presence       { return nil }
func (d subqueryGapDecoder) DecodeRow(src ScanSource, row *subqueryGapRow) error {
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
func subqueryGapQuery(t *testing.T) Query[subqueryGapRow] {
	t.Helper()

	customers, err := ReadTableOf[subqueryGapCustomerRow](schema.TableDef{Name: "customers", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	orders, err := ReadTableOf[subqueryGapOrderRow](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "customer_id", Type: schema.IntegerType{}},
		{Name: "amount", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)

	c, err := SourceOf(customers, "c")
	require.NoError(t, err)
	o, err := SourceOf(orders, "o")
	require.NoError(t, err)
	baseline, err := SourceOf(orders, "baseline")
	require.NoError(t, err)

	customerID, err := BindColumn[subqueryGapCustomerRow, int64](c, "id", "")
	require.NoError(t, err)
	customerName, err := BindColumn[subqueryGapCustomerRow, string](c, "name", "")
	require.NoError(t, err)
	orderCustomerID, err := BindColumn[subqueryGapOrderRow, int64](o, "customer_id", "")
	require.NoError(t, err)
	orderAmount, err := BindColumn[subqueryGapOrderRow, int64](o, "amount", "")
	require.NoError(t, err)
	baselineID, err := BindColumn[subqueryGapOrderRow, int64](baseline, "id", "")
	require.NoError(t, err)
	baselineAmount, err := BindColumn[subqueryGapOrderRow, int64](baseline, "amount", "")
	require.NoError(t, err)

	// Level two: the amount of the fixed order seeded with id 1, read as a
	// plain (non-aggregate) scalar subquery so its element type stays a
	// comparable, ordered, non-null int64 the way InQuery's own subqueries
	// do — an aggregate such as AVG would come back NullExpr instead, which
	// GreaterExpr does not accept.
	baselineProjection, err := Scalar("amount", baselineAmount.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	baselineQuery := Select(baseline.Source(), baselineProjection).
		Where(EqualValue(baselineID.Expr(), int64(1)))
	baselineExpr, err := SubqueryExpr(baselineQuery)
	require.NoError(t, err)

	// Level one: how many of this customer's orders beat the baseline.
	// Correlated(c.Source()) is what makes "this customer's" true instead of
	// "every customer's" — WithCorrelation states the same rule.
	bigOrderCountProjection, err := Scalar("big_order_count", CountRows(), schema.IntegerType{}, "")
	require.NoError(t, err)
	bigOrderCountQuery := Select(o.Source(), bigOrderCountProjection).
		Correlated(c.Source()).
		Where(And(
			EqualExpr(orderCustomerID.Expr(), customerID.Expr()),
			GreaterExpr(orderAmount.Expr(), baselineExpr),
		))
	bigOrderCountExpr, err := SubqueryExpr(bigOrderCountQuery)
	require.NoError(t, err)

	result, err := NewResultSchema(
		ResultColumn{Name: "name", Type: schema.TextType{}},
		ResultColumn{Name: "big_order_count", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{
		Item("name", customerName.Expr(), schema.TextType{}, ""),
		Item("big_order_count", bigOrderCountExpr, schema.IntegerType{}, ""),
	}, subqueryGapDecoder{result: result})
	require.NoError(t, err)

	return Select(c.Source(), projection).OrderBy(AscExpr(customerName.Expr()))
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

func runSubqueryGapAcceptance(t *testing.T, database *sql.DB, executor Executor) {
	t.Helper()
	subqueryGapSeed(t, database)
	rows, err := All(t.Context(), executor, subqueryGapQuery(t))
	require.NoError(t, err)
	require.Equal(t, []subqueryGapRow{
		{Name: "alice", BigOrderCount: 2},
		{Name: "bob", BigOrderCount: 1},
	}, rows)
}

func TestTypedOperatorSubqueryExprAcceptanceSQLite(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runSubqueryGapAcceptance(t, database, executor)
}

func TestTypedOperatorSubqueryExprAcceptancePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	db, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runSubqueryGapAcceptance(t, database, executor)
}

func TestTypedOperatorSubqueryExprAcceptanceMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	db, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runSubqueryGapAcceptance(t, database, executor)
}
