package examples_test

import (
	"fmt"

	"github.com/lestrrat-go/rasql/schema"
)

func Example_schema_table_definition() {
	// schema.Table declares database structure. NewTable validates the
	// descriptor and returns an independent copy for application configuration.
	table, err := schema.NewTable(usersTableDefinition())
	if err != nil {
		fmt.Printf("failed to define table: %s\n", err)
		return
	}

	fmt.Printf("%s: %d columns\n", table.Name, len(table.Columns))

	// Output:
	// users: 2 columns
}

func usersTableDefinition() schema.Table {
	return schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
}
