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

// Example_rasql_where_expressions combines a comparison and a null check with
// And, which is what a predicate richer than one comparison needs. Every
// plain value still travels as a bound argument: Value binds it automatically,
// and the renderer turns it into the dialect's placeholder.
func Example_rasql_where_expressions() {
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
		{ID: 15, Email: "grace@example.com", Nickname: rasql.Nullable[string]{Value: "Grace", Valid: true}},
		{ID: 17, Email: "nia@example.com"},
		{ID: 20, Email: "edsger@example.com", Nickname: rasql.Nullable[string]{Value: "Ed", Valid: true}},
	} {
		create := store.Users().Create().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last")
		if user.Nickname.Valid {
			create = create.Nickname(user.Nickname.Value)
		}
		if _, err := create.Exec(ctx, db); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// The generated columns struct binds every users column to the table, and
	// the generated projection selects them in the order the row type scans.
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

	// id=17 has id > 10 but no nickname, so it shows the And's second
	// condition is doing real work rather than repeating the first.
	// SQL: SELECT users.id, users.email, users.nickname, users.status, users.first_name, users.last_name FROM users WHERE (users.id > ? AND users.nickname IS NOT NULL) ORDER BY users.id DESC (argument: 10)
	rows, err := rasql.All(ctx, db,
		base.Where(rasql.And(
			rasql.GreaterValue(columns.ID.Expr(), int64(10)),
			rasql.IsNotNull(columns.Nickname.NullExpr()),
		)).OrderBy(rasql.DescExpr(columns.ID.Expr())))
	if err != nil {
		fmt.Printf("failed to query users: %s\n", err)
		return
	}
	for _, user := range rows {
		fmt.Println(user.ID, user.Email)
	}

	// Output:
	// 20 edsger@example.com
	// 15 grace@example.com
}
