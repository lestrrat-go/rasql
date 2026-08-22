# 2. Shape the database

[Chapter 1](01-design.md) named three tables and said what each one holds. It did not say what type any column has, how a primary key gets its value, which of PostgreSQL's two timestamps to store, or what a delete does to the rows that point at the deleted one. This chapter settles all of that.

It settles it by trying each choice on a running PostgreSQL server and reading back what the server says, and then by asking rasql's own inspection what it makes of the result. Both halves matter. A column PostgreSQL accepts happily can still come back through rasql's inspection with something missing, and knowing which facts survive that trip is what decides where the schema of record lives.

Every transcript below is what the server printed. The engine was PostgreSQL 17.10.

## Start the project

Chapter 1 wrote nothing down, so there is no project yet. Make the directory the application lives in, put it under git, and give it a Go module path:

```sh
mkdir taskboard
cd taskboard
git init
go mod init example.com/taskboard
```

Every command from here to the end of the walkthrough runs in that directory.

Write chapter 1's description of the application into `README.md`, so the first file a reader opens says what this is:

```text
# Taskboard

A team runs projects, each project holds tasks, and every task is owned by one
member of the team. One HTML page lists the open tasks grouped by project with
the owner's name, offers a form to add a task, and offers a button to close one.

The database is PostgreSQL. The application is built on
[rasql](https://github.com/lestrrat-go/rasql).
```

Chapter 6 comes back to that README and adds the commands that run the finished application.

## Get rasql

rasql is a library and a command, and the walkthrough uses both. The inspection program later in this chapter imports the library, and so does the code chapter 4 generates. Chapter 3 applies migrations with the command, and chapter 4 runs the generator with it.

Clone the repository beside the project directory, so both come out of one checkout:

```sh
git clone https://github.com/lestrrat-go/rasql ../rasql
```

Name the library in `go.mod` and redirect it at that checkout, which sits one level up under its own name:

```text
require github.com/lestrrat-go/rasql v0.0.0

replace github.com/lestrrat-go/rasql => ../rasql
```

Run `go mod tidy` any time before chapter 4 generates code and it takes that `require` line straight back out, because nothing in the project imports rasql yet. Leave the line alone until then.

Put the command on the PATH:

```sh
go install github.com/lestrrat-go/rasql/cmd/rasql@latest
```

That line was not run for this walkthrough. [Chapter 3](03-capture.md#build-rasql-from-a-checkout) says what was run in its place, and why.

Commit the two files that now exist:

```sh
git add README.md go.mod
git commit -m 'start the taskboard project'
```

## Start PostgreSQL

The walkthrough runs PostgreSQL in a container. Start one under podman:

```sh
podman run -d --name rasql-postgres \
  -e POSTGRES_USER=rasql \
  -e POSTGRES_PASSWORD=rasql \
  -e POSTGRES_DB=rasql \
  -p 5432:5432 \
  docker.io/library/postgres:17-alpine
```

That `podman run` is the command that creates the container, and it is the one command on this page that was not run for the walkthrough: the server used here was already up, which is what the `Up 2 days` below reports. Everything after it is a transcript.

Check that it is listening:

```sh
podman ps --format '{{.Names}}\t{{.Image}}\t{{.Status}}\t{{.Ports}}'
```

```text
rasql-postgres	docker.io/library/postgres:17-alpine	Up 2 days	0.0.0.0:5432->5432/tcp
```

Ask the server what it is:

```sh
podman exec rasql-postgres psql -U rasql -d postgres -c 'SELECT version();'
```

```text
                                         version
------------------------------------------------------------------------------------------
 PostgreSQL 17.10 on x86_64-pc-linux-musl, compiled by gcc (Alpine 15.2.0) 15.2.0, 64-bit
(1 row)
```

## Create a database for this application

The image creates a `rasql` database of its own on first start. Leave it alone and make one for this application, so that dropping and rebuilding the working database never touches anything else on the server:

```sh
podman exec rasql-postgres psql -U rasql -d postgres -c 'CREATE DATABASE rasql_taskboard;'
```

Export its connection string as `TASKBOARD_DSN`. That is the name every later chapter reads it under, and chapter 3's first `rasql` call already expects it:

```sh
export TASKBOARD_DSN='postgres://rasql:rasql@127.0.0.1:5432/rasql_taskboard?sslmode=disable'
```

The export lives in the shell that ran it, so a new terminal needs the same line again.

## Give the psql calls a shorter name

Every command in this chapter reaches `psql` through the same long `podman exec` prefix. Put that prefix in a script once, so the rest of the chapter is the SQL and not the plumbing. Make the directory the project's scripts live in:

```sh
mkdir -p scripts
```

Create `scripts/psql.sh`:

```sh
#!/bin/sh
# Open psql on the working database inside the container started in chapter 02.
# Every argument is passed straight through to psql, so both of these work:
#
#   ./scripts/psql.sh
#   ./scripts/psql.sh -c '\d tasks'
set -eu
exec podman exec -i "${TASKBOARD_CONTAINER:-rasql-postgres}" \
	psql -U rasql -d "${TASKBOARD_DATABASE:-rasql_taskboard}" -v ON_ERROR_STOP=1 "$@"
```

Make it executable and commit it:

```sh
chmod +x scripts/psql.sh
git add scripts/psql.sh
git commit -m 'add a psql helper for the working database'
```

`-v ON_ERROR_STOP=1` is what makes a failing statement stop the run instead of letting the next one go ahead against a half-built table.

## How each choice below is checked

Every choice below is checked twice. Try it on the server and read back what PostgreSQL made of it, then ask rasql's inspection what descriptor it produces for the result, because that descriptor is what the code generator in chapter 4 reads.

The transcripts labelled *rasql descriptor* come from a throwaway program written for this chapter and thrown away at the end of it. It imports the rasql the module named above, opens the working database through pgx's `database/sql` driver, builds an inspector with `inspect.New(db, dialect.PostgreSQL())`, calls `Table` for the table being examined, prints the returned `schema.TableDef` as indented JSON, and then feeds that descriptor straight back into `render.CreateTable` and `render.CreateIndexes` to see what DDL rasql would rebuild it from. The finished application does none of this. The program is a way of looking at the database, and it is thrown away with the probe tables.

## The primary key: identity or serial

All three tables want a machine-assigned integer key. PostgreSQL offers two spellings. `serial` is the older one, and `generated always as identity` is the one the SQL standard defines. Create one of each:

```sql
CREATE TABLE probe_serial (id serial PRIMARY KEY, name text NOT NULL);
CREATE TABLE probe_identity (id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY, name text NOT NULL);
```

They look almost the same from `\d`:

```text
                            Table "public.probe_serial"
 Column |  Type   | Collation | Nullable |                 Default
--------+---------+-----------+----------+------------------------------------------
 id     | integer |           | not null | nextval('probe_serial_id_seq'::regclass)
 name   | text    |           | not null |
Indexes:
    "probe_serial_pkey" PRIMARY KEY, btree (id)

                     Table "public.probe_identity"
 Column |  Type   | Collation | Nullable |           Default
--------+---------+-----------+----------+------------------------------
 id     | integer |           | not null | generated always as identity
 name   | text    |           | not null |
Indexes:
    "probe_identity_pkey" PRIMARY KEY, btree (id)
```

The catalog shows where they differ. `serial` is a column default that calls a sequence; identity is a property of the column itself, recorded in `pg_attribute.attidentity`, with no default at all:

```sql
SELECT c.relname AS table, a.attname AS column, a.attidentity, a.attnotnull,
       pg_get_expr(d.adbin, d.adrelid) AS column_default
FROM pg_attribute a
JOIN pg_class c ON c.oid = a.attrelid
LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
WHERE c.relname IN ('probe_serial','probe_identity') AND a.attnum > 0
ORDER BY c.relname, a.attnum;
```

```text
     table      | column | attidentity | attnotnull |              column_default
----------------+--------+-------------+------------+------------------------------------------
 probe_identity | id     | a           | t          |
 probe_identity | name   |             | t          |
 probe_serial   | id     |             | t          | nextval('probe_serial_id_seq'::regclass)
 probe_serial   | name   |             | t          |
(4 rows)
```

Both leave a sequence behind:

```text
                 List of relations
 Schema |         Name          |   Type   | Owner
--------+-----------------------+----------+-------
 public | probe_identity_id_seq | sequence | rasql
 public | probe_serial_id_seq   | sequence | rasql
(2 rows)
```

The difference that decides it is what happens when application code supplies its own `id`. A `serial` column takes it:

```sql
INSERT INTO probe_serial (id, name) VALUES (100, 'explicit');
```

An identity column declared `generated always` refuses it:

```sql
INSERT INTO probe_identity (id, name) VALUES (100, 'explicit');
```

```text
ERROR:  cannot insert a non-DEFAULT value into column "id"
DETAIL:  Column "id" is an identity column defined as GENERATED ALWAYS.
HINT:  Use OVERRIDING SYSTEM VALUE to override.
```

That refusal is the point. The accepted insert left the sequence untouched, so the next generated key starts from the bottom while row 100 already exists:

```sql
INSERT INTO probe_serial (name) VALUES ('generated') RETURNING id;
SELECT last_value FROM probe_serial_id_seq;
```

```text
 id
----
  1
(1 row)

 last_value
------------
          1
(1 row)
```

The table now holds `id` 100 and `id` 1, and the sequence is at 1. It will keep counting up into keys nobody has used, and one day it will reach 100. Bring that day forward by repeating the whole thing with 2 in place of 100. Drop the probe and build it again inside a transaction, and roll that transaction back, which puts the table and its two rows back for the sections below.

```sql
BEGIN;
DROP TABLE probe_serial;
CREATE TABLE probe_serial (id serial PRIMARY KEY, name text NOT NULL);
INSERT INTO probe_serial (id, name) VALUES (2, 'explicit');
INSERT INTO probe_serial (name) VALUES ('first generated') RETURNING id;
INSERT INTO probe_serial (name) VALUES ('second generated') RETURNING id;
ROLLBACK;
```

```text
 id
----
  1
(1 row)

ERROR:  duplicate key value violates unique constraint "probe_serial_pkey"
DETAIL:  Key (id)=(2) already exists.
```

The failure lands on the second generated insert, an unknown number of rows after the explicit one that caused it. `serial` lets an application walk into that. `generated always as identity` stops it at the insert that tried.

Taskboard uses `generated always as identity`.

**rasql descriptor.** Neither spelling survives inspection intact, and they fail differently. For `probe_serial`, rasql records the sequence call verbatim as the column default:

```text
{
  "Name": "id",
  "Type": { "DisplayWidth": null, "Kind": "integer", "Unsigned": false, "ZeroFill": false },
  "Nullable": false,
  "Default": "nextval('probe_serial_id_seq'::regclass)"
}
```

For `probe_identity`, the identity property has nowhere to go, so the column arrives as a plain required integer:

```text
{
  "Name": "id",
  "Type": { "DisplayWidth": null, "Kind": "integer", "Unsigned": false, "ZeroFill": false },
  "Nullable": false,
  "Default": ""
}
```

Rendering each descriptor back into DDL shows what that costs:

```text
CREATE TABLE "probe_serial" ("id" BIGINT NOT NULL DEFAULT nextval('probe_serial_id_seq'::regclass), "name" TEXT NOT NULL, PRIMARY KEY ("id"))
CREATE TABLE "probe_identity" ("id" BIGINT NOT NULL, "name" TEXT NOT NULL, PRIMARY KEY ("id"))
```

The rebuilt `probe_serial` points at a sequence named after a different table. The rebuilt `probe_identity` has lost its key generation entirely. Neither statement is the table it came from, and that is the first sign of where this chapter ends up: rasql's descriptor is enough to generate Go against a schema, and it is not the schema. The checked-in migration SQL is.

## Integer width

The descriptors above turned `integer` into `BIGINT`. Check whether that is a quirk of the primary key or the rule:

```sql
CREATE TABLE probe_int (small smallint NOT NULL, plain integer NOT NULL, big bigint NOT NULL);
```

**rasql descriptor.** All three columns come back as the same integer kind, and re-render as the same type:

```text
CREATE TABLE "probe_int" ("small" BIGINT NOT NULL, "plain" BIGINT NOT NULL, "big" BIGINT NOT NULL)
```

rasql has one integer kind and no column width on it, so `smallint`, `integer`, and `bigint` are the same thing to it. Running chapter 4's generator over this same probe table confirms what that means in Go:

```go
type ProbeIntRow struct {
	Small int64
	Plain int64
	Big   int64
}
```

Declaring `bigint` in the database is the one choice of the three where the column and the Go field it becomes agree about the range, so Taskboard declares `bigint` and keeps them in step.

## Text: `text` or `varchar(n)`

A task title and a member name are both short human strings. PostgreSQL has three ways to hold one:

```sql
CREATE TABLE probe_text (a text NOT NULL, b varchar(40) NOT NULL, c char(40) NOT NULL);
```

```text
                    Table "public.probe_text"
 Column |         Type          | Collation | Nullable | Default
--------+-----------------------+-----------+----------+---------
 a      | text                  |           | not null |
 b      | character varying(40) |           | not null |
 c      | character(40)         |           | not null |
```

`varchar(n)` enforces the limit, and enforces it by rejecting the row:

```sql
INSERT INTO probe_text (a,b,c) VALUES (repeat('x',80), repeat('x',80), 'short');
```

```text
ERROR:  value too long for type character varying(40)
```

`text` takes the same 80 characters without comment, and `char(40)` pads what it is given out to its full width on disk:

```sql
INSERT INTO probe_text (a,b,c) VALUES (repeat('x',80), 'ok', 'short');
SELECT pg_column_size(a) AS text_bytes, pg_column_size(c) AS char_bytes, octet_length(c) AS char_octets FROM probe_text;
```

```text
 text_bytes | char_bytes | char_octets
------------+------------+-------------
         81 |         41 |          40
(1 row)
```

Five characters occupy forty octets in the `char(40)` column, which is why `char(n)` is not in the running.

Between the other two, the question is whether a limit is worth an error. The limit buys no space, because PostgreSQL stores the same string at the same size in both:

```sql
BEGIN;
CREATE TABLE probe_storage (a text NOT NULL, b varchar(40) NOT NULL);
INSERT INTO probe_storage VALUES ('a short title', 'a short title');
SELECT pg_column_size(a) AS text_bytes, pg_column_size(b) AS varchar_bytes FROM probe_storage;
ROLLBACK;
```

```text
 text_bytes | varchar_bytes
------------+---------------
         14 |            14
(1 row)
```

What the limit buys is a rejected insert when a title runs long. A limit the application does not also enforce in its own form handling turns a typo into a 500, and Taskboard's page has no length rule of its own to enforce. Taskboard uses `text`.

**rasql descriptor.** All three round-trip exactly. rasql records the stated width and whether the column is fixed-width, and rebuilds each declaration unchanged:

```text
{ "Name": "a", "Type": { "Fixed": false, "Kind": "text", "Width": null }, ... }
{ "Name": "b", "Type": { "Fixed": false, "Kind": "text", "Width": 40 }, ... }
{ "Name": "c", "Type": { "Fixed": true,  "Kind": "text", "Width": 40 }, ... }
```

```text
CREATE TABLE "probe_text" ("a" TEXT NOT NULL, "b" VARCHAR(40) NOT NULL, "c" CHAR(40) NOT NULL)
```

This choice costs nothing either way at the rasql layer. It is decided entirely by what the application wants PostgreSQL to do.

## Time: `timestamptz` or `timestamp`

`tasks.created_at` records a moment. PostgreSQL's two candidates differ in whether that moment is anchored:

```sql
CREATE TABLE probe_time (naive timestamp NOT NULL, aware timestamptz NOT NULL);
```

The server's own time zone is UTC:

```text
 TimeZone
----------
 UTC
(1 row)
```

Write the same instant into both columns from a session running in Tokyo:

```sql
SET TimeZone = 'Asia/Tokyo';
INSERT INTO probe_time (naive, aware) VALUES (now(), now());
SELECT naive, aware FROM probe_time;
```

```text
           naive            |             aware
----------------------------+-------------------------------
 2026-08-21 16:37:09.259319 | 2026-08-21 16:37:09.259319+09
(1 row)
```

Now read the same row from a session in New York, and then from one in UTC:

```sql
SET TimeZone = 'America/New_York';
SELECT naive, aware FROM probe_time;
SET TimeZone = 'UTC';
SELECT naive, aware FROM probe_time;
```

```text
           naive            |             aware
----------------------------+-------------------------------
 2026-08-21 16:37:09.259319 | 2026-08-21 03:37:09.259319-04
(1 row)

           naive            |             aware
----------------------------+-------------------------------
 2026-08-21 16:37:09.259319 | 2026-08-21 07:37:09.259319+00
(1 row)
```

The `timestamptz` column names one instant and reports it in whatever zone the reader is sitting in. The `timestamp` column reports 16:37 to everybody, which was correct in Tokyo and is wrong by thirteen hours in New York. It stored a wall-clock reading and threw away which clock it was read from.

Taskboard uses `timestamptz`.

**rasql descriptor.** rasql has one time kind, and both columns land on it:

```text
{ "Name": "naive", "Type": { "Kind": "time" }, "Nullable": false, "Default": "" }
{ "Name": "aware", "Type": { "Kind": "time" }, "Nullable": false, "Default": "" }
```

Rendering the descriptor back makes the consequence plain:

```text
CREATE TABLE "probe_time" ("naive" TIMESTAMPTZ NOT NULL, "aware" TIMESTAMPTZ NOT NULL)
```

rasql cannot tell the two apart, and the one type it renders is `TIMESTAMPTZ`. A column declared `timestamp` therefore has a descriptor that describes a different column. Choosing `timestamptz` keeps the descriptor honest, on top of being the type that stores what the application actually means.

## The assignee: required or nullable

Chapter 1 decided that a version-one task always has an owner. This is where that decision gets checked against what the alternative would look like, because chapter 7 comes back and reverses it.

Build the three tables in probe form with a nullable `assignee_id`, and file one task with nobody on it:

```sql
CREATE TABLE probe_members (
  id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL
);
CREATE TABLE probe_projects (
  id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL
);
CREATE TABLE probe_tasks (
  id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_id integer NOT NULL REFERENCES probe_projects (id) ON DELETE CASCADE,
  assignee_id integer REFERENCES probe_members (id) ON DELETE SET NULL,
  title text NOT NULL,
  is_open boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);
INSERT INTO probe_members (name) VALUES ('Ada'), ('Grace');
INSERT INTO probe_projects (name) VALUES ('Website'), ('Billing');
INSERT INTO probe_tasks (project_id, assignee_id, title)
VALUES (1, 1, 'Write the copy'), (1, NULL, 'Pick a font'), (2, 2, 'Reconcile invoices');
SELECT * FROM probe_tasks ORDER BY id;
```

```text
 id | project_id | assignee_id |       title        | is_open |          created_at
----+------------+-------------+--------------------+---------+-------------------------------
  1 |          1 |           1 | Write the copy     | t       | 2026-08-21 07:37:27.194794+00
  2 |          1 |             | Pick a font        | t       | 2026-08-21 07:37:27.194794+00
  3 |          2 |           2 | Reconcile invoices | t       | 2026-08-21 07:37:27.194794+00
(3 rows)
```

A required column refuses that second row outright:

```sql
CREATE TABLE probe_tasks_strict (
  id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  assignee_id integer NOT NULL REFERENCES probe_members (id),
  title text NOT NULL
);
INSERT INTO probe_tasks_strict (assignee_id, title) VALUES (NULL, 'Pick a font');
```

```text
ERROR:  null value in column "assignee_id" of relation "probe_tasks_strict" violates not-null constraint
DETAIL:  Failing row contains (1, null, Pick a font).
```

The cost of allowing the empty value lands on every query that joins to `members`. The page's read is a join, and an inner join silently drops the unowned task:

```sql
SELECT t.id, t.title, m.name AS assignee
FROM probe_tasks t
JOIN probe_members m ON m.id = t.assignee_id
ORDER BY t.id;
```

```text
 id |       title        | assignee
----+--------------------+----------
  1 | Write the copy     | Ada
  3 | Reconcile invoices | Grace
(2 rows)
```

A left join keeps it:

```sql
SELECT t.id, t.title, m.name AS assignee
FROM probe_tasks t
LEFT JOIN probe_members m ON m.id = t.assignee_id
ORDER BY t.id;
```

```text
 id |       title        | assignee
----+--------------------+----------
  1 | Write the copy     | Ada
  2 | Pick a font        |
  3 | Reconcile invoices | Grace
(3 rows)
```

Two rows against three, from the same table, with the difference invisible in the output unless somebody already knows a row is missing. That is the whole argument for keeping version one required: a required column makes the wrong join impossible instead of merely wrong.

Taskboard declares `assignee_id bigint NOT NULL`. Chapter 7 relaxes it, and the compile errors that follow are that chapter's subject.

**rasql descriptor.** rasql records nullability faithfully in both directions. The probe's nullable column reports:

```text
{
  "Name": "assignee_id",
  "Type": { "DisplayWidth": null, "Kind": "integer", "Unsigned": false, "ZeroFill": false },
  "Nullable": true,
  "Default": ""
}
```

and the required one reports `"Nullable": false`. This is the fact chapter 4's generator reads to decide between an `int64` field and a `*int64` field, so it is one that has to survive the trip, and it does.

## The foreign keys: what a delete does

Chapter 1 wanted two different answers here. A deleted project should take its tasks with it. Deleting a member should be refused while that member still owns tasks, because those tasks would otherwise point at a row that is gone.

PostgreSQL's default, written by omitting the clause, is `NO ACTION`, and it refuses the delete:

```sql
CREATE TABLE probe_tasks_noaction (
  id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_id integer NOT NULL REFERENCES probe_projects (id),
  title text NOT NULL
);
INSERT INTO probe_tasks_noaction (project_id, title) VALUES (1, 'Write the copy');
DELETE FROM probe_projects WHERE id = 1;
```

```text
ERROR:  update or delete on table "probe_projects" violates foreign key constraint "probe_tasks_noaction_project_id_fkey" on table "probe_tasks_noaction"
DETAIL:  Key (id)=(1) is still referenced from table "probe_tasks_noaction".
```

`ON DELETE SET NULL` and `ON DELETE CASCADE` are what `probe_tasks` was built with above. Run both deletes inside a transaction and roll it back, so the probe rows survive for the next section:

```sql
BEGIN;
DROP TABLE probe_tasks_noaction;
DELETE FROM probe_members WHERE id = 1;
SELECT id, title, assignee_id FROM probe_tasks ORDER BY id;
DELETE FROM probe_projects WHERE id = 1;
SELECT id, title, project_id FROM probe_tasks ORDER BY id;
ROLLBACK;
```

```text
 id |       title        | assignee_id
----+--------------------+-------------
  1 | Write the copy     |
  2 | Pick a font        |
  3 | Reconcile invoices |           2
(3 rows)

 id |       title        | project_id
----+--------------------+------------
  3 | Reconcile invoices |          2
(1 row)
```

Deleting Ada emptied her tasks' `assignee_id` and left the rows. Deleting the Website project removed both of its tasks. Both clauses do exactly what they say.

`SET NULL` is out for Taskboard, because `assignee_id` is required. That combination is worth trying rather than assuming, because PostgreSQL accepts the declaration:

```sql
BEGIN;
CREATE TABLE probe_setnull_notnull (
  id integer GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  assignee_id integer NOT NULL REFERENCES probe_members (id) ON DELETE SET NULL,
  title text NOT NULL
);
INSERT INTO probe_setnull_notnull (assignee_id, title) VALUES (1, 'Write the copy');
DELETE FROM probe_members WHERE id = 1;
ROLLBACK;
```

```text
ERROR:  null value in column "assignee_id" of relation "probe_setnull_notnull" violates not-null constraint
DETAIL:  Failing row contains (1, null, Write the copy).
CONTEXT:  SQL statement "UPDATE ONLY "public"."probe_setnull_notnull" SET "assignee_id" = NULL WHERE $1 OPERATOR(pg_catalog.=) "assignee_id""
```

The `CREATE TABLE` succeeds and the constraint sits there looking correct until the day somebody deletes a member. Writing the two constraints down together is what makes that visible: a `SET NULL` clause is a promise that the column can hold nothing.

Taskboard gives `project_id` `ON DELETE CASCADE` and `assignee_id` `ON DELETE NO ACTION`.

**rasql descriptor.** Inspecting the settled `tasks` table built at the end of this chapter shows rasql recording both actions:

```text
"ForeignKeys": [
  {
    "Name": "tasks_assignee_id_fkey",
    "Columns": [ "assignee_id" ],
    "ReferencedSchema": "",
    "ReferencedTable": "members",
    "ReferencedColumns": [ "id" ],
    "OnDelete": "NO ACTION",
    "OnUpdate": "NO ACTION"
  },
  {
    "Name": "tasks_project_id_fkey",
    "Columns": [ "project_id" ],
    "ReferencedSchema": "",
    "ReferencedTable": "projects",
    "ReferencedColumns": [ "id" ],
    "OnDelete": "CASCADE",
    "OnUpdate": "NO ACTION"
  }
]
```

```text
CONSTRAINT "tasks_assignee_id_fkey" FOREIGN KEY ("assignee_id") REFERENCES "members" ("id") ON DELETE NO ACTION ON UPDATE NO ACTION,
CONSTRAINT "tasks_project_id_fkey" FOREIGN KEY ("project_id") REFERENCES "projects" ("id") ON DELETE CASCADE ON UPDATE NO ACTION
```

Foreign keys survive inspection whole, which matters beyond DDL: chapter 4's generator turns each one it can support into a relationship accessor on the generated table type.

## The index: over the open tasks or over all of them

The page reads open tasks by project. Fill the probe table with enough rows for the planner to have an opinion, and build the two candidate indexes side by side:

```sql
INSERT INTO probe_tasks (project_id, assignee_id, title, is_open)
SELECT 1 + (g % 2),
       CASE WHEN g % 5 = 0 THEN NULL ELSE 1 + (g % 2) END,
       'Task ' || g,
       g % 20 = 0
FROM generate_series(1, 20000) AS g;
CREATE INDEX probe_tasks_by_project ON probe_tasks (project_id, id);
CREATE INDEX probe_tasks_open_by_project ON probe_tasks (project_id, id) WHERE is_open;
ANALYZE probe_tasks;
```

One in twenty tasks is open, which is roughly the ratio a real board settles at:

```sql
SELECT count(*) FILTER (WHERE is_open) AS open, count(*) AS total FROM probe_tasks;
```

```text
 open | total
------+-------
 1003 | 20003
(1 row)
```

The two indexes cover the same columns and cost very different amounts:

```sql
SELECT indexrelname, pg_size_pretty(pg_relation_size(indexrelid)) AS size
FROM pg_stat_user_indexes WHERE relname = 'probe_tasks' ORDER BY indexrelname;
```

```text
        indexrelname         |  size
-----------------------------+--------
 probe_tasks_by_project      | 456 kB
 probe_tasks_open_by_project | 40 kB
 probe_tasks_pkey            | 456 kB
(3 rows)
```

The partial index is more than ten times smaller, because it holds only the rows the page ever asks for, and it stays that size as closed tasks pile up. The planner picks it for the page's query with both available:

```sql
EXPLAIN (ANALYZE, COSTS OFF, TIMING OFF, SUMMARY OFF)
SELECT id, title FROM probe_tasks WHERE is_open AND project_id = 1 ORDER BY id;
```

```text
 Sort (actual rows=1002 loops=1)
   Sort Key: id
   Sort Method: quicksort  Memory: 56kB
   ->  Bitmap Heap Scan on probe_tasks (actual rows=1002 loops=1)
         Recheck Cond: ((project_id = 1) AND is_open)
         Heap Blocks: exact=148
         ->  Bitmap Index Scan on probe_tasks_open_by_project (actual rows=1002 loops=1)
               Index Cond: (project_id = 1)
(8 rows)
```

The narrowness is the catch as well as the point. A partial index serves only queries whose predicate the planner can prove implies the index's own. Drop the full index and ask for tasks without mentioning `is_open`:

```sql
BEGIN;
DROP INDEX probe_tasks_by_project;
EXPLAIN (COSTS OFF) SELECT id, title FROM probe_tasks WHERE project_id = 1 ORDER BY id;
ROLLBACK;
```

```text
 Index Scan using probe_tasks_pkey on probe_tasks
   Filter: (project_id = 1)
(2 rows)
```

The partial index sat there unused and the query fell back to the primary key. Taskboard has exactly one read of this table and it always filters on `is_open`, so the partial index is the right one and the full one is not worth carrying.

Taskboard builds `tasks_open_by_project` over `(project_id, id)` where `is_open`.

**rasql descriptor.** Inspecting the settled `tasks` table shows rasql recording the predicate:

```text
"Indexes": [
  {
    "Name": "tasks_open_by_project",
    "Columns": [ "project_id", "id" ],
    "Unique": false,
    "Predicate": "is_open"
  }
]
```

and then declines to build DDL from it:

```text
render.CreateIndexes failed: render postgresql: index "tasks_open_by_project" has predicate "is_open", which rasql can describe but not yet render
```

That error is the clearest statement in this chapter of where the line runs. rasql reads the partial index, keeps it in the descriptor, and reports it to anything that asks. It will not write the `CREATE INDEX` back out. That index has to be created by SQL somebody wrote.

## Clear the probes away

Every table above was scaffolding. Drop all of it, so the working database is empty before the real shape goes in:

```sql
DROP TABLE IF EXISTS probe_tasks, probe_tasks_strict, probe_members, probe_projects,
  probe_serial, probe_identity, probe_text, probe_time, probe_int CASCADE;
DROP TABLE IF EXISTS probe_tasks_noaction;
```

Confirm nothing is left:

```sh
./scripts/psql.sh -c '\dt'
```

```text
Did not find any relations.
```

## Write the settled shape to `db/shape.sql`

Make the directory the project's SQL lives in:

```sh
mkdir -p db
```

Write the settled shape down as `db/shape.sql`:

```sql
-- The shape settled in chapter 02, exactly as it was run against the
-- working database. Re-run this file to rebuild that database from empty.

CREATE TABLE members (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL
);

CREATE TABLE projects (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  name text NOT NULL
);

CREATE TABLE tasks (
  id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
  project_id bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
  assignee_id bigint NOT NULL REFERENCES members (id) ON DELETE NO ACTION,
  title text NOT NULL,
  is_open boolean NOT NULL DEFAULT true,
  created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX tasks_open_by_project ON tasks (project_id, id) WHERE is_open;
```

Run it:

```sh
./scripts/psql.sh -f - < db/shape.sql
```

Read the result back:

```sh
./scripts/psql.sh -c '\d members' -c '\d projects' -c '\d tasks'
```

```text
                        Table "public.members"
 Column |  Type  | Collation | Nullable |           Default
--------+--------+-----------+----------+------------------------------
 id     | bigint |           | not null | generated always as identity
 name   | text   |           | not null |
Indexes:
    "members_pkey" PRIMARY KEY, btree (id)
Referenced by:
    TABLE "tasks" CONSTRAINT "tasks_assignee_id_fkey" FOREIGN KEY (assignee_id) REFERENCES members(id)

                        Table "public.projects"
 Column |  Type  | Collation | Nullable |           Default
--------+--------+-----------+----------+------------------------------
 id     | bigint |           | not null | generated always as identity
 name   | text   |           | not null |
Indexes:
    "projects_pkey" PRIMARY KEY, btree (id)
Referenced by:
    TABLE "tasks" CONSTRAINT "tasks_project_id_fkey" FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE

                                     Table "public.tasks"
   Column    |           Type           | Collation | Nullable |           Default
-------------+--------------------------+-----------+----------+------------------------------
 id          | bigint                   |           | not null | generated always as identity
 project_id  | bigint                   |           | not null |
 assignee_id | bigint                   |           | not null |
 title       | text                     |           | not null |
 is_open     | boolean                  |           | not null | true
 created_at  | timestamp with time zone |           | not null | now()
Indexes:
    "tasks_pkey" PRIMARY KEY, btree (id)
    "tasks_open_by_project" btree (project_id, id) WHERE is_open
Foreign-key constraints:
    "tasks_assignee_id_fkey" FOREIGN KEY (assignee_id) REFERENCES members(id)
    "tasks_project_id_fkey" FOREIGN KEY (project_id) REFERENCES projects(id) ON DELETE CASCADE
```

The `ON DELETE NO ACTION` that `db/shape.sql` spells out does not appear beside `tasks_assignee_id_fkey`. `NO ACTION` is what a foreign key does when no clause names something else, and `\d` prints only the clauses that depart from it. Writing it out anyway is worth the redundancy: the next reader of that file learns that the omission was a decision rather than an oversight.

Commit the shape:

```sh
git add db/shape.sql
git commit -m 'record the schema shape settled in psql'
```

## What rasql's inspection keeps, and what it drops

Three of this chapter's decisions come back through rasql's inspection exactly as they went in. Nullability does, so the generator can tell a required column from an optional one. Both foreign keys do, actions included, so the generator can turn them into relationship accessors. Text width and fixedness do, so a `varchar(40)` stays a `varchar(40)`.

Three do not.

The identity property is gone, so a descriptor rebuilt into DDL would create a `tasks` table whose `id` nobody assigns.

The integer width is gone, along with the difference between `timestamp` and `timestamptz`, because rasql has one integer kind and one time kind. Taskboard's `bigint` and `timestamptz` were chosen so that the one type rasql renders is the one the database already has.

The partial index predicate is recorded and then refused at render time, with an error that says so in as many words.

None of those losses is a problem, because the descriptor has one job: it is the input the code generator reads. The schema of record is the SQL, and that SQL has to be checked in, ordered, and applied the same way to every database that runs this application.

`db/shape.sql` is not that yet. It is a file that only works against an empty database, with nothing recording whether it has been run. [Chapter 3](03-capture.md) turns it into a migration.

## Next

[Capture the schema into a migration](03-capture.md).
