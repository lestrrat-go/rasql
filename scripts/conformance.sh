#!/usr/bin/env bash
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

module_version() {
	go list -m -f '{{if .Replace}}{{.Replace.Version}}{{else}}{{.Version}}{{end}}' "$1"
}

export RASQL_CONFORMANCE_COMMIT="${RASQL_CONFORMANCE_COMMIT:-$(git rev-parse HEAD)}"
export RASQL_CONFORMANCE_SQLITE_DRIVER_VERSION="${RASQL_CONFORMANCE_SQLITE_DRIVER_VERSION:-$(module_version modernc.org/sqlite)}"
export RASQL_CONFORMANCE_POSTGRESQL_DRIVER_VERSION="${RASQL_CONFORMANCE_POSTGRESQL_DRIVER_VERSION:-$(module_version github.com/jackc/pgx/v5)}"
export RASQL_CONFORMANCE_MYSQL_DRIVER_VERSION="${RASQL_CONFORMANCE_MYSQL_DRIVER_VERSION:-$(module_version github.com/go-sql-driver/mysql)}"

count_matches() {
	local pattern=$1
	local file=$2
	local output
	if output=$(grep -Ec -- "$pattern" "$file"); then
		printf '%s' "$output"
		return 0
	else
		local status=$?
		if [[ "$status" -eq 1 ]]; then
			printf '0'
			return 0
		fi
		return "$status"
	fi
}

if [[ "$#" -ne 1 ]]; then
	echo "usage: $0 sqlite|live" >&2
	exit 2
fi

normalize_path() {
	local path=$1
	if [[ "$path" != /* ]]; then
		path="$root/$path"
	fi
	printf '%s' "$path"
}

validate_artifact() {
	local output=$1 engines=$2
	RASQL_CONFORMANCE_OUTPUT="$output" \
	RASQL_CONFORMANCE_EXPECTED_ENGINES="$engines" \
	go test -count=1 ./internal/conformance -run '^TestValidateConformanceArtifact$'
}

case "$1" in
sqlite)
	unset RASQL_TEST_POSTGRES_DSN RASQL_TEST_MYSQL_DSN
	output=$(normalize_path "${RASQL_CONFORMANCE_OUTPUT:-.tmp/d4-conformance-sqlite.json}")
	log=$(normalize_path "${RASQL_CONFORMANCE_LOG:-.tmp/d4-conformance-sqlite.log}")
	export RASQL_CONFORMANCE_OUTPUT="$output" RASQL_CONFORMANCE_LOG="$log"
	mkdir -p "$(dirname -- "$output")" "$(dirname -- "$log")"
	rm -f -- "$output"
	go test -count=1 -v ./internal/conformance -run '^TestConformanceSQLite$' 2>&1 | tee "$log"
	validate_artifact "$output" sqlite
	;;
live)
	: "${RASQL_TEST_POSTGRES_DSN:?RASQL_TEST_POSTGRES_DSN is required for live conformance}"
	: "${RASQL_TEST_MYSQL_DSN:?RASQL_TEST_MYSQL_DSN is required for live conformance}"
	output=$(normalize_path "${RASQL_CONFORMANCE_OUTPUT:-.tmp/d4-conformance-live.json}")
	log=$(normalize_path "${RASQL_CONFORMANCE_LOG:-.tmp/d4-conformance-live.log}")
	export RASQL_CONFORMANCE_OUTPUT="$output" RASQL_CONFORMANCE_LOG="$log"
	mkdir -p "$(dirname -- "$output")" "$(dirname -- "$log")"
	rm -f -- "$output"
	go test -count=1 -v ./internal/conformance -run '^TestConformance(PostgreSQL17|MySQL84|SQLite)$' 2>&1 | tee "$log"
	for test in TestConformancePostgreSQL17 TestConformanceMySQL84 TestConformanceSQLite; do
		run_count=$(count_matches "=== RUN   ${test}$" "$log")
		pass_count=$(count_matches "--- PASS: ${test}( |$)" "$log")
		skip_count=$(count_matches "--- SKIP: ${test}( |$)" "$log")
		nested_skip_count=$(count_matches "--- SKIP: ${test}/" "$log")
		if [[ "$run_count" != 1 || "$pass_count" != 1 || "$skip_count" != 0 || "$nested_skip_count" != 0 ]]; then
			echo "live conformance did not prove exactly one passing ${test}" >&2
			exit 1
		fi
	done
	validate_artifact "$output" sqlite,postgresql,mysql
	;;
*)
	echo "usage: $0 sqlite|live" >&2
	exit 2
	;;
esac
