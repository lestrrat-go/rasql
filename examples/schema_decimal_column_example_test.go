package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// invoiceRow maps the one schema.TypeDecimal column this example declares.
// The column decodes into a Go string, so the exact digits inserted are the
// exact digits read back.
type invoiceRow struct {
	ID     int64  `rasql:"id"`
	Amount string `rasql:"amount"`
}

func Example_schema_decimal_column() {
	// This example declares a schema.TypeDecimal column, creates its table in
	// SQLite, and shows that the inserted string round-trips unchanged.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}

	// A TypeDecimal column must state Precision and Scale; Table.Validate
	// rejects a decimal column that omits either.
	invoices := rasql.MustTable[invoiceRow](schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: 4},
		},
		PrimaryKey: []string{"id"},
	})
	// SQLite has no exact decimal storage class, so the dialect declares this
	// column TEXT rather than NUMERIC(19,4), which would round through REAL.
	if err := rasql.Create(ctx, client, invoices); err != nil {
		fmt.Printf("failed to create invoices table: %s\n", err)
		return
	}

	if _, err := rasql.Insert(ctx, client, invoices, invoiceRow{ID: 1, Amount: "19.99"}); err != nil {
		fmt.Printf("failed to insert invoice: %s\n", err)
		return
	}

	invoiceID, err := invoices.Column("id")
	if err != nil {
		fmt.Printf("failed to reference id column: %s\n", err)
		return
	}
	invoice, err := rasql.SelectFrom(client, invoices).WhereEqual(invoiceID, int64(1)).One(ctx)
	if err != nil {
		fmt.Printf("failed to query invoices: %s\n", err)
		return
	}

	fmt.Println(invoice.Amount)

	// Output:
	// 19.99
}
