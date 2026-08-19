# Writing rows

A write takes either of the two paths [Querying](03-querying.md) describes. The `query` package builds the statement and `render` turns it into SQL text with its arguments, which needs no row type and no database handle. The root package instead writes a typed row directly, reading the columns off the Go value, and covers the common inserts, updates, and deletes without a statement being assembled by hand.

`rasql.Exec` runs a statement built the first way, except one carrying a `RETURNING` clause, which reads its rows back through `dynamic.QueryWrite` or the typed `rasql.QueryWriteAll[T]` and `rasql.QueryWriteOne[T]`. [Write through the SQL builder](#write-through-the-sql-builder) covers that path, and the sections before it cover the typed helpers.

Every write operation, predicate, and statement constructor is listed in the [SQL builder reference](04-sql-builder.md#operation-reference) and the [typed reference](05-typed-queries.md#operation-reference).

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

Each statement runs on its own. To create several tables atomically, run `CreateTable` through the `rasql.DB` returned by `DB.Begin` and commit once every one has succeeded, as [Transactions](07-database.md#transactions) shows.

A descriptor that names a [`Schema`](02-schema.md#qualify-a-table-with-a-schema) renders `CREATE TABLE "audit"."events"` and `CREATE INDEX ... ON "audit"."events"` (SQLite instead qualifies the index name and leaves the table bare) into that namespace, but `rasql.CreateTable` never creates the namespace itself: it must already exist, created by a reviewed native migration, or `CreateTable` fails with the server's own error.

Most applications manage schema changes with [`migrate`](10-migrations.md). `CreateTable` remains useful for tests, examples, and one-shot setup.

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

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}

	// Insert uses the tagged fields in UserRow as values for the users table.
	// SQL: INSERT INTO users (id, email) VALUES (?, ?) (arguments: 42, "ada@example.com")
	result, err := rasql.Insert(ctx, db, users, UserRow{ID: 42, Email: "ada@example.com"})
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

The value must carry one exported tagged field for every column of the table, so a row that omits a column is a compile-time or validation problem rather than a silent `NULL`. A generated row type has no tags and supplies its column values through a `ColumnValue` method instead, which [the mapping methods](09-rasqlgen.md#the-mapping-and-scan-methods) covers. `Insert` returns the driver's `sql.Result`, which reports rows affected and, on databases that support it, the last inserted id.

`rasql.InsertMany` applies the same mapping to a slice of values and emits one parameterized multi-row `INSERT`. `InsertManyWithOptions` accepts `DefaultColumns` and omits those columns from every row; when every column is selected, it executes one dialect-rendered default-values `INSERT` per row. An empty slice is rejected, and callers that need to split a very large batch should make several calls so each statement stays under the database's parameter limit.

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
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite" // Registers the database/sql "sqlite" driver for this example.
)

// defaultUserRow and defaultUsersTable have the shape rasqlgen emits for a
// table with a generated ID and a defaulted status: no tags, a ColumnValue
// for writes, and read columns derived from the field names.
type defaultUserRow struct {
	ID     int64
	Email  string
	Status string
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
}

func (t defaultUsersTable) ID() query.ColumnRef     { return rasql.ColumnOf(t.Table, "id") }
func (t defaultUsersTable) Email() query.ColumnRef  { return rasql.ColumnOf(t.Table, "email") }
func (t defaultUsersTable) Status() query.ColumnRef { return rasql.ColumnOf(t.Table, "status") }

var defaultUsers = defaultUsersTable{rasql.MustTableOf[defaultUserRow](schema.MustTableDef("default_users",
	schema.Integer("id"),
	schema.Text("email"),
	schema.Text("status", schema.Default("'pending'")),
	schema.PrimaryKey("id"),
))}

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
	if err := rasql.CreateTable(ctx, db, defaultUsers); err != nil {
		fmt.Printf("failed to create default_users table: %s\n", err)
		return
	}

	// Name each database-assigned column. Email remains an explicit empty string.
	// SQL: INSERT INTO default_users (email) VALUES (?) (argument: "")
	if _, err := rasql.InsertWithOptions(ctx, db, defaultUsers, defaultUserRow{}, rasql.DefaultColumns("id", "status")); err != nil {
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

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	// Insert one row so the update has a persistent target.
	if _, err := rasql.Insert(ctx, db, users, UserRow{ID: 42, Email: "ada@example.com"}); err != nil {
		fmt.Printf("failed to insert user: %s\n", err)
		return
	}

	// Update matches the row's primary key and writes its non-key fields.
	// SQL: UPDATE users SET email = ? WHERE id = ? (arguments: "grace@example.com", 42)
	if _, err := rasql.Update(ctx, db, users, UserRow{ID: 42, Email: "grace@example.com"}); err != nil {
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

The row identifies itself, so there is no separate predicate to keep in step with it. A table without a primary key cannot be updated this way; build an `UPDATE` through the `query` package instead.

## Update selected fields or many rows

`rasql.UpdateWithOptions` keeps the typed mapping while selecting only the fields to assign. `UpdateColumns` names the non-primary-key fields to write, so a partial row value can omit every other field. Without `UpdateWhere`, the primary-key fields still identify one row. `UpdateWhere` replaces that primary-key predicate with an explicit, parameterized `query.Expression`, which updates every matching row. The typed helper rejects an unconditional update; use the lower-level `query` package when that behavior is intentional.

Use `rasql.UpdateMany` when the operation is intentionally bulk. It requires `UpdateWhere`, so omitting the predicate fails before execution.

## Delete rows

`rasql.DeleteFrom` starts a fluent builder that mirrors the select builder: `WhereEqual` and `WhereIn` take a `query.ColumnRef` of the target table, `Where` takes any predicate from the `query` package, and `Exec` runs the statement. `Build` and `Exec` reject a builder that carries no predicate; call `AllowAll` to state a full-table delete explicitly. `WhereIn` needs at least one value; `Build` and `Exec` return an error for an empty list rather than rendering `IN ()`, which is not valid SQL in any supported dialect. Call `Returning` to read deleted rows on dialects that support `RETURNING`.

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

	// A DB couples a database handle with the dialect used to render SQL.
	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	// Create the table described by the generated users descriptor.
	if err := rasql.CreateTable(ctx, db, users); err != nil {
		fmt.Printf("failed to create users table: %s\n", err)
		return
	}
	for id, email := range map[int64]string{1: "ada@example.com", 2: "grace@example.com", 3: "edsger@example.com"} {
		if _, err := rasql.Insert(ctx, db, users, UserRow{ID: id, Email: email}); err != nil {
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
	result, err = rasql.DeleteFrom(users).Where(query.GreaterThan(users.ID(), query.Bind(2))).Exec(ctx, db)
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

A delete matches whatever the predicate matches, so it is not tied to a primary key the way `Update` is. `Build` renders the statement without executing it when you want to see the SQL first; combine it with `AllowAll` to render a full-table delete.

## Write through the SQL builder

The `query` package builds every write statement, and `render` turns one into SQL text with its arguments in placeholder order. Neither step opens a database or asks for a Go row type, so a program that only has to produce SQL stops at the rendered statement.

<!-- INCLUDE(examples/query_render_write_example_test.go#render_write) -->
```go
func Example_query_render_write() {
	// A table description carries everything the DDL and the write
	// statements need. No row type and no database handle appear here.
	definition := schema.MustTableDef("accounts",
		schema.Integer("id"),
		schema.Text("email"),
		schema.PrimaryKey("id"),
	)
	accounts := query.MustTableRef(definition)
	id, err := accounts.Column("id")
	if err != nil {
		fmt.Printf("failed to reference the id column: %s\n", err)
		return
	}
	email, err := accounts.Column("email")
	if err != nil {
		fmt.Printf("failed to reference the email column: %s\n", err)
		return
	}

	// render.CreateTable turns the description into DDL for one dialect.
	ddl, err := render.CreateTable(dialect.PostgreSQL(), definition)
	if err != nil {
		fmt.Printf("failed to render the DDL: %s\n", err)
		return
	}
	fmt.Println(ddl.SQL())

	// query.NewInsert builds the statement; render.Insert turns it into SQL
	// text and the arguments that go with it.
	insert, err := query.NewInsert(accounts,
		[]query.ColumnRef{id, email},
		[]query.Expression{query.Bind(1), query.Bind("ada@example.com")},
	)
	if err != nil {
		fmt.Printf("failed to build the insert: %s\n", err)
		return
	}
	rendered, err := render.Insert(dialect.PostgreSQL(), insert)
	if err != nil {
		fmt.Printf("failed to render the insert: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)

	// An update is the same two steps. WithWhere keeps it off every row.
	update, err := query.NewUpdate(accounts, query.Set(email, query.Bind("grace@example.com")))
	if err != nil {
		fmt.Printf("failed to build the update: %s\n", err)
		return
	}
	update, err = update.WithWhere(query.Equal(id, query.Bind(1)))
	if err != nil {
		fmt.Printf("failed to add the predicate: %s\n", err)
		return
	}
	rendered, err = render.Update(dialect.PostgreSQL(), update)
	if err != nil {
		fmt.Printf("failed to render the update: %s\n", err)
		return
	}
	fmt.Println(rendered.SQL())
	fmt.Println(rendered.Args()...)

	// Output:
	// CREATE TABLE "accounts" ("id" BIGINT NOT NULL, "email" TEXT NOT NULL, PRIMARY KEY ("id"))
	// INSERT INTO "accounts" ("id", "email") VALUES ($1, $2)
	// 1 ada@example.com
	// UPDATE "accounts" SET "email" = $1 WHERE ("accounts"."id" = $2)
	// grace@example.com 1
}
```
source: [examples/query_render_write_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/query_render_write_example_test.go)
<!-- END INCLUDE -->

`render.CreateTable` takes the table description directly, so the DDL above comes out of a `schema.TableDef` alone. `rasql.CreateTable` in [Create a table](#create-a-table) renders the same DDL and executes it, which is why it asks for a `rasql.Table[T]` and an open database.

Inside an application, `rasql.Exec` runs any `query.WriteStatement`, which is what the `query` constructors produce: `NewInsert`, `NewInsertRows`, `NewUpdate`, `NewDelete`, and `NewUpsert`. Use them for conflict handling or SQL shapes not covered by the typed helpers. `Exec` rejects a statement carrying a `RETURNING` clause, because it discards result rows; use `dynamic.QueryWrite` for one of those instead.

`NewUpdate` and `NewDelete` accept a missing predicate while a statement is being assembled, but rendering and execution reject that shape unless the intent is explicit. Call `statement.AllowAll()` and use the returned statement when every row should be changed. A predicate and `AllowAll` cannot be combined.

`NewInsertRows` takes every row's values as one `[][]query.Expression` and renders them as a single `INSERT` with several parenthesized `VALUES` groups. Rendering the rows as one statement does not make the insert atomic on its own: transaction scope, and whether a statement that fails partway rolls back the rows it already wrote, stay the caller's and the database's responsibility. A non-transactional MySQL table, for instance, keeps the rows written before the failure. Run the insert through the `rasql.DB` returned by `DB.Begin` when every row has to land or none of them. Bound parameters are still capped by the database (PostgreSQL and MySQL at 65535, SQLite's `modernc.org/sqlite` at 32766), so a very large row count needs chunking at the caller.

<!-- INCLUDE(examples/rasql_partial_update_example_test.go#partial_update) -->
```go
statement, err := query.NewUpdate(users.Ref(), query.Set(users.Email(), query.Bind("ada@example.com")))
if err != nil {
	fmt.Printf("failed to build update: %s\n", err)
	return
}
statement, err = statement.WithWhere(query.LessThan(users.ID(), query.Bind(100)))
if err != nil {
	fmt.Printf("failed to filter update: %s\n", err)
	return
}
result, err := rasql.Exec(ctx, db, statement)
```
source: [examples/rasql_partial_update_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_partial_update_example_test.go)
<!-- END INCLUDE -->

Each `With…` method returns a new validated statement rather than changing the one it was called on, matching the immutable style of the select builders. `NewUpsert` accepts an explicit conflict target the same way; check `dialect.CapabilityConflictTarget` before relying on it, since MySQL lacks it and rejects a statement that sets one.

A multi-row insert built with `NewInsertRows` carries `RETURNING` and conflict handling the same way a one-row insert does, with two caveats.

Row order in a `RETURNING` result is not guaranteed to match the order of the `VALUES` list: SQLite states outright that `RETURNING` output order is undefined, and PostgreSQL never promises it either. Project a column that identifies the row and match on it; do not correlate the result with the input rows by position.

That advice assumes each `VALUES` row leaves its own distinct row behind, which a repeated conflict key breaks. An upsert over two `VALUES` rows sharing one conflict key returns, on SQLite, one `RETURNING` row per `VALUES` row, both carrying the same identifying value, even though only one physical row exists afterwards. An identifying column tells you which row a result refers to, not how many results refer to the same row.

What a `NewUpsert` over a multi-row insert does when two `VALUES` rows carry the same conflict key follows from the conflict action the statement renders, not from the dialect alone, and `Validate` checks none of it.

With assignments, the statement renders `ON CONFLICT (...) DO UPDATE` on PostgreSQL and SQLite and `ON DUPLICATE KEY UPDATE` on MySQL, and only PostgreSQL fails. PostgreSQL raises a cardinality violation, `ON CONFLICT DO UPDATE command cannot affect row a second time`, because it refuses to touch one existing row twice within a single command. SQLite decides the upsert separately for each row, so the second row takes the `DO UPDATE` branch and overwrites what the first row inserted, without reporting an error. MySQL applies `ON DUPLICATE KEY UPDATE` to the conflicting row, so the second row likewise updates what the first one inserted.

Without assignments, meaning a `NewUpsert` that carries only a conflict target, the statement renders `ON CONFLICT (...) DO NOTHING` on PostgreSQL and SQLite, and MySQL rejects it at render time because it has neither `dialect.CapabilityConflictTarget` nor an assignment list to update with. Neither PostgreSQL nor SQLite reports an error in this form: the duplicate row is skipped. PostgreSQL's cardinality violation belongs to `ON CONFLICT DO UPDATE` alone.

Either keep conflict keys unique within one statement, or account for that update.

### Reading a `RETURNING` clause

`WithReturning` adds a `RETURNING` clause on dialects that support it; check `dialect.CapabilityReturning` before relying on it, since MySQL does not. Once a statement carries one, `dynamic.QueryWrite` renders and runs it, returning the same rangeable `dynamic.Row` sequence a `SELECT` does, and the typed `rasql.QueryWriteAll[T]` and `rasql.QueryWriteOne[T]` decode that sequence the way `TypedSelectBuilder.All` and `.One` do. The typed pair stays in `rasql`, because they name a Go type rather than a column string.

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

	db, err := rasql.New(database, dialect.SQLite())
	if err != nil {
		fmt.Printf("failed to create rasql db: %s\n", err)
		return
	}
	if err := rasql.CreateTable(ctx, db, defaultUsers); err != nil {
		fmt.Printf("failed to create default_users table: %s\n", err)
		return
	}

	// id is assigned by the database and status by its column default, so both
	// are named in the RETURNING clause alongside the column that was set.
	statement, err := query.NewInsert(defaultUsers.Ref(), []query.ColumnRef{defaultUsers.Email()}, []query.Expression{query.Bind("ada@example.com")})
	if err != nil {
		fmt.Printf("failed to build insert: %s\n", err)
		return
	}
	statement, err = statement.WithReturning(query.Project(defaultUsers.ID()), query.Project(defaultUsers.Email()), query.Project(defaultUsers.Status()))
	if err != nil {
		fmt.Printf("failed to add RETURNING clause: %s\n", err)
		return
	}

	// SQL: INSERT INTO default_users (email) VALUES (?) RETURNING id, email, status (argument: "ada@example.com")
	user, err := rasql.QueryWriteOne[defaultUserRow](ctx, db, statement)
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

`QueryWriteOne` reports its row count through the same sentinels `One` does, described under [Select typed rows](05-typed-queries.md#select-typed-rows): `rasql.ErrNoRows` when `RETURNING` produced no rows, and `rasql.ErrMultipleRows` when it produced more than one.

Both typed terminals refuse a `RETURNING` clause that omits a column of the target table when `T` maps the whole table, because the omitted field would come back as a zero value with nothing to say so. A row type maps the whole table when it declares both `ScanRow` and `ScanDestinations`, which is the pair [rasqlgen](09-rasqlgen.md) writes for every row type it generates. A hand-written row type that maps only part of a table declares `ScanDestinations` alone, and these terminals then accept whatever projections it is given.

A fluent delete uses the same dynamic and typed terminals:

<!-- INCLUDE(examples/rasql_delete_returning_example_test.go#delete_returning_dynamic) -->
```go
builder := dynamic.DeleteFrom(users.Ref()).
	WhereEqual(users.ID(), 42).
	Returning(query.Project(users.ID()), query.Project(users.Email()))

rows, err := builder.Query(ctx, db)
```
source: [examples/rasql_delete_returning_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_delete_returning_example_test.go)
<!-- END INCLUDE -->

`dynamic.DeleteFrom` returns `dynamic.Row` values from its own `Query`.
`rasql.QueryDeleteAll[T]` and `rasql.QueryDeleteOne[T]` decode the projections
into `T` and follow the same empty or multiple-row rules as `QueryWriteAll[T]`
and `QueryWriteOne[T]`. Use one terminal per builder:

<!-- INCLUDE(examples/rasql_delete_returning_example_test.go#delete_returning_typed) -->
```go
typed := rasql.DeleteFrom(users).
	WhereEqual(users.ID(), 43).
	Returning(query.Project(users.ID()), query.Project(users.Email()))

deleted, err := rasql.QueryDeleteOne[UserRow](ctx, db, typed)
```
source: [examples/rasql_delete_returning_example_test.go](https://github.com/lestrrat-go/rasql/blob/main/examples/rasql_delete_returning_example_test.go)
<!-- END INCLUDE -->

A MySQL caller who needs a generated key skips `RETURNING` and reads `sql.Result.LastInsertId()` from `Exec` instead.

`DB.ExecRendered` runs a statement that is already rendered, which is how a compiled [static template](08-templates.md) is executed.

## Next

[The database handle](07-database.md) covers hooks and transactions, which wrap the writes on this page. [Static templates](08-templates.md) covers fixed SQL text with named binds.
