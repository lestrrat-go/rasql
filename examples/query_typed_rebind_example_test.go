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

// Example_rebindTypedResult builds a query, then reprojects it to a narrower
// result type while keeping its WHERE. Project takes an existing query's
// QueryPlan and a new Projection, so the base query's predicate carries over
// unchanged and only the projected shape changes: running the reprojected
// query against a row planted at a different id proves the WHERE survived,
// since only the row whose id matches the base predicate can come back.
func Example_rebindTypedResult() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
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
	for _, user := range []store.UsersRow{
		{ID: 3, Email: "wrong@example.com"},
		{ID: 7, Email: "rebind@example.com"},
	} {
		plan := store.NewUsersCreate().ID(user.ID).Email(user.Email).FirstName("First").LastName("Last").Plan()
		if _, err := rasql.ExecMutation(ctx, executor, plan); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	source, err := rasql.SourceOf(users, "")
	if err != nil {
		fmt.Printf("failed to bind users source: %s\n", err)
		return
	}
	id, err := rasql.BindColumn[store.UsersRow, int64](source, users.IDRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind id column: %s\n", err)
		return
	}
	email, err := rasql.BindColumn[store.UsersRow, string](source, users.EmailRef().Name(), "")
	if err != nil {
		fmt.Printf("failed to bind email column: %s\n", err)
		return
	}
	idProjection, err := rasql.Scalar("id", id.Expr(), schema.IntegerType{}, "")
	if err != nil {
		fmt.Printf("failed to build id projection: %s\n", err)
		return
	}
	// base projects id and filters to one row. A caller who only needed the
	// filter, not this particular projected shape, still built it this way to
	// reuse the WHERE.
	base := rasql.Select(source.Source(), idProjection).Where(rasql.EqualValue(id.Expr(), int64(7)))

	emailProjection, err := rasql.Scalar("email", email.Expr(), schema.TextType{}, "")
	if err != nil {
		fmt.Printf("failed to build email projection: %s\n", err)
		return
	}
	// dto keeps base's WHERE and reprojects to email instead of id.
	dto := rasql.Project(base.Plan(), emailProjection)

	found, err := rasql.One(ctx, executor, dto)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	fmt.Println(found)
	// Output:
	// rebind@example.com
}
