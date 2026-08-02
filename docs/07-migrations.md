# Migrations

`migrate` applies ordered, forward-only DDL migrations and records each completed migration with a SHA-256 checksum. It supports PostgreSQL, MySQL, and SQLite. PostgreSQL and SQLite apply each migration atomically. MySQL DDL may commit before a migration record is written, so resolve any failed partial migration before retrying.

## Create an initial migration

Build a complete `migrate.Catalog` from table descriptors, then create one initial migration. The catalog validates cross-table foreign keys and creates referenced tables before dependent tables. It rejects multi-table foreign-key cycles because those require a later `ADD CONSTRAINT` operation.

<!-- INCLUDE(examples/migrate_apply_example_test.go) -->
```go
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
```
source: [examples/migrate_apply_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/migrate_apply_example_test.go)
<!-- END INCLUDE -->

Supply the complete migration set to `Runner.Apply`. It accepts the set in any order and runs migrations in lexicographic ID order. It rejects duplicate IDs, missing recorded migrations, skipped migrations, and an already-applied migration whose rendered DDL no longer matches its checksum.

## Supported operations

Each `migrate.Migration` holds structured operations. `CreateTable` creates a complete table and its declared indexes. `AddColumn` accepts only a nullable column or one with a default, so existing rows remain valid. `CreateIndex` and `DropIndex` manage an existing table's indexes.

Defaults and check constraints remain explicit SQL expressions from `schema.Table`. Supply only application-owned expressions, because DDL cannot bind an expression as a query value.

Column alterations, table and column renames, constraint changes, destructive table drops, SQLite table rebuilds, and automatic live-schema repair are not yet supported. `inspect` can still compare supported metadata outside the migration runner.
