#!/usr/bin/env bash
set -euo pipefail

go test -run '^$' -bench '^BenchmarkConformance' -benchmem -count=10 ./benchmarks/orm
go test -run '^$' -bench . -benchmem -count=10 ./query . ./namedsql
