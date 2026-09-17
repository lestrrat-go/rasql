package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// preparedParamOrdersDecoder decodes every column of the orders table into a
// store.OrdersRow, reusing the generated ScanRow method rather than restating
// the column order.
type preparedParamOrdersDecoder struct{ result rasql.ResultSchema }

func (d preparedParamOrdersDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d preparedParamOrdersDecoder) Presence() []rasql.Presence       { return nil }
func (d preparedParamOrdersDecoder) DecodeRow(src rasql.ScanSource, row *store.OrdersRow) error {
	return row.ScanRow(src)
}

// preparedParamOrdersQuery builds a Query[store.OrdersRow] whose WHERE clause
// carries a Parameter instead of a bound value, so one Prepare serves every
// minimum total a caller asks for.
func preparedParamOrdersQuery() (rasql.Query[store.OrdersRow], rasql.Parameter[int64], error) {
	orders := store.Orders()
	def := store.OrdersDef()
	id, err := rasql.BindColumn[store.OrdersRow, int64](orders, orders.IDRef().Name(), "")
	if err != nil {
		return rasql.Query[store.OrdersRow]{}, rasql.Parameter[int64]{}, err
	}
	userID, err := rasql.BindColumn[store.OrdersRow, int64](orders, orders.UserIDRef().Name(), "")
	if err != nil {
		return rasql.Query[store.OrdersRow]{}, rasql.Parameter[int64]{}, err
	}
	total, err := rasql.BindColumn[store.OrdersRow, int64](orders, orders.TotalRef().Name(), "")
	if err != nil {
		return rasql.Query[store.OrdersRow]{}, rasql.Parameter[int64]{}, err
	}
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: orders.IDRef().Name(), Type: def.Columns[0].Type},
		rasql.ResultColumn{Name: orders.UserIDRef().Name(), Type: def.Columns[1].Type},
		rasql.ResultColumn{Name: orders.TotalRef().Name(), Type: def.Columns[2].Type},
	)
	if err != nil {
		return rasql.Query[store.OrdersRow]{}, rasql.Parameter[int64]{}, err
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item(orders.IDRef().Name(), id.Expr(), def.Columns[0].Type, ""),
		rasql.Item(orders.UserIDRef().Name(), userID.Expr(), def.Columns[1].Type, ""),
		rasql.Item(orders.TotalRef().Name(), total.Expr(), def.Columns[2].Type, ""),
	}, preparedParamOrdersDecoder{result: result})
	if err != nil {
		return rasql.Query[store.OrdersRow]{}, rasql.Parameter[int64]{}, err
	}
	minTotal := rasql.NewParameter[int64]()
	query := rasql.Select(orders, projection).
		Where(rasql.GreaterOrEqualExpr(total.Expr(), minTotal.Expr())).
		OrderBy(rasql.AscExpr(id.Expr()))
	return query, minTotal, nil
}

// Example_rasql_prepared_parameter prepares one query with a Parameter in
// place of a bound value, and runs it for two different minimum totals.
func Example_rasql_prepared_parameter() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	orders := store.Orders()
	if err := rasql.CreateTable(ctx, db, orders); err != nil {
		fmt.Printf("failed to create orders table: %s\n", err)
		return
	}
	for _, order := range []store.OrdersRow{
		{ID: 1, UserID: 1, Total: 10},
		{ID: 2, UserID: 1, Total: 50},
		{ID: 3, UserID: 2, Total: 90},
	} {
		plan := store.NewOrdersCreate().ID(order.ID).UserID(order.UserID).Total(order.Total)
		if _, err := rasql.ExecMutation(ctx, db, plan.Plan()); err != nil {
			fmt.Printf("failed to insert order: %s\n", err)
			return
		}
	}

	// BEGIN(prepared_parameter)

	query, minTotal, err := preparedParamOrdersQuery()
	if err != nil {
		fmt.Printf("failed to build orders query: %s\n", err)
		return
	}
	// Prepare validates, lowers, renders and resolves codecs once. minTotal
	// still has no value, so running prepared as it stands would report
	// parameter_unbound.
	prepared, err := rasql.Prepare(db, query)
	if err != nil {
		fmt.Printf("failed to prepare query: %s\n", err)
		return
	}

	// Bind returns a new Prepared and leaves prepared itself unbound, so the
	// same prepared form serves both minimum totals instead of preparing the
	// query twice.
	atLeast20, err := prepared.Bind(minTotal.Value(int64(20)))
	if err != nil {
		fmt.Printf("failed to bind minimum total: %s\n", err)
		return
	}
	atLeast80, err := prepared.Bind(minTotal.Value(int64(80)))
	if err != nil {
		fmt.Printf("failed to bind minimum total: %s\n", err)
		return
	}

	// END(prepared_parameter)

	smallOrders, err := atLeast20.All(ctx, db)
	if err != nil {
		fmt.Printf("failed to query orders: %s\n", err)
		return
	}
	for _, order := range smallOrders {
		fmt.Println(order.ID, order.Total)
	}
	largeOrders, err := atLeast80.All(ctx, db)
	if err != nil {
		fmt.Printf("failed to query orders: %s\n", err)
		return
	}
	for _, order := range largeOrders {
		fmt.Println(order.ID, order.Total)
	}

	// Output:
	// 2 50
	// 3 90
	// 3 90
}
