#!/usr/bin/env bash
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT
mkdir -p "$work/bin"
cat > "$work/bin/go" <<'GO'
#!/usr/bin/env bash
if [[ "$*" == *"TestValidateConformanceArtifact"* ]]; then
	if [[ "${FAKE_VALIDATE_STATUS:-0}" -ne 0 ]]; then exit "$FAKE_VALIDATE_STATUS"; fi
	[[ -s "${RASQL_CONFORMANCE_OUTPUT:?}" ]] || exit 1
	exit 0
fi
if [[ "${FAKE_WRITE_ARTIFACT:-0}" -eq 1 ]]; then
	printf '%s\n' "$FAKE_ARTIFACT" > "${RASQL_CONFORMANCE_OUTPUT:?}"
fi
printf '%s\n' "$FAKE_GO_OUTPUT"
exit "${FAKE_GO_STATUS:-0}"
GO
chmod +x "$work/bin/go"
cat > "$work/bin/grep" <<'GREP'
#!/usr/bin/env bash
if [[ "${FAKE_GREP_ERROR:-0}" -eq 1 && "$1" == "-Ec" && "$3" == ---* ]]; then exit 2; fi
exec /usr/bin/grep "$@"
GREP
chmod +x "$work/bin/grep"

run_case() {
	local name=$1 expected=$2 output=$3
	if PATH="$work/bin:$PATH" FAKE_ARTIFACT='[]' FAKE_WRITE_ARTIFACT=1 FAKE_GO_OUTPUT="$output" RASQL_TEST_POSTGRES_DSN=pg RASQL_TEST_MYSQL_DSN=my RASQL_CONFORMANCE_LOG="$work/$name.log" RASQL_CONFORMANCE_OUTPUT="$work/$name.json" "$root/scripts/conformance.sh" live >/dev/null 2>&1; then
		status=0
	else
		status=$?
	fi
	if [[ "$expected" == pass && "$status" -ne 0 ]]; then echo "$name unexpectedly failed" >&2; exit 1; fi
	if [[ "$expected" == fail && "$status" -eq 0 ]]; then echo "$name unexpectedly passed" >&2; exit 1; fi
}

passing='=== RUN   TestConformancePostgreSQL17
--- PASS: TestConformancePostgreSQL17 (0.01s)
=== RUN   TestConformanceMySQL84
--- PASS: TestConformanceMySQL84 (0.01s)
=== RUN   TestConformanceSQLite
--- PASS: TestConformanceSQLite (0.01s)'
run_case pass pass "$passing"
run_case missing fail "${passing//$'=== RUN   TestConformanceSQLite\n--- PASS: TestConformanceSQLite (0.01s)'/}"
run_case duplicate fail "$passing"$'\n=== RUN   TestConformanceSQLite\n--- PASS: TestConformanceSQLite (0.01s)'
run_case skip fail "$passing"$'\n--- SKIP: TestConformanceSQLite (missing)'
run_case nested-skip fail "$passing"$'\n--- SKIP: TestConformanceSQLite/single_row_read (missing)'

if PATH="$work/bin:$PATH" FAKE_ARTIFACT='[]' FAKE_WRITE_ARTIFACT=1 FAKE_GO_OUTPUT="$passing" RASQL_TEST_POSTGRES_DSN=pg RASQL_TEST_MYSQL_DSN=my RASQL_CONFORMANCE_LOG="$work/grep-error.log" RASQL_CONFORMANCE_OUTPUT="$work/grep-error.json" "$root/scripts/conformance.sh" live extra >/dev/null 2>&1; then
	echo "extra argument unexpectedly passed" >&2
	exit 1
fi

if PATH="$work/bin:$PATH" FAKE_GREP_ERROR=1 FAKE_ARTIFACT='[]' FAKE_WRITE_ARTIFACT=1 FAKE_GO_OUTPUT="$passing" RASQL_TEST_POSTGRES_DSN=pg RASQL_TEST_MYSQL_DSN=my RASQL_CONFORMANCE_LOG="$work/grep-error.log" RASQL_CONFORMANCE_OUTPUT="$work/grep-error.json" "$root/scripts/conformance.sh" live >/dev/null 2>&1; then
	echo "grep error unexpectedly passed" >&2
	exit 1
fi

if PATH="$work/bin:$PATH" FAKE_VALIDATE_STATUS=1 FAKE_ARTIFACT='[]' FAKE_WRITE_ARTIFACT=1 FAKE_GO_OUTPUT="$passing" RASQL_TEST_POSTGRES_DSN=pg RASQL_TEST_MYSQL_DSN=my RASQL_CONFORMANCE_LOG="$work/artifact.log" RASQL_CONFORMANCE_OUTPUT="$work/artifact.json" "$root/scripts/conformance.sh" live >/dev/null 2>&1; then
	echo "artifact validation unexpectedly passed" >&2
	exit 1
fi

stale="$work/stale.json"
printf '%s\n' '[]' > "$stale"
if PATH="$work/bin:$PATH" FAKE_GO_OUTPUT="$passing" RASQL_TEST_POSTGRES_DSN=pg RASQL_TEST_MYSQL_DSN=my RASQL_CONFORMANCE_LOG="$work/stale.log" RASQL_CONFORMANCE_OUTPUT="$stale" "$root/scripts/conformance.sh" live >/dev/null 2>&1; then
	echo "stale live artifact unexpectedly passed" >&2
	exit 1
fi

sqlite_passing='=== RUN   TestConformanceSQLite
--- PASS: TestConformanceSQLite (0.01s)'
sqlite_stale="$work/stale-sqlite.json"
printf '%s\n' '[]' > "$sqlite_stale"
if PATH="$work/bin:$PATH" FAKE_GO_OUTPUT="$sqlite_passing" RASQL_CONFORMANCE_LOG="$work/stale-sqlite.log" RASQL_CONFORMANCE_OUTPUT="$sqlite_stale" "$root/scripts/conformance.sh" sqlite >/dev/null 2>&1; then
	echo "stale sqlite artifact unexpectedly passed" >&2
	exit 1
fi
