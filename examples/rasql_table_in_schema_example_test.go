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

func Example_rasql_table_in_schema() {
	// This example moves a generated table to a second namespace at run
	// time, through the generated wrapper's own InSchema method, and shows
	// that a row written through the moved wrapper lands there rather than
	// in the namespace the connection is already sitting in.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// InSchema names a PostgreSQL schema, a MySQL database, or, as here, a
	// SQLite attached-database name. rasql never creates the namespace
	// itself, so the ATTACH DATABASE below stands in for a reviewed native
	// migration, which is the only way rasql creates a namespace in
	// production.
	if _, err := database.ExecContext(ctx, `ATTACH DATABASE ':memory:' AS tenant`); err != nil {
		fmt.Printf("failed to attach the tenant database: %s\n", err)
		return
	}

	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}

	home := store.Users()
	if err := rasql.CreateTable(ctx, db, home); err != nil {
		fmt.Printf("failed to create the home users table: %s\n", err)
		return
	}

	// InSchema returns a copy of the generated wrapper whose handle names
	// the moved table, so the copy's own Create, Table and Ref all reach the
	// tenant namespace from here on; home is untouched and still names the
	// table where the connection is already sitting.
	tenant, err := home.InSchema("tenant")
	if err != nil {
		fmt.Printf("failed to move the table into the tenant namespace: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, tenant); err != nil {
		fmt.Printf("failed to create the tenant users table: %s\n", err)
		return
	}

	plan, err := tenant.Create().ID(1).Email("ada@example.com").FirstName("Ada").LastName("Lovelace").Plan()
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	if _, err := rasql.Exec(ctx, db, plan); err != nil {
		fmt.Printf("failed to insert into the tenant table: %s\n", err)
		return
	}

	tenantColumns, err := (store.UsersColumns{}).Bind(tenant)
	if err != nil {
		fmt.Printf("failed to bind tenant columns: %s\n", err)
		return
	}
	tenantProjection, err := store.UsersProjection(tenantColumns)
	if err != nil {
		fmt.Printf("failed to build the tenant projection: %s\n", err)
		return
	}
	tenantRow, err := rasql.One(ctx, db,
		rasql.Select(tenant, tenantProjection).Where(rasql.EqualValue(tenantColumns.ID.Expr(), int64(1))))
	if err != nil {
		fmt.Printf("failed to query the tenant table: %s\n", err)
		return
	}
	fmt.Println(tenantRow.Email)

	// The point of the whole example: the identically named table where the
	// connection is sitting never saw the insert the moved plan built.
	homeColumns, err := (store.UsersColumns{}).Bind(home)
	if err != nil {
		fmt.Printf("failed to bind home columns: %s\n", err)
		return
	}
	homeProjection, err := store.UsersProjection(homeColumns)
	if err != nil {
		fmt.Printf("failed to build the home projection: %s\n", err)
		return
	}
	homeRows, err := rasql.All(ctx, db, rasql.Select(home, homeProjection))
	if err != nil {
		fmt.Printf("failed to query the home table: %s\n", err)
		return
	}
	fmt.Println(len(homeRows))

	// Output:
	// ada@example.com
	// 0
}
