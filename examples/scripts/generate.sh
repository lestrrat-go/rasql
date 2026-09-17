#!/bin/sh
# Rebuild examples/store from examples/db/migrations, and pass every argument
# straight through to the generator:
#
#   ./examples/scripts/generate.sh
#   ./examples/scripts/generate.sh -timeout 2m
#
# -scratch creates a throwaway SQLite database, applies db/migrations to it,
# reads its catalog, and drops it, so the run needs no server and no DSN. The
# command writes every _gen.go file and rasql.sum under examples/store.
#
# sample/taskboard is a separate module and builds the command into a temporary
# directory first. This directory is part of the rasql module itself, so
# `go run` reaches cmd/rasql directly.
set -eu
# Run from the directory rasql.json sits in, because output and migrations
# resolve against it.
cd "$(dirname "$0")/.."
exec go run ../cmd/rasql codegen generate -config rasql.json -scratch "$@"
