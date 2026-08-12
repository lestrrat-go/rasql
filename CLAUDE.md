# CLAUDE.md

Agent-facing rules for this repository. Detail and procedure live in `CONTRIBUTING.md`; this file states rules only.

## Verifying live database behavior

A claim about what MySQL or PostgreSQL actually does MUST be confirmed against a running server before being reported as verified. `sqlmock` and other fixture tests assert rasql's own output back to itself, never against a real engine, so a passing fixture test proves nothing about real MySQL/PostgreSQL behavior.

Get a live database the single way this repository supports: bring up `compose.yaml` with `docker compose up -d --wait`, then export `RASQL_TEST_POSTGRES_DSN` / `RASQL_TEST_MYSQL_DSN`. `internal/dbtest` starts nothing itself and skips a live test when either variable is unset — this is exactly what CI's `integration` job does. CONTRIBUTING.md's "Live database tests" section owns the full resolution order and containment guarantees; its "Privileges a non-superuser DSN needs" section owns what a non-superuser DSN requires.

`docker compose` MUST be the plugin, never the standalone `docker-compose` binary, at v2.1.1 or later (where `up --wait` arrived).

NEVER report behavior as verified against an engine when only fixture tests ran. Name which engines were actually reachable (e.g. "confirmed against PostgreSQL; MySQL was unreachable").

A new assertion about engine behavior belongs in a live test guarded by `internal/dbtest`, added alongside the existing fixture test rather than replacing it.
