package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_rasql_partial_update writes one column of several rows. A generated
// patch builder writes named columns by primary key or an explicit predicate;
// a statement built through the query package is what states an arbitrary
// bulk predicate like the one below.
func Example_rasql_partial_update() {
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
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for id, email := range map[int64]string{42: "old@example.com", 512: "keep@example.com"} {
		plan := store.NewUsersCreate().ID(id).Email(email).FirstName("First").LastName("Last").Plan()
		if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// SQL: UPDATE users SET email = ? WHERE users.id < ? (arguments: "ada@example.com", 100)
	statement, err := query.NewUpdate(users.Ref(), query.Set(users.Email().Ref(), "ada@example.com"))
	if err != nil {
		fmt.Printf("failed to build update: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.LessThan(users.ID().Ref(), 100))
	if err != nil {
		fmt.Printf("failed to filter update: %s\n", err)
		return
	}
	plan, err := rasql.NewStatementPlan(statement)
	if err != nil {
		fmt.Printf("failed to adapt update: %s\n", err)
		return
	}
	outcome, err := rasql.ExecMutation(ctx, executor, plan)
	if err != nil {
		fmt.Printf("failed to run update: %s\n", err)
		return
	}
	fmt.Printf("%d user updated\n", outcome.Affected)

	// The row outside the predicate keeps the email it was inserted with. A
	// dynamic SELECT reads it back through the query and render packages.
	// SQL: SELECT users.id, users.email FROM users WHERE users.id = ? (argument: 512)
	kept, err := query.NewSelect(users.Ref(), users.ID().Ref(), users.Email().Ref())
	if err != nil {
		fmt.Printf("failed to build select: %s\n", err)
		return
	}
	kept, err = kept.WithWhere(query.Equal(users.ID().Ref(), 512))
	if err != nil {
		fmt.Printf("failed to filter select: %s\n", err)
		return
	}
	rendered, err := render.Select(db.Dialect(), kept)
	if err != nil {
		fmt.Printf("failed to render select: %s\n", err)
		return
	}
	rows, err := db.QueryRendered(ctx, rendered)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		fmt.Println("failed to query user: no rows")
		return
	}
	var id int64
	var email string
	if err := rows.Scan(&id, &email); err != nil {
		fmt.Printf("failed to read user: %s\n", err)
		return
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("failed to read user: %s\n", err)
		return
	}
	fmt.Println(email)

	// Output:
	// 1 user updated
	// keep@example.com
}
