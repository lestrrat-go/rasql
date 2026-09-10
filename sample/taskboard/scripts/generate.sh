#!/bin/sh
# Rebuild internal/store from the database TASKBOARD_DSN names, after
# applying db/migrations to it.
set -eu
cd "$(dirname "$0")/.."
dsn="${TASKBOARD_DSN:?set TASKBOARD_DSN to the taskboard connection string}"
./scripts/migrate.sh apply
exec ./scripts/rasql.sh codegen generate -dsn "$dsn" "$@"
