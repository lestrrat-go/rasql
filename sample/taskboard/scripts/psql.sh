#!/bin/sh
# Open psql on the working database inside the container started in chapter 02.
# Every argument is passed straight through to psql, so both of these work:
#
#   ./scripts/psql.sh
#   ./scripts/psql.sh -c '\d tasks'
set -eu
exec podman exec -i "${TASKBOARD_CONTAINER:-rasql-postgres}" \
	psql -U rasql -d "${TASKBOARD_DATABASE:-taskboard_walkthrough}" -v ON_ERROR_STOP=1 "$@"
