#!/bin/sh
# Apply the checked-in migrations to the runtime database the application
# serves from. Set TASKBOARD_DSN to migrate a database other than taskboard.db.
set -eu

root=$(CDPATH='' cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

go run ../../cmd/rasql migrate apply \
  -dir migrations \
  -dialect sqlite \
  -dsn "${TASKBOARD_DSN:-taskboard.db}"
