# Writing rows

The root package writes a typed row without building a statement by hand. For anything the typed helpers do not cover, the `query` package builds the statement and `rasql.Exec` runs it, except a statement carrying a `RETURNING` clause, which reads its rows back through `rasql.QueryWrite` or the typed `rasql.QueryWriteAll[T]` and `rasql.QueryWriteOne[T]`.

Every write operation, predicate, and statement constructor is listed in the [operation reference](03-querying.md#operation-reference).

## Create a table

`rasql.Create` renders a table descriptor as DDL and executes it, then creates its indexes.

```go
if err := rasql.Create(ctx, client, users); err != nil {
	return err
}
```

Each statement runs on its own. To create several tables atomically, run `Create` through a `rasql.Tx` from `rasql.Begin` and commit once every one has succeeded, as [Transactions](#transactions) shows.

A descriptor that names a [`Schema`](02-schema.md#qualify-a-table-with-a-schema) renders `CREATE TABLE "audit"."events"` and `CREATE INDEX ... ON "audit"."events"` (SQLite instead qualifies the index name and leaves the table bare) into that namespace, but `rasql.Create` never creates the namespace itself: it must already exist, created by a reviewed native migration, or `Create` fails with the server's own error.

Most applications manage schema changes with [`migrate`](07-migrations.md). `Create` remains useful for tests, examples, and one-shot setup.

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
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
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

The value must carry one exported tagged field for every column of the table, so a row that omits a column is a compile-time or validation problem rather than a silent `NULL`. A generated row type has no tags and supplies its column values through a `ColumnValue` method instead, which [the two mapping methods](06-rasqlgen.md#the-two-mapping-methods) covers. `Insert` returns the driver's `sql.Result`, which reports rows affected and, on databases that support it, the last inserted id.

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
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/row"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// defaultUserRow and defaultUsersTable have the method-based shape rasqlgen
// emits for a table with a generated ID and a defaulted status.
type defaultUserRow struct {
	ID     int64
	Email  string
	Status string
}

func (r *defaultUserRow) DecodeRow(source row.Dynamic) error {
	if err := row.Assign(source, "id", &r.ID); err != nil {
		return err
	}
	if err := row.Assign(source, "email", &r.Email); err != nil {
		return err
	}
	return row.Assign(source, "status", &r.Status)
}

func (r defaultUserRow) ColumnValue(name string) (any, bool) {
	switch name {
	case "id":
		return r.ID, true
	case "email":
		return r.Email, true
	case "status":
		return r.Status, true
	}
	return nil, false
}

type defaultUsersTable struct {
	rasql.Table[defaultUserRow]
	ID     query.Column
	Email  query.Column
	Status query.Column
}

func newDefaultUsersTable(table rasql.Table[defaultUserRow]) defaultUsersTable {
	return defaultUsersTable{
		Table:  table,
		ID:     rasql.MustColumn(table, "id"),
		Email:  rasql.MustColumn(table, "email"),
		Status: rasql.MustColumn(table, "status"),
	}
}

var defaultUsers = newDefaultUsersTable(rasql.MustTable[defaultUserRow](schema.Table{
	Name: "default_users",
	Columns: []schema.Column{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}},
		{Name: "status", Type: schema.TextType{}, Default: "'pending'"},
	},
	PrimaryKey: []string{"id"},
}))

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

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	if err := rasql.Create(ctx, client, defaultUsers); err != nil {
		fmt.Printf("failed to create default_users table: %s\n", err)
		return
	}

	// Name each database-assigned column. Email remains an explicit empty string.
	if _, err := rasql.InsertWithOptions(ctx, client, defaultUsers, defaultUserRow{}, rasql.DefaultColumns("id", "status")); err != nil {
		fmt.Printf("failed to insert default user: %s\n", err)
		return
	}

	user, err := rasql.SelectFrom(defaultUsers).WhereEqual(defaultUsers.ID, 1).One(ctx, client)
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
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

func Example_rasql_update() {
	// This example changes a generated row by using its primary-key field.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
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

	user, err := rasql.SelectFrom(users).WhereEqual(users.ID, 42).One(ctx, client)
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

`rasql.DeleteFrom` starts a fluent builder that mirrors the select builder: `WhereEqual` and `WhereIn` take a `query.Column` of the target table, `Where` takes any predicate from the `query` package, and `Exec` runs the statement. `Build` and `Exec` reject a builder that carries no predicate; call `AllowAll` to state a full-table delete explicitly. `WhereIn` needs at least one value; `Build` and `Exec` return an error for an empty list rather than rendering `IN ()`, which is not valid SQL in any supported dialect.

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
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
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
	result, err := rasql.DeleteFrom(users).WhereEqual(users.ID, 1).Exec(ctx, client)
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
	result, err = rasql.DeleteFrom(users).Where(query.GreaterThan(users.ID, query.Bind(2))).Exec(ctx, client)
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
	if _, err := rasql.DeleteFrom(users).Build(client.Dialect()); err != nil {
		fmt.Println(err)
	}

	// AllowAll states the full-table delete. Build renders it without executing it.
	statement, err := rasql.DeleteFrom(users).AllowAll().Build(client.Dialect())
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

A delete matches whatever the predicate matches, so it is not tied to a primary key the way `Update` is. `Build` renders the statement without executing it when you want to see the SQL first; combine it with `AllowAll` to render a full-table delete.

## Statements the typed helpers do not cover

`rasql.Exec` runs any `query.WriteStatement`, which is what the `query` constructors produce: `NewInsert`, `NewInsertRows`, `NewUpdate`, `NewDelete`, and `NewUpsert`. Use them for a partial update, conflict handling, or a multi-row insert. `Exec` rejects a statement carrying a `RETURNING` clause, because it discards result rows; use `rasql.QueryWrite` for one of those instead.

`NewUpdate` and `NewDelete` accept a missing predicate while a statement is being assembled, but rendering and execution reject that shape unless the intent is explicit. Call `statement.AllowAll()` and use the returned statement when every row should be changed. A predicate and `AllowAll` cannot be combined.

`NewInsertRows` takes every row's values as one `[][]query.Expression` and renders them as a single `INSERT` with several parenthesized `VALUES` groups. Rendering the rows as one statement does not make the insert atomic on its own: transaction scope, and whether a statement that fails partway rolls back the rows it already wrote, stay the caller's and the database's responsibility. A non-transactional MySQL table, for instance, keeps the rows written before the failure. Run the insert through a `rasql.Tx` from `rasql.Begin` when every row has to land or none of them. Bound parameters are still capped by the database (PostgreSQL and MySQL at 65535, SQLite's `modernc.org/sqlite` at 32766), so a very large row count needs chunking at the caller.

```go
statement, err := query.NewUpdate(users.QueryTable(), query.Set(users.Email, query.Bind("ada@example.com")))
if err != nil {
	return err
}
statement, err = statement.WithWhere(query.LessThan(users.ID, query.Bind(100)))
if err != nil {
	return err
}
result, err := rasql.Exec(ctx, client, statement)
```

Each `With…` method returns a new validated statement rather than changing the one it was called on, matching the immutable style of the select builders. `NewUpsert` accepts an explicit conflict target the same way; check `dialect.CapabilityConflictTarget` before relying on it, since MySQL lacks it and rejects a statement that sets one.

A multi-row insert built with `NewInsertRows` carries `RETURNING` and conflict handling the same way a one-row insert does, with two caveats worth knowing before relying on either.

Row order in a `RETURNING` result is not guaranteed to match the order of the `VALUES` list: SQLite states outright that `RETURNING` output order is undefined, and PostgreSQL never promises it either. Project a column that identifies the row and match on it; do not correlate the result with the input rows by position.

That advice assumes each `VALUES` row leaves its own distinct row behind, which a repeated conflict key breaks. An upsert over two `VALUES` rows sharing one conflict key returns, on SQLite, one `RETURNING` row per `VALUES` row, both carrying the same identifying value, even though only one physical row exists afterwards. An identifying column tells you which row a result refers to, not how many results refer to the same row.

What a `NewUpsert` over a multi-row insert does when two `VALUES` rows carry the same conflict key follows from the conflict action the statement renders, not from the dialect alone, and `Validate` checks none of it.

With assignments, the statement renders `ON CONFLICT (...) DO UPDATE` on PostgreSQL and SQLite and `ON DUPLICATE KEY UPDATE` on MySQL, and only PostgreSQL fails. PostgreSQL raises a cardinality violation, `ON CONFLICT DO UPDATE command cannot affect row a second time`, because it refuses to touch one existing row twice within a single command. SQLite decides the upsert separately for each row, so the second row takes the `DO UPDATE` branch and overwrites what the first row inserted, without reporting an error. MySQL applies `ON DUPLICATE KEY UPDATE` to the conflicting row, so the second row likewise updates what the first one inserted.

Without assignments, meaning a `NewUpsert` that carries only a conflict target, the statement renders `ON CONFLICT (...) DO NOTHING` on PostgreSQL and SQLite, and MySQL rejects it at render time because it has neither `dialect.CapabilityConflictTarget` nor an assignment list to update with. Neither PostgreSQL nor SQLite reports an error in this form: the duplicate row is skipped. PostgreSQL's cardinality violation belongs to `ON CONFLICT DO UPDATE` alone.

Either keep conflict keys unique within one statement, or account for that update.

### Reading a `RETURNING` clause

`WithReturning` adds a `RETURNING` clause on dialects that support it; check `dialect.CapabilityReturning` before relying on it, since MySQL does not. Once a statement carries one, `rasql.QueryWrite` renders and runs it, returning the same rangeable `row.Dynamic` sequence a `SELECT` does, and the typed `rasql.QueryWriteAll[T]` and `rasql.QueryWriteOne[T]` decode that sequence the way `TypedSelectBuilder.All` and `.One` do:

<!-- INCLUDE(examples/rasql_returning_example_test.go) -->
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

// Example_rasql_returning reads the row a RETURNING clause produces, which
// rasql.Exec cannot do because it discards result rows.
func Example_rasql_returning() {
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	if err := rasql.Create(ctx, client, defaultUsers); err != nil {
		fmt.Printf("failed to create default_users table: %s\n", err)
		return
	}

	// id is assigned by the database and status by its column default, so both
	// are named in the RETURNING clause alongside the column that was set.
	statement, err := query.NewInsert(defaultUsers.QueryTable(), []query.Column{defaultUsers.Email}, []query.Expression{query.Bind("ada@example.com")})
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	statement, err = statement.WithReturning(query.Project(defaultUsers.ID), query.Project(defaultUsers.Email), query.Project(defaultUsers.Status))
	if err != nil {
		fmt.Printf("failed to add RETURNING clause: %s\n", err)
		return
	}

	user, err := rasql.QueryWriteOne[defaultUserRow](ctx, client, statement)
	if err != nil {
		fmt.Printf("failed to query inserted user: %s\n", err)
		return
	}
	fmt.Printf("%d %q %q\n", user.ID, user.Email, user.Status)

	// Output:
	// 1 "ada@example.com" "pending"
}
```
source: [examples/rasql_returning_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_returning_example_test.go)
<!-- END INCLUDE -->

`QueryWriteOne` reports its row count through the same sentinels `One` does, described under [Select typed rows](03-querying.md#select-typed-rows): `rasql.ErrNoRows` when `RETURNING` produced no rows, and `rasql.ErrMultipleRows` when it produced more than one.

A MySQL caller who needs a generated key skips `RETURNING` and reads `sql.Result.LastInsertId()` from `Exec` instead.

`Client.ExecRendered` runs a statement that is already rendered, which is how a compiled [static template](05-templates.md) is executed.

## Transactions

`rasql.Begin` takes a `*sql.DB` (anything implementing `rasql.Beginner`), a dialect, and `*sql.TxOptions`, which may be `nil`. It returns a `rasql.Tx`, which is an `Executor` like `Client`, so every builder terminal and every free function that takes an `Executor` — `rasql.Insert`, `rasql.Update`, `rasql.Create`, and the rest — accepts it in place of `client`.

The caller owns the transaction. `defer tx.Rollback()` immediately after `Begin` is the intended shape, because `Rollback` reports nothing once the transaction is finished, whether by a successful `Commit`, an earlier `Rollback`, or a context cancellation.

A transaction cannot be nested: `Tx` does not implement `Beginner`, so passing one to `Begin` does not compile.

<!-- INCLUDE(examples/rasql_transaction_example_test.go) -->
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

func Example_rasql_transaction() {
	// This example writes two rows and reads them back inside one transaction,
	// then reads them again through the plain client after it commits.
	// users and UserRow are declared in query_example_tables_test.go with the
	// shape rasqlgen emits; an application that generated into package store
	// would write store.Users() and store.UsersRow instead.
	ctx := context.Background()
	database, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		fmt.Printf("failed to open SQLite database: %s\n", err)
		return
	}
	defer func() { _ = database.Close() }()
	// An in-memory SQLite database is per connection, so keep this example on one.
	database.SetMaxOpenConns(1)

	// A Client couples a database handle with the dialect used to render SQL.
	client, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql client: %s\n", err)
		return
	}
	// Create the table before any transaction starts.
	if err := rasql.Create(ctx, client, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Begin takes the *sql.DB, not the client: a Tx renders its own statements
	// and does not go through client at all.
	tx, err := rasql.Begin(ctx, database, dialect.SQLite(), nil)
	if err != nil {
		fmt.Printf("failed to begin transaction: %s\n", err)
		return
	}
	// Rollback reports nothing once Commit has already succeeded, which is what
	// makes this bare defer correct rather than an error every caller discards.
	defer func() { _ = tx.Rollback() }()

	if _, err := rasql.Insert(ctx, tx, users, UserRow{ID: 1, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}
	if _, err := rasql.Insert(ctx, tx, users, UserRow{ID: 2, Email: "grace@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// The same builder shape that runs against client also runs against tx: it
	// reads the two rows written above, before they are committed.
	inTx, err := rasql.SelectFrom(users).OrderAsc(users.ID).All(ctx, tx)
	if err != nil {
		fmt.Printf("failed to query users in transaction: %s\n", err)
		return
	}
	fmt.Printf("%d rows visible in transaction\n", len(inTx))

	if err := tx.Commit(); err != nil {
		fmt.Printf("failed to commit transaction: %s\n", err)
		return
	}

	// Nothing touches client between Begin and Commit above. That is this
	// example's own constraint, not rasql's: SetMaxOpenConns(1) gives it one
	// connection, and the transaction holds it until Commit or Rollback
	// releases it back to the pool.
	afterCommit, err := rasql.SelectFrom(users).OrderAsc(users.ID).All(ctx, client)
	if err != nil {
		fmt.Printf("failed to query users after commit: %s\n", err)
		return
	}
	fmt.Printf("%d rows visible after commit\n", len(afterCommit))

	// Output:
	// 2 rows visible in transaction
	// 2 rows visible after commit
}
```
source: [examples/rasql_transaction_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_transaction_example_test.go)
<!-- END INCLUDE -->

## Next

[Static templates](05-templates.md) covers fixed SQL text with named binds.
