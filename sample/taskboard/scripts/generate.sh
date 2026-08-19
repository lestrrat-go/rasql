#!/bin/sh
# Rebuild the throwaway schema database from the checked-in migrations, then
# generate the store from it. Pass -check to report whether the checked-in
# store is current instead of writing it.
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

schema_dsn=internal/store/.taskboard-schema.db
rm -f "$schema_dsn" "$schema_dsn-wal" "$schema_dsn-shm"

go run ../../cmd/rasql migrate apply \
  -dir migrations \
  -dialect sqlite \
  -dsn "$schema_dsn"

go run ../../cmd/rasql codegen generate \
  -dsn "$schema_dsn" \
  -dialect sqlite \
  -package store \
  -output internal/store \
  "$@"
