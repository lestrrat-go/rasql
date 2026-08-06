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
// The column decodes into a Go string on every dialect. This example runs on
// SQLite, which stores such a column as TEXT and hands back the exact digits
// inserted; PostgreSQL and MySQL instead return the value in the column's
// declared scale, so the same "19.99" reads back as "19.9900" there.
type invoiceRow struct {
	ID     int64  `rasql:"id"`
	Amount string `rasql:"amount"`
}

func Example_schema_decimal_column() {
	// This example declares a schema.TypeDecimal column, creates its table in
	// SQLite, and shows that the inserted string round-trips unchanged there.
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
	// rejects a decimal column that omits either. Scale is stated through
	// schema.NewDecimalScale so that a scale of 0 is distinguishable from a
	// column that named no scale at all.
	invoices := rasql.MustTable[invoiceRow](schema.Table{
		Name: "invoices",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "amount", Type: schema.TypeDecimal, Precision: 19, Scale: schema.NewDecimalScale(4)},
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
	invoice, err := rasql.SelectFrom(invoices).WhereEqual(invoiceID, int64(1)).One(ctx, client)
	if err != nil {
		fmt.Printf("failed to query invoices: %s\n", err)
		return
	}

	fmt.Println(invoice.Amount)

	// Output:
	// 19.99
}
