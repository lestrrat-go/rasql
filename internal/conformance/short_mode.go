package conformance

import "testing"

// skipUnderShort lets a developer skip this package's three genuinely
// expensive tests -- TestGeneratedFootprintBuild, TestConformancePostgreSQL17,
// and TestConformanceMySQL84 -- while iterating locally, by running `go test
// -short`. Every other test in this package already runs in well under a
// second, so -short has nothing else to shorten here.
//
// CI must never trigger this skip. The "check" job's full-suite step is
// pinned by checkRunCommand (ci_integration_coverage_test.go) to reject any
// command whose effective -short value is true, in every spelling go
// accepts. The "integration" job's dedicated conformance step is pinned by
// dedicatedConformanceStep to run the fixed `./scripts/conformance.sh live`
// command with no flags at all, and that script's own log parsing fails the
// job if TestConformancePostgreSQL17 or TestConformanceMySQL84 shows a SKIP
// line for any reason -- so even a stray -short reaching that step would be
// caught, not just a deliberately added one.
func skipUnderShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping expensive conformance test under -short; CI always runs it (see checkRunCommand and scripts/conformance.sh)")
	}
}
