#!/usr/bin/env bash
set -euo pipefail

root=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root"

module=github.com/lestrrat-go/rasql
dbtest="$module/internal/dbtest"
conformance="$module/internal/conformance"

if [[ "$#" -ne 1 ]]; then
	echo "usage: $0 check|live" >&2
	exit 2
fi

# guarded_packages prints the import path of every package in this module
# holding a test that imports internal/dbtest, which is the package that skips
# a live test when its DSN is unset. Deriving the list here is what keeps it
# from being maintained by hand in .github/workflows/ci.yml, where it drifted
# out of step with the tree it describes.
#
# internal/conformance is left out. The D4 matrix it holds runs in a step of
# its own, through scripts/conformance.sh, so listing it here would run that
# matrix a second time.
guarded_packages() {
	go list -f '{{.ImportPath}}{{range .TestImports}} {{.}}{{end}}{{range .XTestImports}} {{.}}{{end}}' ./... |
		awk -v dep="$dbtest" -v skip="$conformance" '
			$1 == skip { next }
			{ for (i = 2; i <= NF; i++) if ($i == dep) { print $1; break } }
		'
}

# Both modes pass -v, -count=1 and -p 1, and each flag answers a way the job
# could otherwise end green having proved nothing.
#
# -v names every test in the log. Without it a live test that skipped for a
# missing DSN and one that ran against a real server leave the same
# package-level "ok" line, so the log cannot say which happened. See
# CONTRIBUTING.md's "Reading the integration job's log".
#
# -count=1 defeats the test cache, so a cached result from an earlier run
# cannot stand in for a run that touched a database.
#
# -p 1 runs one package test binary at a time, so the performance evidence
# these packages record is not measured against neighbours competing for the
# same machine.
case "$1" in
check)
	echo "+ go test -p 1 -count=1 -v ./..."
	go test -p 1 -count=1 -v ./...
	;;
live)
	# An import path carries neither whitespace nor a glob character, so word
	# splitting reads this list back correctly and quoting it would hand go a
	# single operand naming no package.
	listed=$(guarded_packages | tr '\n' ' ')
	if [[ -z "${listed// /}" ]]; then
		echo "no package in $module has a test importing $dbtest, so this mode would run no live test at all" >&2
		exit 1
	fi
	echo "+ go test -p 1 -count=1 -v $listed"
	# shellcheck disable=SC2086
	go test -p 1 -count=1 -v $listed
	;;
*)
	echo "usage: $0 check|live" >&2
	exit 2
	;;
esac
