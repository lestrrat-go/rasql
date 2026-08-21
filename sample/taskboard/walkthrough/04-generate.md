# 4. Generate the store

The schema is in the database and in `db/migrations`. This chapter turns it into Go: row structs, table types, one accessor per column, and a join for each foreign key. None of it is typed by hand, and none of it is edited afterwards.

## Point the module at rasql

The project has been a `go.mod` and some SQL until now. The generated code and the application both import `github.com/lestrrat-go/rasql`, so the module needs it as a dependency.

The walkthrough builds against a checkout of the repository rather than a release, for the reason [chapter 3](03-capture.md#get-the-rasql-command) gave, so its `go.mod` names the dependency and then redirects it at that checkout. Clone the repository beside the project directory, and the checkout is one level up under its own name:

```text
require github.com/lestrrat-go/rasql v0.0.0

replace github.com/lestrrat-go/rasql => ../rasql
```

Go resolves a `replace` path relative to the directory holding `go.mod`, so a checkout kept somewhere else takes the path that reaches it from the project.

A project using a released rasql adds the dependency with `go get github.com/lestrrat-go/rasql` and needs no `replace` line.

## Write `rasql.json`

`rasql codegen generate` reads its settings from `rasql.json` at the module root. Create it:

```json
{
  "package": "store",
  "output": "internal/store",
  "dialect": "postgresql"
}
```

Three settings cover this application. `package` is the Go package name to generate, `output` is the directory to write it into relative to the module root, and `dialect` is the engine the DSN will point at.

The DSN is deliberately not one of them. A connection string carries a password and differs between every developer and every environment, so it stays on the command line or in the environment, and the file that goes into version control holds only the parts that are the same for everybody.

## Generate

Every other setting comes from the file, so the command needs only the database to read:

```sh
rasql codegen generate -dsn "$TASKBOARD_DSN"
```

```text
wrote internal/store from 3 tables
```

```text
internal/store/
  members_gen.go
  projects_gen.go
  tasks_gen.go
  schema_gen.go
  schema_gen_test.go
```

One file per table, plus two the whole package shares.

## What one table file holds

`members_gen.go` is the shortest of the three, and it has every part the other two do. It opens with the row struct:

```go
type MembersRow struct {
	ID   int64
	Name string
}
```

Two required columns, two plain fields. Chapter 1 predicted that: nothing in this schema is nullable, so nothing here is a pointer.

Below it comes the table type and one method per column:

```go
// MembersTable is the generated table type for the "members" table.
type MembersTable struct {
	rasql.Table[MembersRow]
}

// ID returns a reference to the "id" column.
func (t MembersTable) ID() query.ColumnRef { return rasql.ColumnOf(t.Table, "id") }

// Name returns a reference to the "name" column.
func (t MembersTable) Name() query.ColumnRef { return rasql.ColumnOf(t.Table, "name") }

// Members returns the descriptor for the "members" table.
func Members() MembersTable {
	return membersTable
}
```

Those accessors are the reason chapter 5's queries never spell a column as a string. `members.Name()` is a `query.ColumnRef` already bound to the `members` table; `members.Nmae()` does not compile.

The rest of the file is scanning support and the relationships. `members` is referenced by `tasks`, so the generator gives it the other side of that link:

```go
// Tasks returns the generated relationship descriptor.
func (t MembersTable) Tasks() MembersTableTasksRelation {
	child := Tasks()
	parent := t
	return MembersTableTasksRelation{Parent: parent, Child: child, ParentKey: parent.ID(), ChildKey: child.AssigneeID()}
}

// Join returns an INNER JOIN for the relationship.
func (r MembersTableTasksRelation) Join() query.Join {
	return rasql.InnerJoin(r.Child, query.Equal(r.ParentKey, r.ChildKey))
}
```

`tasks_gen.go` carries the matching pair in the other direction, `TasksTable.Assignee()` and `TasksTable.Project()`, each returning the parent row for a task. Both exist because both foreign keys sit on required columns, which is the second thing [chapter 1](01-design.md#which-parts-of-rasql-this-uses) predicted and [chapter 2](02-database.md#the-assignee-required-or-nullable) explained the cost of.

## What the shared files hold

`schema_gen.go` holds the descriptor each table type is built from. It is the whole of chapter 2's argument, written back out as Go:

```text
var tasksDef = schema.TableDef{
	Name: "tasks",
	Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}, Identity: schema.IdentityAlways},
		{Name: "project_id", Type: schema.IntegerType{}},
		{Name: "assignee_id", Type: schema.IntegerType{}},
		{Name: "title", Type: schema.TextType{}},
		{Name: "is_open", Type: schema.BooleanType{}, Default: "true"},
		{Name: "created_at", Type: schema.TimeType{}, Default: "now()"},
	},
	PrimaryKey: []string{"id"},
	Indexes: []schema.IndexDef{
		{Name: "tasks_open_by_project", Columns: []string{"project_id", "id"}, Predicate: "is_open"},
	},
	ForeignKeys: []schema.ForeignKeyDef{
		{Name: "tasks_assignee_id_fkey", Columns: []string{"assignee_id"}, ReferencedTable: "members", ReferencedColumns: []string{"id"}, OnDelete: schema.NoAction, OnUpdate: schema.NoAction},
		{Name: "tasks_project_id_fkey", Columns: []string{"project_id"}, ReferencedTable: "projects", ReferencedColumns: []string{"id"}, OnDelete: schema.Cascade, OnUpdate: schema.NoAction},
	},
	Relationships: []schema.RelationshipDef{
		{Name: "Assignee", Kind: schema.RelationshipBelongsTo, Columns: []string{"assignee_id"}, ReferencedTable: "members", ReferencedColumns: []string{"id"}},
		{Name: "Project", Kind: schema.RelationshipBelongsTo, Columns: []string{"project_id"}, ReferencedTable: "projects", ReferencedColumns: []string{"id"}},
	},
}
```

`Identity: schema.IdentityAlways` is on `id`, so the write path in chapter 5 knows to leave that column alone rather than send PostgreSQL a value it will reject. The partial index kept its `Predicate`. The two foreign keys kept their actions, and each turned into a `Relationships` entry, which is where the accessors above come from.

`schema_gen_test.go` validates every descriptor in the package, in four lines of work:

<!-- INCLUDE(sample/taskboard/internal/store/schema_gen_test.go) -->
```go
// Code generated by rasqlgen; DO NOT EDIT.

package store

import (
	"testing"

	"github.com/lestrrat-go/rasql/schema"
)

func TestRasqlgenGeneratedDefinitionsAreValid(t *testing.T) {
	for _, definition := range []schema.TableDef{membersDef, projectsDef, tasksDef} {
		if err := definition.Validate(); err != nil {
			t.Errorf("%s: %s", definition.Name, err)
		}
	}
}
```
source: [sample/taskboard/internal/store/schema_gen_test.go](https://github.com/lestrrat-go/rasql/blob/main/sample/taskboard/internal/store/schema_gen_test.go)
<!-- END INCLUDE -->

Run it, and the package compiles and passes:

```sh
go build ./...
go test ./...
```

```text
ok  	example.com/taskboard/internal/store	0.002s
```

Commit the settings file and the generated package together, since neither means anything without the other:

```sh
git add rasql.json go.mod go.sum internal/store
git commit -m 'generate the store from the schema'
```

## Regenerate from the migrations, not from the database

The store above was generated from `taskboard_walkthrough`, the database chapter 2 shaped by hand. That worked, and it is the wrong habit. A developer who tries something in `psql` and forgets to undo it would generate a store describing their experiment, and nothing would notice.

Generate from a database built by the checked-in migrations instead. Chapter 3 already created one, `taskboard_verify`, and proved it matches. Wrap the two commands so the pairing is not something anybody has to remember. Create `scripts/generate.sh`:

```sh
#!/bin/sh
# Rebuild internal/store from the checked-in migrations.
#
# It applies db/migrations to the database TASKBOARD_SCHEMA_DSN names, then
# runs rasql codegen generate against that database, so the generated store
# describes whatever the migrations build and not whatever a developer last
# typed into psql. Pass -check to report staleness instead of writing:
#
#   ./scripts/generate.sh
#   ./scripts/generate.sh -check
set -eu
dsn="${TASKBOARD_SCHEMA_DSN:?set TASKBOARD_SCHEMA_DSN to a schema database this script may rebuild}"
rasql migrate apply -dir db/migrations -dialect postgresql -dsn "$dsn"
exec rasql codegen generate -dsn "$dsn" "$@"
```

`-check` is passed straight through to `rasql codegen generate`, which then reports whether the checked-in package is current instead of writing it. Export the schema database's DSN, point the script at it, and ask:

```sh
chmod +x scripts/generate.sh
export TASKBOARD_SCHEMA_DSN='postgres://rasql:rasql@127.0.0.1:5432/taskboard_verify?sslmode=disable'
./scripts/generate.sh -check
```

```text
migration apply completed: 0 applied
internal/store is up to date
```

Nothing was pending, because chapter 3 already applied the migration to that database. The generated package matches, byte for byte, what the migrations produce.

The `export` is what lets every later call be the bare `./scripts/generate.sh` that chapters 7 and 8 write. The script reads `TASKBOARD_SCHEMA_DSN` from its own environment and stops with the message its `:?` line spells out when the variable is unset, so a fresh shell exports it again before it runs the script.

That is the check worth running in CI, and it is what chapter 7 leans on: after a schema change, `./scripts/generate.sh` rewrites the store from the new migrations, and the compiler reports every place the old shape was assumed.

```sh
git add scripts/generate.sh
git commit -m 'rebuild the store from the migrations'
```

## Next

[Build the repository](05-queries.md).
