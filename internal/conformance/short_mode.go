package conformance

import "testing"

// skipUnderShort lets a developer skip this package's three genuinely
// expensive tests -- TestGeneratedFootprintBuild, TestConformancePostgreSQL17,
// and TestConformanceMySQL84 -- while iterating locally, by running `go test
// -short`. Every other test in this package already runs in well under a
// second, so -short has nothing else to shorten here.
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
