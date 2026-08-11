package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	querytemplate "github.com/lestrrat-go/rasql/template"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

type rankedUser struct {
	ID    int64  `rasql:"id"`
	Email string `rasql:"email"`
	Rank  int64  `rasql:"rank"`
}

func Example_rasql_typed_static_template() {
	// A static template keeps complex SQL readable while QueryRenderedAll maps
	// its result into a normal Go type.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for _, user := range []UserRow{
		{ID: 1, Email: "ada@example.com"},
		{ID: 2, Email: "bob@example.com"},
		{ID: 3, Email: "cyd@example.com"},
	} {
		if _, err := rasql.Insert(ctx, client, users, user); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	parsed, err := querytemplate.Parse("ranked_users", `WITH ranked_users AS (
		SELECT id, email, ROW_NUMBER() OVER (ORDER BY id) AS rank
		FROM users
	)
	SELECT id, email, rank FROM ranked_users WHERE id >= {{bind "minimum_id"}} ORDER BY rank`)
	if err != nil {
		fmt.Printf("failed to parse template: %s\n", err)
		return
	}
	compiled, err := parsed.Compile(dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to compile template: %s\n", err)
		return
	}
	statement, err := compiled.Bind(map[string]any{"minimum_id": 2})
	if err != nil {
		fmt.Printf("failed to bind template: %s\n", err)
		return
	}
	// SQL: WITH ranked_users AS (SELECT id, email, ROW_NUMBER() OVER (ORDER BY id) AS rank FROM users) SELECT id, email, rank FROM ranked_users WHERE id >= ? ORDER BY rank (argument: 2)
	rows, err := rasql.QueryRenderedAll[rankedUser](ctx, client, statement)
	if err != nil {
		fmt.Printf("failed to query ranked users: %s\n", err)
		return
	}
	for _, user := range rows {
		fmt.Println(user.Rank, user.Email)
	}

	// Output:
	// 2 bob@example.com
	// 3 cyd@example.com
}
