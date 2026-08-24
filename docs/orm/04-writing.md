# Writing rows

The root package writes a typed row directly, reading the columns off the Go value, and covers the common inserts, updates, and deletes without a statement being assembled by hand. [Write statements](../core/03-write-statements.md) covers the other path, where `query` builds the statement and `render` turns it into SQL text with its arguments, with no row type and no database handle in hand.

Every write operation, predicate, and statement constructor is listed in the [SQL builder reference](../core/02-sql-builder.md#operation-reference) and the [typed reference](03-typed-queries.md#operation-reference).

## Create a table

`rasql.CreateTable` renders a table descriptor as DDL and executes it, then creates its indexes.

<!-- INCLUDE(examples/rasql_sqlite_query_example_test.go#create_table) -->
```go
if err := rasql.CreateTable(ctx, db, users); err != nil {
	fmt.Printf("failed to create users table: %s\n", err)
	return
}
```
source: [examples/rasql_sqlite_query_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_sqlite_query_example_test.go)
<!-- END INCLUDE -->

Each statement runs on its own. To create several tables atomically, run `CreateTable` through the `rasql.DB` returned by `DB.Begin` and commit once every one has succeeded, as [Transactions](../core/04-database.md#transactions) shows.

A descriptor that names a [`Schema`](../core/01-schema.md#qualify-a-table-with-a-schema) renders `CREATE TABLE "audit"."events"` and `CREATE INDEX ... ON "audit"."events"` (SQLite instead qualifies the index name and leaves the table bare) into that namespace, but `rasql.CreateTable` never creates the namespace itself: it must already exist, created by a reviewed native migration, or `CreateTable` fails with the server's own error.

Most applications manage schema changes with [`migrate`](../core/07-migrations.md). `CreateTable` remains useful for tests, examples, and one-shot setup.

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
	"github.com/lestrrat-go/rasql/examples/store"
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
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	users := store.Users()
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Insert reads store.UsersRow's fields through the mapping method the
	// generator wrote, and binds them as values for the users table.
	// SQL: INSERT INTO users (id, email) VALUES (?, ?) (arguments: 42, "ada@example.com")
	result, err := rasql.Insert(ctx, db, users, store.UsersRow{ID: 42, Email: "ada@example.com"})
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

The value must carry one exported tagged field for every column of the table, so a row that omits a column is a compile-time or validation problem rather than a silent `NULL`. A generated row type has no tags and supplies its column values through a `ColumnValue` method instead, which [the mapping methods](02-generated-store.md#the-mapping-and-scan-methods) covers. `Insert` returns the driver's `sql.Result`, which reports rows affected and, on databases that support it, the last inserted id.

A generated column, one whose value the database computes from an expression, is left out of every write these helpers build. `rasql.Insert`, `rasql.InsertMany`, `rasql.Update`, and `rasql.UpdateMany` all drop it from their column list on their own, because a database rejects a statement that writes to one. Naming a generated column through `rasql.UpdateColumns` is refused up front. [Generated columns](../core/08-inspection-facts.md#generated-columns) covers how a descriptor records one.

`rasql.InsertMany` applies the same mapping to a slice of values and emits one parameterized multi-row `INSERT`. `InsertManyWithOptions` accepts `DefaultColumns` and omits those columns from every row. When every column is selected, it executes one dialect-rendered default-values `INSERT` per row. An empty slice is rejected, and callers that need to split a very large batch should make several calls so each statement stays under the database's parameter limit.

## Use database defaults

Pass `rasql.DefaultColumns` to `rasql.InsertWithOptions` to omit named columns from an insert. The database supplies those values. `InsertWithOptions` never treats a Go zero value as absent, so every column not named by `DefaultColumns` remains a bound value. When every column is named, PostgreSQL, MySQL, and SQLite render an all-default insert.

<!-- INCLUDE(examples/rasql_insert_defaults_example_test.go) -->
```go
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

// Example_rasql_insert_defaults writes a row whose id the database assigns and
// whose status comes from the column's default. default_users is generated into
// examples/store from a descriptor that declares that default.
func Example_rasql_insert_defaults() {
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
	defaultUsers := store.DefaultUsers()
	if err := rasql.CreateTable(ctx, db, defaultUsers); err != nil {
		fmt.Printf("failed to create default_users table: %s\n", err)
		return
	}

	// Name each database-assigned column. Email remains an explicit empty string.
	// SQL: INSERT INTO default_users (email) VALUES (?) (argument: "")
	if _, err := rasql.InsertWithOptions(ctx, db, defaultUsers, store.DefaultUsersRow{}, rasql.DefaultColumns("id", "status")); err != nil {
		fmt.Printf("failed to insert default user: %s\n", err)
		return
	}

	// SQL: SELECT default_users.id, default_users.email, default_users.status FROM default_users WHERE default_users.id = ? (argument: 1)
	user, err := rasql.SelectFrom(defaultUsers).WhereEqual(defaultUsers.ID(), 1).One(ctx, db)
	if err != nil {
		fmt.Printf("failed to query default user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "" "pending"
}
```
source: [examples/rasql_insert_defaults_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_insert_defaults_example_test.go)
<!-- END INCLUDE -->

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
	"github.com/lestrrat-go/rasql/examples/store"
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
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	users := store.Users()
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert one row so the update has a persistent target.
	if _, err := rasql.Insert(ctx, db, users, store.UsersRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// Update matches the row's primary key and writes its non-key fields.
	// SQL: UPDATE users SET email = ? WHERE id = ? (arguments: "grace@example.com", 42)
	if _, err := rasql.Update(ctx, db, users, store.UsersRow{ID: 42, Email: "grace@example.com"}); err != nil {
		fmt.Printf("failed to update user: %s\n", err)
		return
	}

	// SQL: SELECT users.id, users.email FROM users WHERE users.id = ? (argument: 42)
	user, err := rasql.SelectFrom(users).WhereEqual(users.ID(), 42).One(ctx, db)
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

The row identifies itself, so there is no separate predicate to keep in step with it. A table without a primary key cannot be updated this way. Build an `UPDATE` through the `query` package instead.

## Update selected fields or many rows

`rasql.UpdateWithOptions` keeps the typed mapping while selecting only the fields to assign. `UpdateColumns` names the non-primary-key fields to write, so a partial row value can omit every other field. Without `UpdateWhere`, the primary-key fields still identify one row. `UpdateWhere` replaces that primary-key predicate with an explicit, parameterized `query.Expression`, which updates every matching row. The typed helper rejects an unconditional update. Use the lower-level `query` package when that behavior is intentional.

Use `rasql.UpdateMany` when the operation is intentionally bulk. It requires `UpdateWhere`, so omitting the predicate fails before execution.

## Delete rows

`rasql.DeleteFrom` starts a fluent builder that mirrors the select builder: `WhereEqual` and `WhereIn` take a `query.ColumnRef` of the target table, `Where` takes any predicate from the `query` package, and `Exec` runs the statement. `Build` and `Exec` reject a builder that carries no predicate. Call `AllowAll` to state a full-table delete explicitly. `WhereIn` needs at least one value. `Build` and `Exec` return an error for an empty list rather than rendering `IN ()`, which is not valid SQL in any supported dialect. Call `Returning` to read deleted rows on dialects that support `RETURNING`.

<!-- INCLUDE(examples/rasql_delete_example_test.go) -->
```go
package examples_test

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/examples/store"
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
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	users := store.Users()
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for id, email := range map[int64]string{1: "ada@example.com", 2: "grace@example.com", 3: "edsger@example.com"} {
		if _, err := rasql.Insert(ctx, db, users, store.UsersRow{ID: id, Email: email}); err != nil {
			fmt.Printf("failed to insert user: %s\n", err)
			return
		}
	}

	// WhereEqual takes a column of the target table and binds the value.
	// SQL: DELETE FROM users WHERE users.id = ? (argument: 1)
	result, err := rasql.DeleteFrom(users).WhereEqual(users.ID(), 1).Exec(ctx, db)
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
	// SQL: DELETE FROM users WHERE users.id > ? (argument: 2)
	result, err = rasql.DeleteFrom(users).Where(query.GreaterThan(users.ID(), 2)).Exec(ctx, db)
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

	// A builder with no predicate is rejected, so a dropped Where cannot become
	// a full-table delete by accident.
	if _, err := rasql.DeleteFrom(users).Build(db.Dialect()); err != nil {
		fmt.Println(err)
	}

	// AllowAll states the full-table delete. Build renders it without executing it.
	// SQL: DELETE FROM users
	statement, err := rasql.DeleteFrom(users).AllowAll().Build(db.Dialect())
	if err != nil {
		fmt.Printf("failed to build delete: %s\n", err)
		return
	}
	fmt.Println(statement.SQL())

	// Output:
	// 1 user deleted by id
	// 1 user deleted by predicate
	// rasql: DELETE requires a WHERE predicate or an explicit AllowAll
	// DELETE FROM "users"
}
```
source: [examples/rasql_delete_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_delete_example_test.go)
<!-- END INCLUDE -->

A delete matches whatever the predicate matches, so it is not tied to a primary key the way `Update` is. `Build` renders the statement without executing it when you want to see the SQL first. Combine it with `AllowAll` to render a full-table delete.

## Next

[Write statements](../core/03-write-statements.md) builds the same writes through `query` and `render`, with no row type and no database handle. [The database handle](../core/04-database.md) covers hooks and transactions, which wrap the writes on this page.
