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

func Example_rasql_group_by() {
	// This example counts tasks per status and keeps only the statuses with
	// more than one task, using GROUP BY and HAVING together.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}

	// A typed descriptor makes tasks usable with rasql.Insert.
	type taskRow struct {
		ID     int    `rasql:"id"`
		Status string `rasql:"status"`
	}
	// A local result type holds one row per group.
	type statusCount struct {
		Status string
		Total  int64
	}
	tasks := rasql.MustTableOf[taskRow](schema.MustTableDef("tasks",
		schema.Integer("id"),
		schema.Text("status"),
		schema.PrimaryKey("id"),
	))
	if err := rasql.CreateTable(ctx, db, tasks); err != nil {
		fmt.Printf("failed to create tasks table: %s\n", err)
		return
	}
	for _, task := range []taskRow{
		{ID: 1, Status: "open"},
		{ID: 2, Status: "open"},
		{ID: 3, Status: "done"},
		{ID: 4, Status: "done"},
		{ID: 5, Status: "done"},
	} {
		if _, err := rasql.Insert(ctx, db, tasks, task); err != nil {
			fmt.Printf("failed to insert task: %s\n", err)
			return
		}
	}

	// tasks has no generated column accessor for status, so it is looked up
	// by name. That lookup validates it against the descriptor as the query is
	// assembled.
	status := tasks.Column("status")

	// GroupBy adds the GROUP BY clause the mixed projection below needs: a
	// bare column beside COUNT(*) is refused without one. Having filters
	// groups after aggregation, so it may call an aggregate a WHERE clause
	// could not.
	// SQL: SELECT tasks.status, COUNT(*) AS total FROM tasks GROUP BY tasks.status HAVING COUNT(*) > ? ORDER BY tasks.status (argument: 1)
	rows, err := rasql.DecodeFrom[statusCount](tasks).
		Project(status, query.CountAll().As("total")).
		GroupBy(status).
		Having(query.GreaterThan(query.CountAll(), 1)).
		Order(query.Asc(status)).
		Query(ctx, db)
	if err != nil {
		fmt.Printf("failed to query status counts: %s\n", err)
		return
	}
	for found, err := range rows {
		if err != nil {
			fmt.Printf("failed to query status counts: %s\n", err)
			return
		}
		fmt.Println(found.Status, found.Total)
	}

	// Output:
	// done 3
	// open 2
}
