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

// Example_rasql_delete_returning reads the rows a delete removes. The low-level
// rendered query returns database/sql rows, which the example scans into either
// individual values or the generated row type.
//
// It builds its statements with the query package, whose Set and WithReturning
// take a query.ColumnRef. Column is the generated table's only way to produce
// one, so this example names its columns as strings; an example that stays in
// the typed layer binds them through store.UsersColumns instead.
func Example_rasql_delete_returning() {
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
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	users := store.Users()
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for _, user := range []struct {
		id                         int64
		email, firstName, lastName string
	}{
		{42, "ada@example.com", "Ada", "Lovelace"},
		{43, "grace@example.com", "Grace", "Hopper"},
	} {
		insert, err := query.NewInsert(users.Ref(),
			query.Set(users.Ref().Column("id"), user.id), query.Set(users.Ref().Column("email"), user.email),
			query.Set(users.Ref().Column("first_name"), user.firstName), query.Set(users.Ref().Column("last_name"), user.lastName))
		if err != nil {
			fmt.Printf("failed to build insert: %s\n", err)
			return
		}
		rendered, err := render.Insert(db.Dialect(), insert)
		if err != nil {
			fmt.Printf("failed to render insert: %s\n", err)
			return
		}
		if _, err := db.Exec(ctx, rendered); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// SQL: DELETE FROM users WHERE users.id = ? RETURNING id, email (argument: 42)
	statement, err := query.NewDelete(users.Ref())
	if err != nil {
		fmt.Printf("failed to build delete: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(users.Ref().Column("id"), query.Bind(42)))
	if err != nil {
		fmt.Printf("failed to add delete predicate: %s\n", err)
		return
	}
	statement, err = statement.WithReturning(users.Ref().Column("id"), users.Ref().Column("email"))
	if err != nil {
		fmt.Printf("failed to add delete returning: %s\n", err)
		return
	}
	rendered, err := render.Delete(db.Dialect(), statement)
	if err != nil {
		fmt.Printf("failed to render delete: %s\n", err)
		return
	}
	rows, err := db.QueryRendered(ctx, rendered)
	if err != nil {
		fmt.Printf("failed to delete user: %s\n", err)
		return
	}
	defer func() { _ = rows.Close() }()
	for rows.Next() {
		var deletedID int64
		var email string
		if err := rows.Scan(&deletedID, &email); err != nil {
			fmt.Printf("failed to read deleted user: %s\n", err)
			return
		}
		fmt.Println("dynamic:", email)
	}
	if err := rows.Err(); err != nil {
		fmt.Printf("failed to read deleted user: %s\n", err)
		return
	}

	// The typed layer reads the same clause without naming a column twice. The
	// generated table's own Delete method takes no argument, and its Where
	// takes a typed rasql.Predicate; the generated projection names all six
	// columns, so Returning hands One a whole decoded store.UsersRow.
	// SQL: DELETE FROM users WHERE users.id = ? RETURNING id, email, nickname, status, first_name, last_name (argument: 43)
	plan, err := users.Delete().Where(rasql.EqualValue(users.ID.Expr(), int64(43))).Plan()
	if err != nil {
		fmt.Printf("failed to build typed delete: %s\n", err)
		return
	}
	projection, err := store.UsersProjection(users)
	if err != nil {
		fmt.Printf("failed to build users projection: %s\n", err)
		return
	}
	saved, err := rasql.Returning(plan, projection)
	if err != nil {
		fmt.Printf("failed to attach the RETURNING clause: %s\n", err)
		return
	}
	deleted, err := rasql.One(ctx, db, saved)
	if err != nil {
		fmt.Printf("failed to read deleted user: %s\n", err)
		return
	}
	fmt.Println("typed:", deleted.ID, deleted.Email)

	// Output:
	// dynamic: ada@example.com
	// typed: 43 grace@example.com
}
