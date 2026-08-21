# 1. Design the application

This walkthrough builds Taskboard, a small web application that runs on rasql and PostgreSQL, and it builds it in the order a real project is built. The schema is shaped by hand in a live database, captured from there into a checked-in migration, and only then turned into Go. This first chapter decides what version one of that application is. It runs no commands and writes no code.

## What the application does

A team runs projects. Each project holds tasks. Each task belongs to one project, is owned by one member of the team, and is either still open or already closed.

The application serves one HTML page. That page lists every open task, grouped under the project it belongs to, and prints the owner's name beside each task. Below the list sits a form that adds a task to a project. Beside each task sits a button that closes it.

That is the whole feature set for version one. Editing a title, reopening a closed task, and managing the member list are all left out. Each of them would add a handler and a query without adding an idea the page does not already show.

Chapter 7 changes this design once the application is running: a task gains a due date, and it becomes possible to file a task that nobody owns yet. Leaving that out of version one is deliberate, because the chapter is about what happens to working Go code when the schema underneath it moves.

## The three tables

`members` holds the people who can be given work. A member has an `id` and a `name`, and the name is what the page prints.

`projects` holds the groups the page sorts tasks into. A project has an `id` and a `name`.

`tasks` holds the work itself. A task has an `id`, the `project_id` of the project it sits under, the `assignee_id` of the member who owns it, a `title`, a flag saying whether it is still open, and the moment it was created.

Every column of every table is required in version one. Nothing on this page has a sensible empty value: a task with no title, no project, or no owner is not a task the page can draw.

The creation time earns its column by giving the list a stable order inside each project, and it also forces a decision the next chapter has to settle. PostgreSQL offers two timestamp types that store different things, and the application has to pick one.

## How the tables relate

A task belongs to exactly one project. Deleting a project should take its tasks with it, because a task with no project has nowhere to appear on the page.

A task belongs to exactly one member. Deleting a member who still owns tasks should fail, because those tasks would otherwise be left pointing at a row that is gone, and version one has no way to say "unowned".

Those are two different answers to the same question, and PostgreSQL spells both of them in the foreign key's `on delete` clause. [Chapter 2](02-database.md) settles them by deleting rows on a running server and reporting what happened, rather than by asserting what should happen.

## What the page asks of the database

Drawing the page is one read: every open task, with the project it belongs to and the member who owns it. It joins the three tables, filters on the open flag, and orders its rows so that the grouping the page prints falls straight out of the row order.

Adding a task is one insert. Closing a task is one update of the open flag, addressed by primary key.

A task table grows without bound while the open tasks stay few, so this read is the one query worth an index. An index built over only the open rows is far smaller than one built over every row. Chapter 2 builds both and compares them.

## Which parts of rasql this uses

The schema lives in PostgreSQL, so `rasql migrate` is what puts it there and keeps it there. It applies the checked-in migration directories in order, records a checksum for each one, and reports and reverts what it applied. [Migrations](../../../docs/core/07-migrations.md) describes the format and the commands.

The Go code that talks to those tables is generated rather than written. `rasql codegen generate` connects to a database that already carries the schema, reads its metadata, and writes one file per table holding the row struct, the table type, a column accessor per column, and a relationship accessor for each foreign key it can support. [`rasql codegen`](../../../docs/orm/01-codegen.md) describes the command and its settings file.

The repository code is then built on those generated types. Reads use rasql's typed query builder, which takes column accessors rather than column names, so a misspelled column stops at the Go compiler instead of at the database. Writes go through rasql's typed insert and update.

Two consequences of the design above show up in the generated code, and chapter 4 is where the generator prints them out.

Every column is required, so every field of every generated row struct is a plain Go value rather than a pointer.

Both foreign keys sit on required columns, so both of them get a generated relationship accessor. The page's join to `projects` and its join to `members` are therefore both handed to the repository rather than written out. Chapter 7 is where the second of those two facts stops being true.

## What the rest of the walkthrough does

[Chapter 2](02-database.md) creates the project directory, starts PostgreSQL, and settles every column type, constraint, and index by trying it against the server and reading back what the server and rasql's own inspection report.

Chapter 3 captures that settled shape into a checked-in migration. Chapter 4 adds the codegen settings file and generates the store. Chapter 5 builds the repository on the generated store, and chapter 6 adds the view model and the HTTP layer that draw the page.

Chapter 7 makes the schema change described above, regenerates, and walks the compile errors that fall out into fixed Go. Chapter 8 shows three ways to build on generated code. Chapter 9 covers the tests and the day-to-day migration commands.

Every block of output shown in those chapters is what the command above it actually printed, cut down to the lines that carry something to read. `psql` acknowledges every statement it runs, and a block leaves out the acknowledgements the passage has nothing to say about, such as a bare `CREATE TABLE` or `BEGIN`, so running the commands prints a few more lines than the page shows. Two commands are exceptions, and each of them says so where it appears: the `podman run` that starts the database, and the `go install` that puts a released `rasql` on the PATH, both in chapter 2.

## Next

[Shape the database](02-database.md).
