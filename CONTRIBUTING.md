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

The same flag rewrites the checked-in generated files the documentation shows, `examples/store/users_gen.go`, `examples/store/schema_gen.go`, `examples/store/schema_gen_test.go`, and `examples/store/user_by_email_gen.go`, which `TestGeneratedStoreIsCurrent` and `TestGeneratedQueryIsCurrent` otherwise fail on when `rasqlgen` output changes. It also rewrites the files under `examples/schemasource/internal/store`, which no page shows but `TestSchemaSourceExampleGenerates` checks the same way, by running the schemasource example through its own `go:generate` directive, and `examples/bootstrapsource/internal/tables/users_gen.go`, `examples/bootstrapsource/internal/tables/tables_gen.go`, and `examples/bootstrapsource/internal/tables/hints.go`, which `docs/06-rasqlgen.md` shows and `TestBootstrapSourceExampleGenerates` checks the same way, by running the bootstrapsource example's own `go:generate` directive against a throwaway SQLite database it creates itself. `hints.go` is the one file among those that `rasqlgen bootstrap` itself never rewrites once it exists, but the example's own `gen/main.go` removes the whole output directory before every run, so it is created fresh by `-update-docs` exactly like the other two. The five files under `sample/taskboard/internal/store` are generated too, and no test checks them, so a generator change must also run `rm -f sample/taskboard/internal/store/.taskboard-schema.db && cd sample/taskboard/internal/store && go generate ./...`, which applies the SQLite migrations to a throwaway database and regenerates from it.

### Generated files outside the root module

`sample/taskboard` is a separate module with its own checked-in `rasqlgen` output, and nothing in `go test ./...` regenerates or checks it. A change to the generator therefore leaves `sample/taskboard/internal/store/{members,projects,tasks}_gen.go`, `schema_gen.go`, and `schema_gen_test.go` stale with a fully green root test run. Refresh them in the same commit:

```sh
cd sample/taskboard/internal/store && go generate ./...
```

That applies `sample/taskboard/migrations/sqlite` to a throwaway SQLite database and reruns `rasqlgen schema` against it; the database file is gitignored. Then run the sample module's own build and tests, which the root `go test ./...` never reaches:

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
