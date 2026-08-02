package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite"
)

func Example_migrate_apply() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	database.SetMaxOpenConns(1)

	users := schema.Table{
		Name: "users",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "email", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	}
	catalog, err := migrate.NewCatalog(users)
	if err != nil {
		fmt.Printf("failed to define migration catalog: %s\n", err)
		return
	}
	initial, err := catalog.InitialMigration("001_create_users")
	if err != nil {
		fmt.Printf("failed to plan initial migration: %s\n", err)
		return
	}
	runner, err := migrate.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create migration runner: %s\n", err)
		return
	}
	if err := runner.Apply(ctx, initial); err != nil {
		fmt.Printf("failed to apply migration: %s\n", err)
		return
	}

	var columns int
	if err := database.QueryRowContext(ctx, "SELECT COUNT(*) FROM pragma_table_info('users')").Scan(&columns); err != nil {
		fmt.Printf("failed to inspect migrated users table: %s\n", err)
		return
	}
	fmt.Printf("users table has %d columns\n", columns)

	// Output:
	// users table has 2 columns
}
