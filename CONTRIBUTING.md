# Contributing to rasql

This page covers the local development workflow. For how to use `rasql` as a library, start with the [README](README.md) and [docs/](docs/).

## Running the test suite

```sh
go test ./...
go vet ./...
```

Most of the suite needs no database. It also needs no Docker: any test that does need a live database skips cleanly when its DSN is not set (see below), so `go test ./...` always passes on a machine with nothing set up.

## Code in the documentation

Every Go block in `README.md` and `docs/` is copied from a file the compiler and `go test` see, and `TestDocGoBlocksComeFromExamples` fails on one that is written by hand. A block is delimited by include markers and its body is generated, so never edit the body itself:

```
<!-- INCLUDE(examples/rasql_hook_example_test.go#hook) -->
<!-- END INCLUDE -->
```

A target naming a file alone includes that whole file. A target ending in `#name` includes only the region an example marks with `// BEGIN(name)` and `// END(name)`, shifted to the left margin, which is how a short passage shows a few lines instead of a whole example. `TestDocRegionsAreIncluded` fails on a region no page includes, so the markers and the pages stay in step.

Write the example first, under `examples/`, as an `Example*` function with an `// Output:` block. Then add the markers and fill every block:

```sh
go test ./examples/ -update-docs
```

The same flag rewrites the checked-in generated files the documentation shows. `examples/store/users_gen.go`, `examples/store/schema_gen.go`, `examples/store/schema_gen_test.go`, and `examples/store/user_by_email_gen.go` are checked by `TestGeneratedStoreIsCurrent`, which plans a `generate.Store` over the same table and query the files were generated from, reads the directory itself rather than a hardcoded file list, and requires the two to name the same generated files with the same bytes. `user_by_email_gen.go` is checked a second time by `TestGeneratedQueryIsCurrent`, which runs the documented `rasqlgen query` command itself and requires the source it writes to match both the store's plan and the checked-in file, so a change to that command's flag handling or output wiring cannot pass on the strength of the library path alone. It also rewrites the files under `examples/schemasource/internal/store`, which no page shows but `TestSchemaSourceExampleGenerates` checks the same way, by running the schemasource example through its own `go:generate` directive and then comparing the whole directory, not just `users_gen.go`. That directive overwrites its output unconditionally, so the test snapshots the directory before running it and compares that snapshot against the store's plan as well: the snapshot is what tells it whether the *checked-in* source is stale, while the directory the directive just wrote is what tells it whether the documented `go:generate` path still agrees with `generate.Store`. It then restores the snapshot, so a stale example never leaves the working tree holding output the test did not check. `examples/bootstrapsource/internal/tables/users_gen.go`, `examples/bootstrapsource/internal/tables/tables_gen.go`, and `examples/bootstrapsource/internal/tables/hints.go` are shown by `docs/06-rasqlgen.md` and checked the same way by `TestBootstrapSourceExampleGenerates`, by running the bootstrapsource example's own `go:generate` directive against a throwaway SQLite database it creates itself. `hints.go` is the one file among those that `rasqlgen bootstrap` itself never rewrites once it exists, but the example's own `gen/main.go` removes the whole output directory before every run, so it is created fresh by `-update-docs` exactly like the other two.

The five files under `sample/taskboard/internal/store` are generated too. `TestTaskboardStoreIsCurrent`, in the root module, checks them against a `generate.Store` built from a throwaway in-process SQLite database, but it never writes into that directory, not even under `-update-docs`: that module regenerates itself with its own `go generate`, and a generator change must still refresh it manually, in the same commit, the way the next section describes.

### Generated files outside the root module

`sample/taskboard` is a separate module with its own checked-in `rasqlgen` output, and nothing in `go test ./...` regenerates it. `TestTaskboardStoreIsCurrent` does check it, from the root module, by building a throwaway SQLite database from every migration under `sample/taskboard/migrations/sqlite` and sweeping it with `catalog.FromDatabase`, so a change to the generator that leaves `sample/taskboard/internal/store/{members,projects,tasks}_gen.go`, `schema_gen.go`, or `schema_gen_test.go` stale now fails a fully green root test run instead of passing silently. It reads the migration tree with `internal/migrationdir` and applies it with `migrate.Runner`, which is what `rasqlmigrate apply` does, so adding a migration directory does not leave the test pinning the schema the module used to have. That test only compares, though; it never writes there. Refresh the checked-in files themselves in the same commit:

```sh
cd sample/taskboard && go generate ./...
```

That runs `sample/taskboard/gen/main.go`, which applies `sample/taskboard/migrations/sqlite` to a throwaway SQLite database and regenerates the store; `sample/taskboard/internal/store/.taskboard-schema.db` is ignored by Git. Then run the sample module's own build and tests, which the root `go test ./...` never reaches:

```sh
cd sample/taskboard && go build ./... && go test ./...
```

## Live database tests

A handful of tests run against a real PostgreSQL or MySQL server rather than a mock, such as `TestDatabaseIntegration` at the repository root and the privilege tests in `inspect/`. Any package can add one: `internal/dbtest` gives a test in any package a live `*sql.DB` or an already-parsed connection config (`*pgx.ConnConfig` / `*mysql.Config`) for PostgreSQL and MySQL, resolved a single way:

Set `RASQL_TEST_POSTGRES_DSN` / `RASQL_TEST_MYSQL_DSN` to a non-blank value, and that value is parsed with the driver's own parser (`pgx.ParseConfig` / `mysql.ParseDSN`). Whatever the driver parses is accepted as-is, including a DSN that leaves its target to libpq's `PG*` environment variables or a driver default -- there is no separate check that it names its own host, port, and database. Safety does not depend on that: see "Containment" below. A DSN that fails to parse fails the test rather than skipping; see the `dbtest` package doc for why. `internal/dbtest` never hands back a raw DSN string, only the parsed config, so nothing in this repository rebuilds or reparses a connection string.

If the relevant variable is not set, the test skips with a message naming exactly what to run: bring up the checked-in [`compose.yaml`](compose.yaml) and export the DSN it defines.

```sh
docker compose up -d --wait
```

This is exactly what CI's `integration` job does, as a workflow step before the test step runs; `internal/dbtest` itself never touches Docker. Only the `docker compose` plugin is supported, not the standalone `docker-compose` binary, and it must be at least v2.1.1, where `up --wait` arrived -- an older plugin fails with a confusing flag-usage error instead of a version complaint, so if you hit that, upgrade the plugin.

### Reading the integration job's log

The `integration` job runs `go test -v` scoped to exactly the packages that hold a test guarded by `internal/dbtest` (currently the repository root, `catalog`, `cli/rasqlgen`, `generate`, and `inspect`), rather than a plain `go test ./...`. Without `-v`, a skipped live test and an executed one both leave the same `ok  github.com/lestrrat-go/rasql/inspect  0.336s` package line, so the log cannot tell you which happened. With `-v` naming every test in those packages, a passing live test shows as `--- PASS: TestPostgreSQLInspectorReadsTableNamesAgainstLiveDatabase`, and a live test that skipped because a DSN was unset shows as `--- SKIP:` with the same name -- search the log for the specific test name to confirm it ran rather than skipped. The rest of the module still runs, unchanged and without `-v`, in the `check` job.

Both jobs reach a live test only by expanding package patterns, so a test file in a directory the go tool passes over runs in neither of them. `go help packages` names the directories no pattern reaches at all: any whose name begins with `.` or `_`, plus `testdata`. `TestIntegrationJobListsEveryDBTestGuardedPackage` cannot rescue a file in one of those either, since no package list it could ask for would reach it.

A `vendor` directory is a different case, and the rule is narrower than "go ignores vendor". `go help packages` says that "any slash-separated pattern element containing a wildcard never participates in a match of the `vendor` element in the path of a vendored package, so that `./...` does not match packages in subdirectories of `./vendor` or `./mycode/vendor`, but `./vendor/...` and `./mycode/vendor/...` do." So the patterns both jobs actually write skip what is under vendor, while a pattern naming vendor outright would reach and run the tests there. The same paragraph adds that a directory named `vendor` holding code of its own is not a vendored package at all, so an ordinary wildcard does match that one. At the module root there is a second gate on top of all of this: under `-mod=vendor`, `go list ./vendor/...` rejects a directory `vendor/modules.txt` does not list. Put live tests in ordinary package directories all the same, and leave `vendor` to `go mod vendor`.

If you already run a PostgreSQL or MySQL server locally, `docker compose up -d --wait` fails on whichever service's port (5432 for `postgres`, 3306 for `mysql`) is already bound; the other service still comes up fine. Either bring up only the service you actually need (`docker compose up -d --wait postgres`), or skip Docker for that database entirely and point `RASQL_TEST_POSTGRES_DSN` / `RASQL_TEST_MYSQL_DSN` at the server you already have running.

Whichever DSN you supply, `internal/dbtest` creates a fresh database (a schema, for MySQL) for the run and drops it afterward, so the credentials in the DSN must be able to `CREATE DATABASE` and `DROP DATABASE`, not merely read and write inside one that already exists. For PostgreSQL, a cluster owner such as `POSTGRES_USER` already can. For MySQL, the official image's `MYSQL_USER`/`MYSQL_PASSWORD` account is granted privileges scoped to `MYSQL_DATABASE` only and cannot create a new schema; a `RASQL_TEST_MYSQL_DSN` you supply needs an account with broader rights, such as `root` with `MYSQL_ROOT_PASSWORD` (CI and `compose.yaml` both do this). A DSN whose credentials cannot create a database fails the test loudly rather than silently skipping, naming the variable to fix; see `internal/dbtest/mysql.go`. See "Privileges a non-superuser DSN needs" below for the full, precise list.

### Containment

`internal/dbtest` does not try to prove where a supplied DSN's text points -- an earlier version attempted that and it required reimplementing pieces of each driver's own parser this package cannot call. Instead, safety comes from containment: every live test can only touch objects it created itself -- its own fresh database, dropped in cleanup, plus per-run-unique tables and roles/users created with `dbtest.UniqueName`. A DSN that happens to point somewhere unexpected still only gets a throwaway database created and dropped in it; it is up to you not to point `RASQL_TEST_POSTGRES_DSN` / `RASQL_TEST_MYSQL_DSN` at a server you are not willing to have a database created and dropped on.

### Privileges a non-superuser DSN needs

CI's own accounts don't exercise this: `RASQL_TEST_POSTGRES_DSN` uses `POSTGRES_USER`, the cluster's bootstrap role, which is a superuser, and `RASQL_TEST_MYSQL_DSN` uses `root`. If you supply a DSN for a more restricted account, here is what it genuinely needs.

**PostgreSQL:**

- `CREATEDB`, to create this package's own fresh per-run database.
- `CREATEROLE`, which the `inspect` privilege tests (`inspect/postgresql_privilege_test.go`) need for `CREATE ROLE ... LOGIN PASSWORD`.
- Enough privilege to run `DROP OWNED BY` on a role the same connection just created with `CREATEROLE`. This needs to be stronger than plain `CREATEROLE`: PostgreSQL's `CREATE ROLE` grants a non-superuser `CREATEROLE` creator `ADMIN OPTION` on the role it creates automatically, but not `INHERIT` (PostgreSQL source, `src/backend/commands/user.c`, `CreateRole`'s `poptself.admin = true; poptself.inherit = false;`), and `DROP OWNED BY` is gated on `has_privs_of_role()`, which only follows `pg_auth_members` grants with `inherit_option = true` (`src/backend/utils/adt/acl.c`, `has_privs_of_role` / `roles_is_member_of`). So on PostgreSQL 16 and later, a plain `CREATEROLE` account cannot run `DROP OWNED BY` on a role it just created; only a superuser can (which is why CI never exposes this).
- No grant is needed for `CREATE` on the working schema: creating the database makes the connecting role its owner, and PostgreSQL 15+ grants `public.CREATE` to `pg_database_owner`.
- A non-privilege requirement: `openAsRole` in `inspect/postgresql_privilege_test.go` connects as the freshly created role using the role name as its own password, so the server's `pg_hba.conf` must accept password authentication for newly created roles.

**MySQL:**

- Global `CREATE` and `DROP` on `*.*`. The per-run schema name is unpredictable (see `dbtest.UniqueName`), so a schema-scoped grant cannot cover it.
- `SELECT`, `INSERT`, `UPDATE`, and table-level `CREATE`/`DROP` inside that schema.
- Nothing needs `SUPER`, `RELOAD`, `PROCESS`, or `GRANT OPTION`.

### Unix only

`internal/dbtest`, and every test file that imports it, builds only on unix (`//go:build unix`). Nothing left in the package is itself unix-specific; the constraint predates this package's single-DSN-path form and stays as-is rather than being relitigated here. `GOOS=windows go build ./...` and `go vet ./...` still succeed: the package and its consumers are simply excluded from that build, the same as any other platform-restricted package.

### No automatic teardown

Nothing in this repository stops the containers `docker compose up` starts, and there is no environment variable to opt into it doing so. `go test ./...` builds and runs every package as a separate test binary and runs them in parallel, so several packages can be using the same containers at once; an automatic `docker compose down` in one package's cleanup would tear down a database another package's tests are still using. Stop the containers yourself once you are done:

```sh
docker compose down -v
```

### Skip vs. fail

A DSN variable left unset is treated as an environment fact, not a rasql defect, so it produces a skip naming exactly what to run (see "Live database tests" above). Once a DSN is set, resolution fails the test loudly instead of skipping for anything else that goes wrong: a value the driver cannot parse, or credentials that cannot `CREATE DATABASE`. There is no silent fallback once you have told `internal/dbtest` where to connect.
