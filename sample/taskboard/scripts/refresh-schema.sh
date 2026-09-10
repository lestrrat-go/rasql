#!/bin/sh
set -eu
cd "$(dirname "$0")/.."
dsn="${TASKBOARD_SCHEMA_DSN:?set TASKBOARD_SCHEMA_DSN to a disposable PostgreSQL database}"
./scripts/rasql.sh migrate apply -dir db/migrations -dialect postgresql -dsn "$dsn"
exec env TASKBOARD_SCHEMA_DSN="$dsn" ./scripts/rasql.sh schema update --dsn "$dsn"
