package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// EventRow maps the qualified "audit.events" table this example queries.
type EventRow struct {
	ID     int64  `rasql:"id"`
	Action string `rasql:"action"`
}

// EventsTable has the shape rasqlgen emits: the typed table plus one accessor
// method per column. The descriptor itself stays inside the example below,
// because how the table is qualified is what this example teaches.
type EventsTable struct {
	rasql.Table[EventRow]
}

func (t EventsTable) ID() query.ColumnRef     { return rasql.ColumnOf(t.Table, "id") }
func (t EventsTable) Action() query.ColumnRef { return rasql.ColumnOf(t.Table, "action") }

// eventDecoder decodes an EventRow from its two columns, in projection order.
type eventDecoder struct{ result rasql.ResultSchema }

func (d eventDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d eventDecoder) Presence() []rasql.Presence       { return nil }
func (d eventDecoder) DecodeRow(src rasql.ScanSource, row *EventRow) error {
	return src.Scan(&row.ID, &row.Action)
}

func Example_schema_qualified_table() {
	// This example creates and queries a table through a schema-qualified
	// descriptor. Schema names a PostgreSQL schema, a MySQL database, or, as
	// here, a SQLite attached-database name. rasql never creates the
	// namespace itself, so the ATTACH DATABASE below stands in for a
	// reviewed native migration, which is the only way rasql creates a
	// namespace in production; rasql.CreateTable then renders CREATE TABLE
	// "audit"."events" into the namespace that migration already created.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	if _, err := database.ExecContext(ctx, `ATTACH DATABASE ':memory:' AS audit`); err != nil {
		fmt.Printf("failed to attach audit database: %s\n", err)
		return
	}

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 40, 0)
	if err != nil {
		fmt.Printf("failed to describe engine profile: %s\n", err)
		return
	}
	executor, err := rasql.AsExecutor(db, profile)
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}

	// InSchema qualifies the table without changing how any other option works.
	events := EventsTable{rasql.MustTableOf[EventRow](schema.MustTableDef("events",
		schema.InSchema("audit"),
		schema.Integer("id"),
		schema.Text("action"),
		schema.PrimaryKey("id"),
	))}

	// SQL: CREATE TABLE audit.events (id INTEGER NOT NULL, action TEXT NOT NULL, PRIMARY KEY (id))
	if err := rasql.CreateTable(ctx, db, events); err != nil {
		fmt.Printf("failed to create events table: %s\n", err)
		return
	}

	source, err := rasql.SourceOf(events, "")
	if err != nil {
		fmt.Printf("failed to bind events source: %s\n", err)
		return
	}
	id, err := rasql.BindColumn[EventRow, int64](source, "id", "")
	if err != nil {
		fmt.Printf("failed to bind id column: %s\n", err)
		return
	}
	action, err := rasql.BindColumn[EventRow, string](source, "action", "")
	if err != nil {
		fmt.Printf("failed to bind action column: %s\n", err)
		return
	}

	// SQL: INSERT INTO audit.events (id, action) VALUES (?, ?) (arguments: 1, "created")
	createPlan, err := rasql.NewCreatePlan[EventRow](events,
		rasql.SetField[EventRow](id, int64(1)),
		rasql.SetField[EventRow](action, "created"),
	)
	if err != nil {
		fmt.Printf("failed to build create plan: %s\n", err)
		return
	}
	if _, err := rasql.ExecMutation(ctx, executor, createPlan); err != nil {
		fmt.Printf("failed to insert event: %s\n", err)
		return
	}

	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}},
		rasql.ResultColumn{Name: "action", Type: schema.TextType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("id", id.Expr(), schema.IntegerType{}, ""),
		rasql.Item("action", action.Expr(), schema.TextType{}, ""),
	}, eventDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// SQL: SELECT audit.events.id, audit.events.action FROM audit.events WHERE audit.events.id = ? (argument: 1)
	event, err := rasql.One(ctx, executor,
		rasql.Select(source.Source(), projection).Where(rasql.EqualValue(id.Expr(), int64(1))))
	if err != nil {
		fmt.Printf("failed to query events: %s\n", err)
		return
	}

	// QualifiedName is for display only, never a SQL identifier: the renderer
	// quotes Schema and Name as two separate identifiers.
	fmt.Printf("%s: %s\n", events.Ref().QualifiedName(), event.Action)

	// Output:
	// audit.events: created
}
