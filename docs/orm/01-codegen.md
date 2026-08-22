# `rasql codegen`

`rasql codegen` writes the typed store package from live database metadata. It
reads that metadata with `catalog.FromDatabase` and writes deterministic Go
source through `generate.Store`. The standalone `rasqlgen` binary accepts the
same command under its own name.

There is one command, `generate`, and one settings file. A project needs no
generator program of its own.

## Run the command

Run it from the module root whenever the source database changes:

```sh
go get github.com/lestrrat-go/rasql/cmd/rasql@latest
go run github.com/lestrrat-go/rasql/cmd/rasql codegen generate -dsn "$DATABASE_URL"
```

The command opens the database itself, so the project needs no driver import
of its own for generation.

Add `-check` to report whether the checked-in package is current instead of
writing it, which is what a CI job runs:

```sh
go run github.com/lestrrat-go/rasql/cmd/rasql codegen generate -dsn "$DATABASE_URL" -check
```

A `go:generate` directive in a hand-written file of the generated package puts
the same run behind `go generate ./...`. The Taskboard sample does this from
`internal/store/repository.go`, through a script that first applies its
migrations to the database it then generates from. See `sample/taskboard`.

## The settings file

Everything that stays the same from run to run lives in `rasql.json` at the
module root. Write it once and check it in:

```json
{
  "package": "store",
  "output": "internal/store",
  "dialect": "sqlite",
  "prune": true,
  "tables": {
    "exclude": ["audit_log"],
    "history_table": "schema_migrations",
    "row_names": {"users": "User"}
  },
  "queries": [
    {"input": "queries/user_by_email.sql", "function": "UserByEmail"},
    {"sql": "SELECT count(*) FROM users", "function": "CountUsers"}
  ]
}
```

`package` names the generated package and `output` names its directory,
resolved against the module root unless `root` names a different base.
`dialect` is `postgresql` (or `postgres`), `mysql`, or `sqlite`.

`tables.include` names the only tables to generate, and `tables.exclude` names
tables to skip. A sweep otherwise covers every visible base table.
`tables.history_table` names the migration history table to skip when it is
not `rasql_schema_migrations`. `tables.row_names` overrides a generated row
type: the generator derives `UsersRow` from a `users` table on its own, and
`"users": "User"` makes it `User` instead. State one when the derived name
reads badly, and when it collides with another table's generated names, which
refuses the run.

`queries` compiles static SQL templates into generated functions beside the
table code. Each entry names the `function` to generate and states its
template in exactly one of two places. Naming an `input` file, resolved
against the same root, keeps the template in a file an editor, a formatter and
a query runner all read as SQL, and holds a multi-line statement as the lines
it was written as. Writing the template into `sql` instead keeps a short query
in the settings file, at the cost of escaping the quotes each `{{bind "name"}}`
action needs. Stating both is refused rather than resolved by precedence.

An entry may also name the `output` file. Leaving that out derives it from the
input, so `queries/user_by_email.sql` becomes `user_by_email_gen.go`, and
derives it from the function for an entry stating `sql`, so `CountUsers`
becomes `count_users_gen.go`.

A `{{bind "name"}}` action may also name a column, as `{{bind "name"
users.email}}`: the generated parameter's Go type then comes from that
column's descriptor instead of `any`. The table must be one this run
generates, so a `tables.include` or `tables.exclude` that leaves it out makes
the reference an error rather than an untyped parameter.

A template held in `input` is read again before the run writes anything, so an
edit made while a run was in flight is caught rather than committed around. A
template held in `sql` is already in hand, so nothing has to be re-read.

`prune` lets a run delete a generated file it no longer writes, such as the
per-table file of a dropped table. Setting it to `false` refuses the run and
names the file instead.

A key the file does not define is refused rather than ignored, so a
misspelling is a message rather than a setting that silently does nothing.

## What stays on the command line

`-dsn` is never read from the settings file, because that file is checked in
and a connection string carries a credential. Keep it in an environment
variable or a secret store.

`-check` stays a flag too, since it selects what one run does rather than what
the project is, and so does `-timeout`, which bounds the whole run and
defaults to 30 seconds.

`-config` reads a settings file somewhere other than `rasql.json` at the
module root. A project with no settings file at all is fine, as long as the
flags say everything.

Every setting in the file has a matching flag: `-package`, `-output`, `-root`,
`-dialect`, `-include`, `-exclude`, `-history-table`, and `-prune`. A flag you
type wins over the file, so a one-off run needs no edit to it. The two list
flags take comma-separated names.

## Next

[The generated store](02-generated-store.md) says what the command writes and
what each generated member is for. [Typed queries](03-typed-queries.md) reads
rows through the generated table.
