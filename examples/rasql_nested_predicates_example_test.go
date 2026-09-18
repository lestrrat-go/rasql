package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// Example_rasql_nested_predicates builds a predicate tree several levels deep
// and shows the SQL it renders, which is what a filter that mixes AND and OR
// needs. rasql.And and rasql.Or take predicates and return one, so either
// holds the other to any depth.
func Example_rasql_nested_predicates() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	db, err := rasql.Open(ctx, database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create executor: %s\n", err)
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for _, user := range []store.UsersRow{
		{ID: 5, Email: "ada@example.com"},
		{ID: 7, Email: "linus@other.org"},
		{ID: 15, Email: "grace@example.com"},
		{ID: 25, Email: "alan@example.com", Nickname: rasql.Nullable[string]{Value: "Alan", Valid: true}},
		{ID: 30, Email: "extra@example.com"},
	} {
		create := store.Users().Create().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last")
		if user.Nickname.Valid {
			create = create.Nickname(user.Nickname.Value)
		}
		plan, err := create.Plan()
		if err != nil {
			fmt.Printf("failed to build insert: %s\n", err)
			return
		}
		if _, err := rasql.Exec(ctx, db, plan); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// Bind binds every users column to the table, and UsersProjection selects
	// them in the order the generated row type scans them.
	columns, err := (store.UsersColumns{}).Bind(users)
	if err != nil {
		fmt.Printf("failed to bind users columns: %s\n", err)
		return
	}
	projection, err := store.UsersProjection(columns)
	if err != nil {
		fmt.Printf("failed to build users projection: %s\n", err)
		return
	}
	base := rasql.Select(users, projection)

	// The inner And sits inside an Or, which sits inside the outer And this
	// Where call adds, and the whole tree is one predicate. Row 30 has id > 20
	// but no nickname, so it shows the innermost And is not vacuous.
	selected := base.Where(rasql.And(
		rasql.LikeValue(columns.Email.Expr(), "%@example.com"),
		rasql.Or(
			rasql.LessValue(columns.ID.Expr(), int64(10)),
			rasql.And(
				rasql.GreaterValue(columns.ID.Expr(), int64(20)),
				rasql.IsNotNull(columns.Nickname.NullExpr()),
			),
		),
	)).OrderBy(rasql.AscExpr(columns.ID.Expr()))

	// Every level of the tree renders its own parentheses, so the SQL groups
	// the way the Go code nests rather than by the database's operator
	// precedence. Render proves it, rather than a comment merely claiming it.
	statement, err := rasql.Render(selected, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to render statement: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	found, err := rasql.All(ctx, db, selected)
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, user := range found {
		fmt.Println(user.ID, user.Email)
	}

	// Output:
	// SELECT "users"."id" AS "id", "users"."email" AS "email", "users"."nickname" AS "nickname", "users"."status" AS "status", "users"."first_name" AS "first_name", "users"."last_name" AS "last_name" FROM "users" WHERE (("users"."email" LIKE ?) AND (("users"."id" < ?) OR (("users"."id" > ?) AND ("users"."nickname" IS NOT NULL)))) ORDER BY "users"."id"
	// 5 ada@example.com
	// 25 alan@example.com
}
