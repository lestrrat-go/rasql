//go:build unix

package dbtest

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCIWorkflowDSNsMatchComposeConstants pins postgresComposeDSN and
// mysqlComposeDSN, which skipNoDSN quotes verbatim in the message a
// developer reads when the relevant RASQL_TEST_*_DSN variable is unset (see
// CONTRIBUTING.md's "Live database tests" section), against the DSNs
// .github/workflows/ci.yml's integration job hard-codes for the same
// environment variables. Nothing enforces this equality except this test:
// compose.yaml, this package's constants, and ci.yml's env block are three
// independent copies of the same values, and a change to one that misses
// the others would otherwise only surface as a wrong export line in a skip
// message that no other test or CI job exercises.
//
// This is a plain-text substring check, not a YAML parse: ci.yml's exact
// "KEY: value" formatting is asserted directly rather than pulling in a
// YAML parser dependency for one test.
func TestCIWorkflowDSNsMatchComposeConstants(t *testing.T) {
	data, err := os.ReadFile("../../.github/workflows/ci.yml")
	require.NoError(t, err, "read .github/workflows/ci.yml")
	ci := string(data)

	wantPostgres := "RASQL_TEST_POSTGRES_DSN: " + postgresComposeDSN
	require.True(t, strings.Contains(ci, wantPostgres),
		"ci.yml does not contain %q; postgresComposeDSN in dbtest.go and the RASQL_TEST_POSTGRES_DSN CI sets have drifted apart -- update whichever one is stale so both match", wantPostgres)

	wantMySQL := "RASQL_TEST_MYSQL_DSN: " + mysqlComposeDSN
	require.True(t, strings.Contains(ci, wantMySQL),
		"ci.yml does not contain %q; mysqlComposeDSN in dbtest.go and the RASQL_TEST_MYSQL_DSN CI sets have drifted apart -- update whichever one is stale so both match", wantMySQL)
}
