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

func Example_dynamicCSV() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create database: %s\n", err)
		return
	}
	if _, err := db.ExecRendered(ctx, stmt.New(sqltext.Text("CREATE TABLE users (name TEXT, email TEXT)"))); err != nil {
		fmt.Printf("failed to create table: %s\n", err)
		return
	}
	if _, err := db.ExecRendered(ctx, stmt.New(sqltext.Text("INSERT INTO users VALUES ('Ada', 'ada@example.com')"))); err != nil {
		fmt.Printf("failed to insert row: %s\n", err)
		return
	}
	users, err := query.NewTableRef(schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "name", Type: schema.TextType{}},
		{Name: "email", Type: schema.TextType{}},
	}})
	if err != nil {
		fmt.Printf("failed to define table: %s\n", err)
		return
	}
	statement, err := query.NewSelect(users, users.Column("name").As("display_name"), users.Column("email").As("contact"))
	if err != nil {
		fmt.Printf("failed to build query: %s\n", err)
		return
	}
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
		values := make([]any, len(header))
		destinations := make([]any, len(values))
		for index := range destinations {
			destinations[index] = &values[index]
		}
		if err := sqlRows.Scan(destinations...); err != nil {
			fmt.Printf("failed to read row: %s\n", err)
			return
		}
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
