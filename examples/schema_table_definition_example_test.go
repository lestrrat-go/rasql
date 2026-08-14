package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/schema"
)

func Example_schema_table_definition() {
	// This example defines two reusable table descriptors in Go code, built
	// with schema.MustTableDef. A column constructor such as schema.Integer and
	// a constraint constructor such as schema.PrimaryKey each return a
	// schema.TableOption, so they may appear in any order: PrimaryKey names
	// "id" below before Integer declares it, and the assembled descriptor is
	// the same either way. The same descriptor can later supply a reusable
	// query.TableRef or generate DDL.
	//
	// RowNamed states the Go row type rasqlgen generates for the table: here
	// it makes the row type User instead of the default UsersRow, so calling
	// code reads store.User rather than store.UsersRow. Like RelationshipNamed
	// below, it is a code-generation hint only — rasqlgen reads it, but
	// nothing else in rasql does, and it never appears in rendered SQL.
	users := schema.MustTableDef("users",
		schema.Integer("id"),
		schema.Text("email"),
		schema.Text("nickname", schema.Nullable()),
		schema.Time("created_at", schema.Default("CURRENT_TIMESTAMP")),
		schema.Decimal("balance", 19, 4),
		schema.PrimaryKey("id"),
		schema.Unique("email"),
		schema.Index("users_email_idx", "email"),
		schema.Check("balance >= 0"),
		schema.RowNamed("User"),
	)

	// A foreign key's Named, References, and OnDelete options configure the
	// constraint itself. RelationshipNamed additionally derives the belongs-to
	// schema.RelationshipDef that rasqlgen would otherwise name on its own
	// from the local column, letting the generated method read
	// orders.Buyer() rather than orders.Customer().
	orders := schema.MustTableDef("orders",
		schema.Integer("id"),
		schema.Integer("customer_id"),
		schema.PrimaryKey("id"),
		schema.ForeignKey("customer_id",
			schema.Named("orders_customer_fkey"),
			schema.References("customers", "id"),
			schema.OnDelete(schema.Cascade),
			schema.RelationshipNamed("buyer")),
	)

	fmt.Printf("%s: %d columns, primary key %v, row type %s\n", users.Name, len(users.Columns), users.PrimaryKey, users.RowName)
	fmt.Printf("%s: foreign key %s references %s, relationship %q\n",
		orders.Name, orders.ForeignKeys[0].Name, orders.ForeignKeys[0].ReferencedTable, orders.Relationships[0].Name)

	// Output:
	// users: 5 columns, primary key [id], row type User
	// orders: foreign key orders_customer_fkey references customers, relationship "buyer"
}
