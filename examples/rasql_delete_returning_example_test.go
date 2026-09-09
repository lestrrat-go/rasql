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

	db, err := rasql.New(database, dialect.SQLite())
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
			query.Set(users.ID().Ref(), user.id), query.Set(users.Email().Ref(), user.email),
			query.Set(users.FirstName().Ref(), user.firstName), query.Set(users.LastName().Ref(), user.lastName))
		if err != nil {
			fmt.Printf("failed to build insert: %s\n", err)
			return
		}
		rendered, err := render.Insert(db.Dialect(), insert)
		if err != nil {
			fmt.Printf("failed to render insert: %s\n", err)
			return
		}
		if _, err := db.ExecRendered(ctx, rendered); err != nil {
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
	statement, err = statement.WithWhere(query.Equal(users.ID().Ref(), query.Bind(42)))
	if err != nil {
		fmt.Printf("failed to add delete predicate: %s\n", err)
		return
	}
	statement, err = statement.WithReturning(users.ID().Ref(), users.Email().Ref())
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

	// The generated row names every column, where the first scan above named two.
	// store.UsersRow maps the whole users table, so the RETURNING list supplies
	// every field that its generated scanner expects.
	// SQL: DELETE FROM users WHERE users.id = ? RETURNING id, email, nickname, status, first_name, last_name (argument: 43)
	statement, err = query.NewDelete(users.Ref())
	if err != nil {
		fmt.Printf("failed to build typed delete: %s\n", err)
		return
	}
	statement, err = statement.WithWhere(query.Equal(users.ID().Ref(), query.Bind(43)))
	if err != nil {
		fmt.Printf("failed to add typed delete predicate: %s\n", err)
		return
	}
	statement, err = statement.WithReturning(users.ID().Ref(), users.Email().Ref(), users.Nickname().Ref(),
		users.Status().Ref(), users.FirstName().Ref(), users.LastName().Ref())
	if err != nil {
		fmt.Printf("failed to add typed delete returning: %s\n", err)
		return
	}
	rendered, err = render.Delete(db.Dialect(), statement)
	if err != nil {
		fmt.Printf("failed to render typed delete: %s\n", err)
		return
	}
	rows, err = db.QueryRendered(ctx, rendered)
	if err != nil {
		fmt.Printf("failed to delete user: %s\n", err)
		return
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			fmt.Printf("failed to read deleted user: %s\n", err)
		} else {
			fmt.Println("failed to read deleted user: no rows")
		}
		return
	}
	var deleted store.UsersRow
	if err := deleted.ScanRow(rows); err != nil {
		fmt.Printf("failed to read deleted user: %s\n", err)
		return
	}
	fmt.Println("typed:", deleted.ID, deleted.Email)
	if err := rows.Err(); err != nil {
		fmt.Printf("failed to read deleted user: %s\n", err)
		return
	}

	// Output:
	// dynamic: ada@example.com
	// typed: 43 grace@example.com
}
