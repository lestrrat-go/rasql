package conformance

import "testing"

// skipUnderShort lets a developer skip this package's two genuinely expensive
// tests -- TestConformancePostgreSQL17 and TestConformanceMySQL84 -- while
// iterating locally, by running `go test -short`. Both seed a live server with
// 11,000 rows and run the whole canonical workload matrix twice over it, once
// through rasql and once through database/sql.
//
// CI must never trigger this skip. Both CI jobs reach `go test` through
// scripts/test.sh, which writes out the flags each mode uses and passes
// -short in neither. The "integration" job's dedicated conformance step runs
// `./scripts/conformance.sh live`, and that script's own log parsing fails
// the job if TestConformancePostgreSQL17 or TestConformanceMySQL84 shows a
// SKIP line for any reason -- so a -short added to the live matrix is caught
// by the run itself rather than by reading the command that started it.
func skipUnderShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping expensive conformance test under -short; CI always runs it (see scripts/test.sh and scripts/conformance.sh)")
	}
}
