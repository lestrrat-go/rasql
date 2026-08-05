# Contributing to rasql

This page covers the local development workflow. For how to use `rasql` as a library, start with the [README](README.md) and [docs/](docs/).

## Running the test suite

```sh
go test ./...
go vet ./...
```

Most of the suite needs no database. It also needs no Docker: any test that does need a live database skips cleanly when neither a DSN nor Docker is available (see below), so `go test ./...` always passes on a machine with nothing set up.

## Live database tests

A handful of tests run against a real PostgreSQL or MySQL server rather than a mock, such as `TestDatabaseIntegration` at the repository root and the privilege tests in `inspect/`. Any package can add one: `internal/dbtest` gives a test in any package a live `*sql.DB` or an already-parsed connection config (`*pgx.ConnConfig` / `*mysql.Config`) for PostgreSQL and MySQL, resolved in this order:

1. **`RASQL_TEST_POSTGRES_DSN` / `RASQL_TEST_MYSQL_DSN` environment variables.** If set to a non-blank value, that value is parsed with the driver's own parser (`pgx.ParseConfig` / `mysql.ParseDSN`) and Docker is never touched. This is how CI's `integration` job runs the suite. Whatever the driver parses is accepted as-is, including a DSN that leaves its target to libpq's `PG*` environment variables or a driver default -- there is no separate check that it names its own host, port, and database. Safety does not depend on that: see "Containment" below. A DSN that fails to parse fails the test rather than silently falling back to Docker; see the `dbtest` package doc for why. `internal/dbtest` never hands back a raw DSN string, only the parsed config, so nothing in this repository rebuilds or reparses a connection string.

   Whichever DSN you supply, `internal/dbtest` creates a fresh database (a schema, for MySQL) for the run and drops it afterward, so the credentials in the DSN must be able to `CREATE DATABASE` and `DROP DATABASE`, not merely read and write inside one that already exists. For PostgreSQL, a cluster owner such as `POSTGRES_USER` already can. For MySQL, the official image's `MYSQL_USER`/`MYSQL_PASSWORD` account is granted privileges scoped to `MYSQL_DATABASE` only and cannot create a new schema; a `RASQL_TEST_MYSQL_DSN` you supply needs an account with broader rights, such as `root` with `MYSQL_ROOT_PASSWORD` (CI and `compose.yaml` both do this). A DSN whose credentials cannot create a database fails the test loudly rather than silently falling back, naming the variable to fix; see `internal/dbtest/mysql.go`. See "Privileges a non-superuser DSN needs" below for the full, precise list.
2. **Docker Compose**, otherwise. The harness runs `docker compose up -d --wait` against the checked-in [`compose.yaml`](compose.yaml), which defines the same two services CI uses (`postgres:17-alpine` on port 5432, `mysql:8.4` on port 3306, same credentials), and derives the config from those fixed ports. You can also bring these up yourself ahead of time:

   ```sh
   docker compose up -d --wait
   ```

   Only the `docker compose` plugin is supported, not the standalone `docker-compose` binary.

3. **Skip**, otherwise. If Docker is not usable on your machine (no `docker` binary, the `compose` subcommand not available, or the daemon unreachable, including a socket you don't have permission to use), the test skips with a message naming which of those was detected, plus how to set the DSN or start compose yourself.

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

`internal/dbtest`, and every test file that imports it, builds only on unix (`//go:build unix`). Its bring-up lock (`internal/dbtest/lock_unix.go`) has no portable equivalent on other platforms, and an earlier no-op fallback let two `go test ./...` binaries race the same `docker compose up`, producing a container-name conflict or a "network already exists" error that would fail the test loudly and blame a broken compose file that was never broken. `GOOS=windows go build ./...` and `go vet ./...` still succeed: the package and its consumers are simply excluded from that build, the same as any other platform-restricted package.

### No automatic teardown

The harness never stops the containers it starts, and there is no environment variable to opt into it doing so. `go test ./...` builds and runs every package as a separate test binary and runs them in parallel, so several packages can reach the Docker Compose step at the same moment in different processes; an automatic `docker compose down` in one package's cleanup would tear down a database another package's tests are still using. Stop the containers yourself once you are done:

```sh
docker compose down -v
```

### Skip vs. fail

Docker being unusable on your machine is treated as an environment fact, not a rasql defect, so it produces a skip. A `docker compose up` that fails *after* Docker has already been confirmed reachable is treated differently: it fails the test loudly, carrying Docker's own message, instead of skipping. There is no attempt to classify why it failed -- a broken compose file, a broken image reference, and a host port (5432 or 3306) already bound by something else all fail the same way.

If you already run a PostgreSQL or MySQL server locally on one of those ports, that loud failure is expected: set `RASQL_TEST_POSTGRES_DSN` or `RASQL_TEST_MYSQL_DSN` to point at the database you already have, and the harness never touches Docker at all.
