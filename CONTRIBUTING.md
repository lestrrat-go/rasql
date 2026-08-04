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

1. **`RASQL_TEST_POSTGRES_DSN` / `RASQL_TEST_MYSQL_DSN` environment variables.** If set to a non-blank value, that value is parsed with the driver's own parser (`pgx.ParseConfig` / `mysql.ParseDSN`) and Docker is never touched. This is how CI's `integration` job runs the suite. A DSN that parses but does not itself pin down its host, port, and database -- so that the actual target would instead come from libpq's `PG*` environment variables or a driver default -- is rejected, and a DSN that fails to parse or fails that check fails the test rather than silently falling back to Docker; see the `dbtest` package doc for why. `internal/dbtest` never hands back a raw DSN string, only the parsed config, so nothing in this repository rebuilds or reparses a connection string.

   Whichever DSN you supply, `internal/dbtest` creates a fresh database (a schema, for MySQL) for the run and drops it afterward, so the credentials in the DSN must be able to `CREATE DATABASE` and `DROP DATABASE`, not merely read and write inside one that already exists. For PostgreSQL, a cluster owner such as `POSTGRES_USER` already can. For MySQL, the official image's `MYSQL_USER`/`MYSQL_PASSWORD` account is granted privileges scoped to `MYSQL_DATABASE` only and cannot create a new schema; a `RASQL_TEST_MYSQL_DSN` you supply needs an account with broader rights, such as `root` with `MYSQL_ROOT_PASSWORD` (CI and `compose.yaml` both do this). A DSN whose credentials cannot create a database fails the test loudly rather than silently falling back, naming the variable to fix; see `internal/dbtest/mysql.go`.
2. **Docker Compose**, otherwise. The harness runs `docker compose up -d --wait` against the checked-in [`compose.yaml`](compose.yaml), which defines the same two services CI uses (`postgres:17-alpine` on port 5432, `mysql:8.4` on port 3306, same credentials), and derives the config from those fixed ports. You can also bring these up yourself ahead of time:

   ```sh
   docker compose up -d --wait
   ```

3. **Skip**, otherwise. If Docker is not usable on your machine (no `docker` binary, no `docker compose` and no `docker-compose` fallback, or the daemon unreachable), the test skips with a message naming which of those was detected, plus how to set the DSN or start compose yourself.

### Unix only

`internal/dbtest`, and every test file that imports it, builds only on unix (`//go:build unix`). Its bring-up lock (`internal/dbtest/lock_unix.go`) has no portable equivalent on other platforms, and an earlier no-op fallback let two `go test ./...` binaries race the same `docker compose up`, which this package's own failure classifier could not tell apart from a broken compose file. `GOOS=windows go build ./...` and `go vet ./...` still succeed: the package and its consumers are simply excluded from that build, the same as any other platform-restricted package.

### No automatic teardown

The harness never stops the containers it starts, and there is no environment variable to opt into it doing so. `go test ./...` builds and runs every package as a separate test binary and runs them in parallel, so several packages can reach the Docker Compose step at the same moment in different processes; an automatic `docker compose down` in one package's cleanup would tear down a database another package's tests are still using. Stop the containers yourself once you are done:

```sh
docker compose down -v
```

### Skip vs. fail

Docker being unusable on your machine is treated as an environment fact, not a rasql defect, so it produces a skip. A `docker compose up` that fails *after* Docker has already been confirmed reachable is treated differently: it fails the test loudly instead of skipping, because that means the compose file or an image reference is broken rather than that Docker is merely absent.

One failure is carved out of that rule: if port 5432 or 3306 is already bound by something else -- most commonly a PostgreSQL or MySQL server you already have running locally -- that is neither a broken compose file nor a broken image, so it skips instead. The skip message names the conflicting port and the `RASQL_TEST_POSTGRES_DSN` / `RASQL_TEST_MYSQL_DSN` variable to set so the test uses the database you already have running.
