//go:build unix

package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// This proves a correlated scalar subquery can stand as a projected value in
// a SELECT list, and that the shape composes two levels deep: the outer
// query's projected value is a correlated scalar subquery (middle) whose own
// projected value is itself a correlated scalar subquery (leaf).
// SubqueryExpr is what turns a Query[T] into an Expr[T] wherever Item wants
// one, and Query.Correlated is what ties each level back to the row of the
// query enclosing it.

type apiQ7User struct {
	ID int64
}

type apiQ7Order struct {
	ID     int64
	UserID int64
	Amount int64
}

type apiQ7Result struct {
	ID    int64
	Value int64
}

type apiQ7ResultDecoder struct{ schema rasql.ResultSchema }

func (d apiQ7ResultDecoder) ResultSchema() rasql.ResultSchema { return d.schema }
func (apiQ7ResultDecoder) Presence() []rasql.Presence         { return nil }
func (d apiQ7ResultDecoder) DecodeRow(source rasql.ScanSource, row *apiQ7Result) error {
	return source.Scan(&row.ID, &row.Value)
}

func apiQ7ResultProjection(t *testing.T, id, value rasql.Expr[int64]) rasql.Projection[apiQ7Result] {
	t.Helper()

	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id, schema.IntegerType{}, ""),
		rasql.Item("value", value, schema.IntegerType{}, ""),
	}, apiQ7ResultDecoder{schema: resultSchema})
	require.NoError(t, err)
	return projection
}

func TestSQLiteCorrelatedProjectionConstructorsDecode(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	db, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(db, profile)
	require.NoError(t, err)

	users, err := rasql.TableOf[apiQ7User](schema.TableDef{
		Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	orders, err := rasql.TableOf[apiQ7Order](schema.TableDef{
		Name: "orders", Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "user_id", Type: schema.IntegerType{}},
			{Name: "amount", Type: schema.IntegerType{}},
		}, PrimaryKey: []string{"id"},
	})
	require.NoError(t, err)
	require.NoError(t, rasql.CreateTable(t.Context(), db, users))
	require.NoError(t, rasql.CreateTable(t.Context(), db, orders))

	usersSource, err := rasql.SourceOf(users, "")
	require.NoError(t, err)
	ordersSource, err := rasql.SourceOf(orders, "")
	require.NoError(t, err)
	usersID, err := rasql.BindColumn[apiQ7User, int64](usersSource, "id", "")
	require.NoError(t, err)
	ordersID, err := rasql.BindColumn[apiQ7Order, int64](ordersSource, "id", "")
	require.NoError(t, err)
	ordersUserID, err := rasql.BindColumn[apiQ7Order, int64](ordersSource, "user_id", "")
	require.NoError(t, err)
	ordersAmount, err := rasql.BindColumn[apiQ7Order, int64](ordersSource, "amount", "")
	require.NoError(t, err)

	for _, user := range []apiQ7User{{ID: 1}, {ID: 2}} {
		plan, err := rasql.NewCreatePlan(users, rasql.SetField(usersID, user.ID))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
	}
	for _, order := range []apiQ7Order{{ID: 1, UserID: 1, Amount: 1}, {ID: 2, UserID: 2, Amount: 2}} {
		plan, err := rasql.NewCreatePlan(orders,
			rasql.SetField(ordersID, order.ID),
			rasql.SetField(ordersUserID, order.UserID),
			rasql.SetField(ordersAmount, order.Amount),
		)
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
	}

	// One level: SELECT id, (SELECT amount FROM orders WHERE user_id = id)
	// AS value FROM users. A correlation that silently fell back to reading
	// every order in the table, instead of just this user's, would return
	// an arbitrary one of the two seeded amounts for both users instead of
	// each user's own.
	amountProjection, err := rasql.Scalar("amount", ordersAmount.Expr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	leafQuery := rasql.Select(ordersSource.Source(), amountProjection).
		Correlated(usersSource.Source()).
		Where(rasql.EqualExpr(ordersUserID.Expr(), usersID.Expr()))
	leafExpr, err := rasql.SubqueryExpr(leafQuery)
	require.NoError(t, err)

	oneLevel := rasql.Select(usersSource.Source(), apiQ7ResultProjection(t, usersID.Expr(), leafExpr)).
		OrderBy(rasql.AscExpr(usersID.Expr()))
	rows, err := rasql.All(t.Context(), executor, oneLevel)
	require.NoError(t, err)
	require.Equal(t, []apiQ7Result{{ID: 1, Value: 1}, {ID: 2, Value: 2}}, rows)

	// Two levels: the outer query's projected value is a second correlated
	// scalar subquery (middle) whose own projected value is the one-level
	// subquery built above (leaf). Both correlations reach back to the same
	// enclosing users row, so a correlation dropped at either level would
	// again surface as the wrong amount, or every order's amount, for a
	// user with more than one order.
	middleProjection, err := rasql.Scalar("value", leafExpr, schema.IntegerType{}, "")
	require.NoError(t, err)
	middleQuery := rasql.Select(ordersSource.Source(), middleProjection).
		Correlated(usersSource.Source()).
		Where(rasql.EqualExpr(ordersUserID.Expr(), usersID.Expr()))
	middleExpr, err := rasql.SubqueryExpr(middleQuery)
	require.NoError(t, err)

	twoLevel := rasql.Select(usersSource.Source(), apiQ7ResultProjection(t, usersID.Expr(), middleExpr)).
		OrderBy(rasql.AscExpr(usersID.Expr()))
	rows, err = rasql.All(t.Context(), executor, twoLevel)
	require.NoError(t, err)
	require.Equal(t, []apiQ7Result{{ID: 1, Value: 1}, {ID: 2, Value: 2}}, rows)
}
