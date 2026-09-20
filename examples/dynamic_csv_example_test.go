package examples_test

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/csv"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	_ "modernc.org/sqlite"
)

// Example_dynamicCSV solves the case where a query's columns are chosen at
// runtime, so no fixed Go row type can represent every possible result. It
// executes a rendered query through database/sql, reads the returned column
// names, and scans each row into a slice before writing CSV.
func Example_dynamicCSV() {
	ctx := context.Background()
	// Keep one SQLite connection because each connection gets a separate
	// in-memory database.
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	// rasql.Open pairs the database handle with the dialect that renders the
	// dynamic statement below.
	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create database: %s\n", err)
		return
	}
	if _, err := db.Exec(ctx, stmt.New(sqltext.Text("CREATE TABLE users (name TEXT, email TEXT)"))); err != nil {
		fmt.Printf("failed to create table: %s\n", err)
		return
	}
	if _, err := db.Exec(ctx, stmt.New(sqltext.Text("INSERT INTO users VALUES ('Ada', 'ada@example.com')"))); err != nil {
		fmt.Printf("failed to insert row: %s\n", err)
		return
	}
	// Describe the table at runtime because this path deliberately has no
	// generated table or row type.
	users, err := query.NewTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "name", Type: schema.TextType{}},
		{Name: "email", Type: schema.TextType{}},
	}})
	if err != nil {
		fmt.Printf("failed to define table: %s\n", err)
		return
	}
	// Alias each selected column because database/sql.Columns supplies these
	// names as the CSV header.
	statement, err := query.NewSelect(users, users.Column("name").As("display_name"), users.Column("email").As("contact"))
	if err != nil {
		fmt.Printf("failed to build query: %s\n", err)
		return
	}
	// Render separately from execution so QueryRendered can preserve the
	// runtime projection instead of decoding into a typed rasql.Query.
	rendered, err := render.Select(dialect.SQLite(), statement)
	if err != nil {
		fmt.Printf("failed to render query: %s\n", err)
		return
	}
	sqlRows, err := db.QueryRendered(ctx, rendered)
	if err != nil {
		fmt.Printf("failed to query rows: %s\n", err)
		return
	}
	defer func() { _ = sqlRows.Close() }()
	// The driver reports the final aliases, so the exported CSV follows the
	// query even when callers change its projection.
	header, err := sqlRows.Columns()
	if err != nil {
		fmt.Printf("failed to read header: %s\n", err)
		return
	}
	var output bytes.Buffer
	writer := csv.NewWriter(&output)
	if err := writer.Write(header); err != nil {
		fmt.Printf("failed to write header: %s\n", err)
		return
	}
	for sqlRows.Next() {
		// Scan through pointers to interface values because neither the number
		// nor the Go types of the selected columns are known at compile time.
		values := make([]any, len(header))
		destinations := make([]any, len(values))
		for index := range destinations {
			destinations[index] = &values[index]
		}
		if err := sqlRows.Scan(destinations...); err != nil {
			fmt.Printf("failed to read row: %s\n", err)
			return
		}
		// encoding/csv accepts strings, so convert every non-NULL driver value
		// after scanning and leave SQL NULL as an empty field.
		row := make([]string, len(header))
		for index, value := range values {
			if value != nil {
				row[index] = fmt.Sprint(value)
			}
		}
		if err := writer.Write(row); err != nil {
			fmt.Printf("failed to write row: %s\n", err)
			return
		}
	}
	if err := sqlRows.Err(); err != nil {
		fmt.Printf("failed to read rows: %s\n", err)
		return
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		fmt.Printf("failed to flush CSV: %s\n", err)
		return
	}
	fmt.Print(output.String())

	// Output:
	// display_name,contact
	// Ada,ada@example.com
}
