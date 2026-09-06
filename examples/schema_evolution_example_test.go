package examples_test

import (
	"context"
	"database/sql"
	"fmt"
	"path/filepath"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/lestrrat-go/rasql/sqltext"
	_ "modernc.org/sqlite"
)

func Example_schemaEvolution() {
	// BEGIN(schemaEvolution)
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer database.Close()
	if _, err := database.ExecContext(ctx, "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"); err != nil {
		fmt.Println(err)
		return
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO members (name) VALUES ('Ada')"); err != nil {
		fmt.Println(err)
		return
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer connection.Close()
	analyzer := sqlite.New()
	inspector, err := inspect.New(connection, dialect.SQLite())
	if err != nil {
		fmt.Println(err)
		return
	}
	table, err := inspector.Table(ctx, "members")
	if err != nil {
		fmt.Println(err)
		return
	}
	baselineSources, err := analyzer.LiveSources(table)
	if err != nil {
		fmt.Println(err)
		return
	}
	baseline, err := analyzer.Parse(baselineSources)
	if err != nil {
		fmt.Println(err)
		return
	}
	facts, err := sqlite.InspectLiveCatalog(ctx, connection, "members")
	if err != nil {
		fmt.Println(err)
		return
	}
	baseline, err = analyzer.AttachLiveCatalog(baseline, facts)
	if err != nil {
		fmt.Println(err)
		return
	}
	target, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: sqltext.Text("CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL DEFAULT '')")}})
	if err != nil {
		fmt.Println(err)
		return
	}
	plan, err := analyzer.Diff(baseline, target)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, operation := range plan.Operations {
		fmt.Printf("operation %s (%s): %s\n", operation.ID, operation.Kind, operation.Summary)
	}
	for _, decision := range plan.Decisions {
		fmt.Printf("decision %s (%s): %s\n", decision.ID, decision.Kind, decision.Reason)
	}
	fmt.Println("executable:", plan.Executable())
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE members SET email = name || '@example.test' WHERE email IS NULL;"})
	if err != nil {
		fmt.Println(err)
		return
	}
	root := filepath.Join(".tmp", "schema-evolution-example")
	if err := diff.WriteMigration(filepath.Join(root, "001_schema_evolution"), resolved); err != nil {
		fmt.Println(err)
		return
	}
	migrations, err := migrationdir.Load(root)
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, statement := range migrations[0].Statements {
		fmt.Println("up:", statement.Source)
	}
	for _, statement := range migrations[0].Down {
		fmt.Println("down:", statement.Source)
	}
	// END(schemaEvolution)
}
