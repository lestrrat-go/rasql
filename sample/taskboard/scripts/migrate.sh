#!/bin/sh
# Run one rasql migrate subcommand against the database TASKBOARD_DSN names:
#
#   ./scripts/migrate.sh apply
#   ./scripts/migrate.sh status
#
# Every argument after the subcommand is passed through, so a run can add
# -dry-run, -steps, or anything else the subcommand takes.
set -eu
cd "$(dirname "$0")/.."
subcommand="${1:?name a rasql migrate subcommand, such as apply or status}"
shift
exec ./scripts/rasql.sh migrate "$subcommand" \
	-dir db/migrations \
	-dialect postgresql \
	-dsn "${TASKBOARD_DSN:?set TASKBOARD_DSN to the taskboard connection string}" \
	"$@"
