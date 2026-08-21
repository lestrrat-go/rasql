#!/bin/sh
# Rebuild internal/store from the checked-in migrations.
#
# It applies db/migrations to the database TASKBOARD_SCHEMA_DSN names, then
# runs rasql codegen generate against that database, so the generated store
# describes whatever the migrations build and not whatever a developer last
# typed into psql. Pass -check to report staleness instead of writing:
#
#   ./scripts/generate.sh
#   ./scripts/generate.sh -check
set -eu
# Run from the module root, so `go generate ./...` reaches the same migrations
# and writes the same package as a run started here by hand.
cd "$(dirname "$0")/.."
dsn="${TASKBOARD_SCHEMA_DSN:?set TASKBOARD_SCHEMA_DSN to a schema database this script may rebuild}"
./scripts/rasql.sh migrate apply -dir db/migrations -dialect postgresql -dsn "$dsn"
exec ./scripts/rasql.sh codegen generate -dsn "$dsn" "$@"
