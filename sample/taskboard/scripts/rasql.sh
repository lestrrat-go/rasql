#!/bin/sh
# Run one rasql command, and pass every argument straight through:
#
#   ./scripts/rasql.sh migrate status
#   ./scripts/rasql.sh codegen generate -dsn "$TASKBOARD_DSN"
#
# The walkthrough installs the command with `go install` and calls it by name.
# This copy of the project is checked into the rasql repository itself, two
# directories below its root, so it builds the command from the checkout
# beside it and needs nothing installed. `go run ../../cmd/rasql` would be
# shorter, and it fails: the command's own dependencies are not in this
# module's go.sum, and putting them there would give the sample a dependency
# it never imports.
set -eu
# Run from the module root, so a relative path in the arguments means the same
# thing however the script was reached. `go generate ./...` runs the directive
# in internal/store from that package's own directory.
cd "$(dirname "$0")/.."
workdir=$(mktemp -d)
trap 'rm -rf "$workdir"' EXIT
go build -C ../.. -o "$workdir/rasql" ./cmd/rasql
"$workdir/rasql" "$@"
