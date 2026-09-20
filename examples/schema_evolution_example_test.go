package examples_test

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/lestrrat-go/rasql/sqltext"
	_ "modernc.org/sqlite"
)

// Example_schemaEvolution solves a SQLite migration that adds a required
// column to a table which already contains rows. The analyzer compares the live
// table with the target DDL, pauses for application-specific backfill SQL, then
// writes an executable rebuild migration after that decision is resolved.
func Example_schemaEvolution() {
	// BEGIN(schemaEvolution)
	ctx := context.Background()
	// Use a file-backed database because the migration writer and live catalog
	// inspection need one stable database while the example creates temp files.
	if err := os.MkdirAll(".tmp", 0o700); err != nil {
		fmt.Println(err)
		return
	}
	root, err := os.MkdirTemp(".tmp", "schema-evolution-*")
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = os.RemoveAll(root) }()
	database, err := sql.Open("sqlite", filepath.Join(root, "schema.sqlite"))
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = database.Close() }()
	// Seed the old schema with a row so the new NOT NULL email column cannot be
	// added correctly until the application supplies a value for existing data.
	if _, err := database.ExecContext(ctx, "CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"); err != nil {
		fmt.Println(err)
		return
	}
	if _, err := database.ExecContext(ctx, "INSERT INTO members (name) VALUES ('Ada')"); err != nil {
		fmt.Println(err)
		return
	}
	// Retain one connection because inspection reads SQLite connection-local
	// catalog state throughout the baseline build.
	connection, err := database.Conn(ctx)
	if err != nil {
		fmt.Println(err)
		return
	}
	defer func() { _ = connection.Close() }()
	analyzer := sqlite.New()
	inspector, err := inspect.New(connection, dialect.SQLite())
	if err != nil {
		fmt.Println(err)
		return
	}
	// Convert the inspected table into parser input so the live and target
	// schemas pass through the same analyzer representation.
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
	// Attach catalog facts that CREATE TABLE text alone cannot preserve, such
	// as SQLite details needed to plan a faithful rebuild.
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
	// The target adds email as required. Existing rows make its value an
	// application decision rather than something the analyzer can invent.
	target, err := analyzer.Parse([]diff.Source{{Path: "schema.sql", SQL: sqltext.Text("CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL, email TEXT NOT NULL)")}})
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
	if len(plan.Decisions) != 1 {
		fmt.Printf("expected one decision, got %d\n", len(plan.Decisions))
		return
	}
	// Resolve the reported decision with reviewed SQL that fills every existing
	// row before the rebuild makes email NOT NULL.
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE members SET email = name || '@example.test' WHERE email IS NULL;"})
	if err != nil {
		fmt.Println(err)
		return
	}
	// Only the resolved plan is executable, so write it and load it through the
	// same migration reader an application uses.
	if err := diff.WriteMigration(filepath.Join(root, "migrations", "001_schema_evolution"), resolved); err != nil {
		fmt.Println(err)
		return
	}
	migrations, err := migrationdir.Load(filepath.Join(root, "migrations"))
	if err != nil {
		fmt.Println(err)
		return
	}
	for _, statement := range migrations[0].Statements {
		fmt.Printf("up: %s %q\n", statement.Source, statement.SQL)
	}
	for _, statement := range migrations[0].Down {
		fmt.Printf("down: %s %q\n", statement.Source, statement.SQL)
	}
	// END(schemaEvolution)

	// Output:
	// operation rebuild_table_sqlite_members (rebuild_table): rebuild table members
	// decision backfill_sqlite_members_email (backfill): required column needs an application-specific backfill
	// executable: false
	// up: 001_0_stage_email.up.sql "ALTER TABLE members ADD COLUMN email text;\n"
	// up: 002_1_backfill_email.up.sql "UPDATE members SET email = name || '@example.test' WHERE email IS NULL;\n"
	// up: 003_2_create_rebuild.up.sql "CREATE TABLE members__rasql_rebuild (id integer PRIMARY KEY, name text NOT NULL, email text NOT NULL);\n\n"
	// up: 004_3_copy_rebuild.up.sql "INSERT INTO members__rasql_rebuild (email, id, name) SELECT email, \"id\", \"name\" FROM members;\n"
	// up: 005_4_drop_table.up.sql "DROP TABLE members;\n"
	// up: 006_5_rename_rebuild.up.sql "ALTER TABLE members__rasql_rebuild RENAME TO members;\n"
	// down: 006_5_rename_rebuild.down.sql "CREATE TABLE \"main\".members__rasql_rebuild_2 (\"id\" integer NOT NULL, \"name\" text NOT NULL, PRIMARY KEY (\"id\"));\n\n"
	// down: 005_4_drop_table.down.sql "INSERT INTO \"main\".members__rasql_rebuild_2 (\"id\", \"name\") SELECT id, name FROM members;\n"
	// down: 004_3_copy_rebuild.down.sql "DROP TABLE members;\n"
	// down: 003_2_create_rebuild.down.sql "ALTER TABLE \"main\".members__rasql_rebuild_2 RENAME TO \"members\";\n"
	// down: 002_1_backfill_email.down.sql "-- backfill is removed by the structural rebuild\n"
	// down: 001_0_stage_email.down.sql "-- staging column is removed by the structural rebuild\n"
}
