# Writing rows

The root package writes a typed row without building a statement by hand. For anything the typed helpers do not cover, the `query` package builds the statement and `Client.Exec` runs it.

Every write operation, predicate, and statement constructor is listed in the [operation reference](03-querying.md#operation-reference).

## Create a table

`rasql.Create` renders a table descriptor as DDL and executes it, then creates its indexes.

```go
if err := rasql.Create(ctx, client, users); err != nil {
	return err
}
```

Each statement runs on its own. To create several tables atomically, build the client from a `*sql.Tx` and commit once every `Create` has succeeded.

Most applications manage schema changes with a migration tool instead. `rasql` has no migration planner; `Create` exists for tests, examples, and one-shot setup.

## Insert a row

`rasql.Insert` reads the `rasql`-tagged fields of the value and writes them as bound arguments.

<!-- INCLUDE(examples/rasql_insert_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_insert() {
	// This example inserts one generated row without constructing query.Insert.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Insert uses the tagged fields in UserRow as values for the users table.
	result, err := rasql.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"})
	if err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	inserted, err := result.RowsAffected()
	if err != nil {
		fmt.Printf("failed to count inserted users: %s\n", err)
		return
	}
	fmt.Printf("%d user inserted\n", inserted)

	// Output:
	// 1 user inserted
}
```
source: [examples/rasql_insert_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_insert_example_test.go)
<!-- END INCLUDE -->

The value must carry one exported tagged field for every column of the table, so a row that omits a column is a compile-time or validation problem rather than a silent `NULL`. `Insert` returns the driver's `sql.Result`, which reports rows affected and, on databases that support it, the last inserted id.

## Update a row

`rasql.Update` matches the primary-key fields of the value and writes every non-key column.

<!-- INCLUDE(examples/rasql_update_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_update() {
	// This example changes a generated row by using its primary-key field.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert one row so the update has a persistent target.
	if _, err := rasql.Insert(ctx, client, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// Update matches the row's primary key and writes its non-key fields.
	if _, err := rasql.Update(ctx, client, users, UserRow{ID: 42, Email: "grace@example.com"}); err != nil {
		fmt.Printf("failed to update user: %s\n", err)
		return
	}

	user, err := rasql.SelectFrom(client, users).WhereEqual(users.ID, 42).One(ctx)
	if err != nil {
		fmt.Printf("failed to query user: %s\n", err)
		return
	}
	fmt.Println(user.Email)

	// Output:
	// grace@example.com
}
```
source: [examples/rasql_update_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_update_example_test.go)
<!-- END INCLUDE -->

The row identifies itself, so there is no separate predicate to keep in step with it. A table without a primary key cannot be updated this way; build an `UPDATE` through the `query` package instead.

## Delete rows

`rasql.DeleteFrom` starts a fluent builder that mirrors the select builder: `WhereEqual` takes a `query.Column` of the target table, `Where` takes any predicate from the `query` package, and `Exec` runs the statement.

<!-- INCLUDE(examples/rasql_delete_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_delete() {
	// This example deletes rows by a generated column and by a query expression.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer database.Close()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for id, email := range map[int64]string{1: "ada@example.com", 2: "grace@example.com", 3: "edsger@example.com"} {
		if _, err := rasql.Insert(ctx, client, users, UserRow{ID: id, Email: email}); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// WhereEqual takes a column of the target table and binds the value.
	result, err := rasql.DeleteFrom(client, users).WhereEqual(users.ID, 1).Exec(ctx)
	if err != nil {
		fmt.Printf("failed to delete user: %s\n", err)
		return
	}
	deleted, err := result.RowsAffected()
	if err != nil {
		fmt.Printf("failed to count deleted users: %s\n", err)
		return
	}
	fmt.Printf("%d user deleted by id\n", deleted)

	// Where takes any predicate built through the query package.
	result, err = rasql.DeleteFrom(client, users).Where(query.GreaterThan(users.ID, query.Bind(2))).Exec(ctx)
	if err != nil {
		fmt.Printf("failed to delete users: %s\n", err)
		return
	}
	deleted, err = result.RowsAffected()
	if err != nil {
		fmt.Printf("failed to count deleted users: %s\n", err)
		return
	}
	fmt.Printf("%d user deleted by predicate\n", deleted)

	// Build renders the statement without executing it, which shows that a
	// builder with no predicate deletes every row.
	statement, err := rasql.DeleteFrom(client, users).Build()
	if err != nil {
		fmt.Printf("failed to build delete: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	// Output:
	// 1 user deleted by id
	// 1 user deleted by predicate
	// DELETE FROM "users"
}
```
source: [examples/rasql_delete_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_delete_example_test.go)
<!-- END INCLUDE -->

A delete matches whatever the predicate matches, so it is not tied to a primary key the way `Update` is. A builder with no predicate deletes every row of the table; `Build` renders the statement without executing it when you want to see the SQL first.

## Statements the typed helpers do not cover

`Client.Exec` runs any `query.WriteStatement`, which is what the `query` constructors produce: `NewInsert`, `NewUpdate`, `NewDelete`, and `NewUpsert`. Use them for a partial update, conflict handling, or a `RETURNING` clause.

```go
statement, err := query.NewUpdate(users.QueryTable(), query.Set(users.Email, query.Bind("ada@example.com")))
if err != nil {
	return err
}
statement, err = statement.WithWhere(query.LessThan(users.ID, query.Bind(100)))
if err != nil {
	return err
}
result, err := client.Exec(ctx, statement)
```

Each `With…` method returns a new validated statement rather than changing the one it was called on, matching the immutable style of the select builders. `WithReturning` adds a `RETURNING` clause on dialects that support it; check `dialect.CapabilityReturning` before relying on it, since MySQL does not.

`Client.ExecRendered` runs a statement that is already rendered, which is how a compiled [static template](05-templates.md) is executed.

## Next

[Static templates](05-templates.md) covers fixed SQL text with named binds.
