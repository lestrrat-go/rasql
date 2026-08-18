#!/bin/sh
# Rebuild the throwaway schema database from the checked-in migrations, then
# run the owned generator over it. Pass -check to report whether the checked-in
# store is current instead of writing it.
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

schema_dsn=internal/store/.taskboard-schema.db
rm -f "$schema_dsn" "$schema_dsn-wal" "$schema_dsn-shm"

go run ../../cmd/rasql migrate apply \
  -dir migrations/sqlite \
  -dialect sqlite \
  -dsn "$schema_dsn"

TASKBOARD_SCHEMA_DSN="$schema_dsn" go run ./gen "$@"
