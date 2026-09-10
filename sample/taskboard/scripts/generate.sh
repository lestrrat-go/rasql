#!/bin/sh
# Rebuild internal/store from checked-in snapshots and query inputs.
set -eu
cd "$(dirname "$0")/.."
unset TASKBOARD_SCHEMA_DSN TASKBOARD_DSN TASKBOARD_TEST_DSN
exec go run github.com/lestrrat-go/rasql/cmd/rasql generate "$@"
