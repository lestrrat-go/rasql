package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// statusCount is one row per group: a task status and how many tasks share
// it. No table has a total column, so store.TasksRow could not receive this
// result at all.
type statusCount struct {
	Status string
	Total  int64
}

// statusCountDecoder decodes a statusCount from the status column and a
// COUNT(*) result, in projection order.
type statusCountDecoder struct{ result rasql.ResultSchema }

func (d statusCountDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (d statusCountDecoder) Presence() []rasql.Presence       { return nil }
func (d statusCountDecoder) DecodeRow(src rasql.ScanSource, row *statusCount) error {
	return src.Scan(&row.Status, &row.Total)
}

// Example_rasql_group_by counts tasks per status and keeps only the statuses
// with more than one task, using GroupBy and Having together.
func Example_rasql_group_by() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

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
	tasks := store.Tasks()
	if err := rasql.CreateTable(ctx, db, tasks); err != nil {
		fmt.Printf("failed to create tasks table: %s\n", err)
		return
	}
	for _, task := range []store.TasksRow{
		{ID: 1, Status: "open"},
		{ID: 2, Status: "open"},
		{ID: 3, Status: "done"},
		{ID: 4, Status: "done"},
		{ID: 5, Status: "done"},
	} {
		plan := store.NewTasksCreate().ID(task.ID).Status(task.Status).Plan()
		if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
			fmt.Printf("failed to insert task: %s\n", err)
			return
		}
	}

	source, err := rasql.SourceOf(tasks, "")
	if err != nil {
		fmt.Printf("failed to bind tasks source: %s\n", err)
		return
	}
	status, err := rasql.BindColumn[store.TasksRow, string](source, tasks.StatusRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind status column: %s\n", err)
		return
	}
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "status", Type: schema.TextType{}},
		rasql.ResultColumn{Name: "total", Type: schema.IntegerType{}},
	)
	if err != nil {
		fmt.Printf("failed to build result schema: %s\n", err)
		return
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.Item("status", status.Expr(), schema.TextType{}, ""),
		rasql.Item("total", rasql.CountRows(), schema.IntegerType{}, ""),
	}, statusCountDecoder{result: result})
	if err != nil {
		fmt.Printf("failed to build projection: %s\n", err)
		return
	}

	// GroupBy adds the GROUP BY clause the mixed projection needs: a bare
	// column beside COUNT(*) is refused without one. Having filters groups
	// after aggregation, so it may call an aggregate a WHERE clause could not.
	// SQL: SELECT tasks.status, COUNT(*) AS total FROM tasks GROUP BY tasks.status HAVING COUNT(*) > ? ORDER BY tasks.status (argument: 1)
	q := rasql.Select(source.Source(), projection).
		GroupBy(rasql.Group(status.Expr())).
		Having(rasql.GreaterValue(rasql.CountRows(), int64(1))).
		OrderBy(rasql.AscExpr(status.Expr()))
	rows, err := rasql.All(ctx, executor, q)
	if err != nil {
		fmt.Printf("failed to query status counts: %s\n", err)
		return
	}
	for _, found := range rows {
		fmt.Println(found.Status, found.Total)
	}

	// Output:
	// done 3
	// open 2
}
