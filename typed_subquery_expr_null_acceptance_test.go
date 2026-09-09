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
	Total Nullable[int64]
}

type subqueryNullDecoder struct{ result ResultSchema }

func (d subqueryNullDecoder) ResultSchema() ResultSchema { return d.result }
func (d subqueryNullDecoder) Presence() []Presence       { return nil }
func (d subqueryNullDecoder) DecodeRow(src ScanSource, row *subqueryNullResultRow) error {
	return src.Scan(&row.Total)
}

// subqueryNullQuery builds:
//
//	SELECT (SELECT amount FROM orders WHERE customer_id = c.id) AS total
//	FROM customers c
//
// against an orders table that stays empty, so the correlated scalar
// subquery has zero rows to return from for the one seeded customer.
func subqueryNullQuery(t *testing.T) Query[subqueryNullResultRow] {
	t.Helper()

	customers, err := ReadTableOf[subqueryNullCustomerRow](schema.TableDef{Name: "customers", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)
	orders, err := ReadTableOf[subqueryNullOrderRow](schema.TableDef{Name: "orders", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "customer_id", Type: schema.IntegerType{}},
		{Name: "amount", Type: schema.IntegerType{}},
	}})
	require.NoError(t, err)

	c, err := SourceOf(customers, "c")
	require.NoError(t, err)
	o, err := SourceOf(orders, "o")
	require.NoError(t, err)

	customerID, err := BindColumn[subqueryNullCustomerRow, int64](c, "id", "")
	require.NoError(t, err)
	orderCustomerID, err := BindColumn[subqueryNullOrderRow, int64](o, "customer_id", "")
	require.NoError(t, err)
	orderAmount, err := BindColumn[subqueryNullOrderRow, int64](o, "amount", "")
	require.NoError(t, err)

	amountProjection, err := Scalar("amount", orderAmount.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	totalQuery := Select(o.Source(), amountProjection).
		Correlated(c.Source()).
		Where(EqualExpr(orderCustomerID.Expr(), customerID.Expr()))
	totalExpr, err := SubqueryExpr(totalQuery)
	require.NoError(t, err)

	result, err := NewResultSchema(
		ResultColumn{Name: "total", Type: schema.IntegerType{}, Nullable: true},
	)
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{
		NullItem("total", totalExpr, schema.IntegerType{}, ""),
	}, subqueryNullDecoder{result: result})
	require.NoError(t, err)

	return Select(c.Source(), projection)
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

func runSubqueryNullAcceptance(t *testing.T, database *sql.DB, executor Executor) {
	t.Helper()
	subqueryNullSeed(t, database)
	rows, err := All(t.Context(), executor, subqueryNullQuery(t))
	require.NoError(t, err)
	require.Equal(t, []subqueryNullResultRow{{Total: Nullable[int64]{}}}, rows)
}

func TestTypedOperatorSubqueryExprNullAcceptanceSQLite(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runSubqueryNullAcceptance(t, database, executor)
}

func TestTypedOperatorSubqueryExprNullAcceptancePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	db, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runSubqueryNullAcceptance(t, database, executor)
}

func TestTypedOperatorSubqueryExprNullAcceptanceMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	db, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runSubqueryNullAcceptance(t, database, executor)
}
