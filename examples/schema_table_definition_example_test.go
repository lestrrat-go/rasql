package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/schema"
)

func Example_schema_table_definition() {
	// This example defines two reusable table descriptors in Go code, built
	// with schema.MustTable. A column constructor such as schema.Integer and
	// a constraint constructor such as schema.PrimaryKey each return a
	// schema.TableOption, so they may appear in any order: PrimaryKey names
	// "id" below before Integer declares it, and the assembled descriptor is
	// the same either way. The same descriptor can later supply a reusable
	// query.Table or generate DDL.
	users := schema.MustTable("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.Text("nickname", schema.Nullable()),
		schema.Time("created_at", schema.Default("CURRENT_TIMESTAMP")),
		schema.Decimal("balance", 19, 4),
		schema.PrimaryKey("id"),
		schema.Unique("email"),
		schema.Index("users_email_idx", "email"),
		schema.Check("balance >= 0"),
	)

	// A foreign key's Named, References, and OnDelete options configure the
	// constraint itself. As additionally derives the belongs-to
	// schema.RelationshipDef that rasqlgen would otherwise name on its own
	// from the local column, letting the generated method read
	// orders.Buyer() rather than orders.Customer().
	orders := schema.MustTable("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id",
			schema.Named("orders_customer_fkey"),
			schema.References("customers", "id"),
			schema.OnDelete(schema.Cascade),
			schema.As("buyer")),
	)

	fmt.Printf("%s: %d columns, primary key %v\n", users.Name, len(users.Columns), users.PrimaryKey)
	fmt.Printf("%s: foreign key %s references %s, relationship %q\n",
		orders.Name, orders.ForeignKeys[0].Name, orders.ForeignKeys[0].ReferencedTable, orders.Relationships[0].Name)

	// Output:
	// users: 5 columns, primary key [id]
	// orders: foreign key orders_customer_fkey references customers, relationship "buyer"
}
