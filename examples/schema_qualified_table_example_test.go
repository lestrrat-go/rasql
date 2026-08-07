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

// eventRow maps the qualified "audit.events" table this example queries.
type eventRow struct {
	ID     int64  `rasql:"id"`
	Action string `rasql:"action"`
}

func Example_schema_qualified_table() {
	// This example creates and queries a table through a schema-qualified
	// descriptor. Schema names a PostgreSQL schema, a MySQL database, or, as
	// here, a SQLite attached-database name. rasql never creates the
	// namespace itself, so the ATTACH DATABASE below stands in for a
	// reviewed native migration, which is the only way rasql creates a
	// namespace in production; rasql.Create then renders CREATE TABLE
	// "audit"."events" into the namespace that migration already created.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	if _, err := database.ExecContext(ctx, `ATTACH DATABASE ':memory:' AS audit`); err != nil {
		fmt.Printf("failed to attach audit database: %s\n", err)
		return
	}

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}

	// Schema qualifies the table without changing how any other field works.
	events := rasql.MustTable[eventRow](schema.Table{
		Schema: "audit",
		Name:   "events",
		Columns: []schema.Column{
			{Name: "id", Type: schema.TypeInteger},
			{Name: "action", Type: schema.TypeText},
		},
		PrimaryKey: []string{"id"},
	})

	if err := rasql.Create(ctx, client, events); err != nil {
		fmt.Printf("failed to create events table: %s\n", err)
		return
	}

	if _, err := rasql.Insert(ctx, client, events, eventRow{ID: 1, Action: "created"}); err != nil {
		fmt.Printf("failed to insert event: %s\n", err)
		return
	}

	eventID, err := events.Column("id")
	if err != nil {
		fmt.Printf("failed to reference id column: %s\n", err)
		return
	}
	event, err := rasql.SelectFrom(events).WhereEqual(eventID, int64(1)).One(ctx, client)
	if err != nil {
		fmt.Printf("failed to query events: %s\n", err)
		return
	}

	// QualifiedName is for display only, never a SQL identifier: the renderer
	// quotes Schema and Name as two separate identifiers.
	fmt.Printf("%s: %s\n", events.QueryTable().QualifiedName(), event.Action)

	// Output:
	// audit.events: created
}
