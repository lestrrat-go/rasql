//go:build unix

package dbtest

import (
	"fmt"
	"net"
	"os"
	"strings"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

// containsLine reports whether content has a line whose trimmed text equals
// want exactly. A substring match would accept a value that merely starts
// with want, such as "rasqluser" for a wanted "rasql" or a DSN with an
// appended query parameter, so the check is line-exact instead.
func containsLine(content, want string) bool {
	for _, line := range strings.Split(content, "\n") {
		if strings.TrimSpace(line) == want {
			return true
		}
	}
	return false
}

// TestCIWorkflowDSNsMatchComposeConstants pins postgresComposeDSN and
// mysqlComposeDSN, which skipNoDSN quotes verbatim in the message a
// developer reads when the relevant RASQL_TEST_*_DSN variable is unset (see
// CONTRIBUTING.md's "Live database tests" section), against BOTH other
// independent copies of the same values: the DSNs .github/workflows/ci.yml's
// integration job hard-codes for the same environment variables, and the
// service definitions compose.yaml itself hard-codes. Nothing enforces this
// equality except this test: compose.yaml, this package's constants, and
// ci.yml's env block are three independent copies of the same values, and a
// change to one that misses the others would otherwise only surface as a
// wrong export line in a skip message that no other test or CI job
// exercises.
//
// This is a plain-text line-exact check, not a YAML parse: both ci.yml's
// exact "KEY: value" formatting and compose.yaml's exact "KEY: value" /
// port-mapping formatting are asserted directly rather than pulling in a
// YAML parser dependency for one test. The values checked against
// compose.yaml are derived by parsing postgresComposeDSN and mysqlComposeDSN
// with the same driver parsers (pgx.ParseConfig, mysql.ParseDSN) the package
// itself uses to parse a live RASQL_TEST_*_DSN, rather than hard-coding a
// fourth copy of user/password/database/port here.
func TestCIWorkflowDSNsMatchComposeConstants(t *testing.T) {
	ciData, err := os.ReadFile("../../.github/workflows/ci.yml")
	require.NoError(t, err, "read .github/workflows/ci.yml")
	ci := string(ciData)

	wantPostgres := "RASQL_TEST_POSTGRES_DSN: " + postgresComposeDSN
	require.True(t, containsLine(ci, wantPostgres),
		"ci.yml does not contain %q; postgresComposeDSN in dbtest.go and the RASQL_TEST_POSTGRES_DSN CI sets have drifted apart -- update whichever one is stale so both match", wantPostgres)

	wantMySQL := "RASQL_TEST_MYSQL_DSN: " + mysqlComposeDSN
	require.True(t, containsLine(ci, wantMySQL),
		"ci.yml does not contain %q; mysqlComposeDSN in dbtest.go and the RASQL_TEST_MYSQL_DSN CI sets have drifted apart -- update whichever one is stale so both match", wantMySQL)

	composeData, err := os.ReadFile("../../compose.yaml")
	require.NoError(t, err, "read compose.yaml")
	compose := string(composeData)

	pgCfg, err := pgx.ParseConfig(postgresComposeDSN)
	require.NoError(t, err, "parse postgresComposeDSN in dbtest.go with pgx.ParseConfig")

	wantPGUser := fmt.Sprintf("POSTGRES_USER: %s", pgCfg.User)
	require.True(t, containsLine(compose, wantPGUser),
		"compose.yaml does not contain %q; the postgres service's POSTGRES_USER in compose.yaml and postgresComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantPGUser)

	wantPGPassword := fmt.Sprintf("POSTGRES_PASSWORD: %s", pgCfg.Password)
	require.True(t, containsLine(compose, wantPGPassword),
		"compose.yaml does not contain %q; the postgres service's POSTGRES_PASSWORD in compose.yaml and postgresComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantPGPassword)

	wantPGDatabase := fmt.Sprintf("POSTGRES_DB: %s", pgCfg.Database)
	require.True(t, containsLine(compose, wantPGDatabase),
		"compose.yaml does not contain %q; the postgres service's POSTGRES_DB in compose.yaml and postgresComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantPGDatabase)

	wantPGPort := fmt.Sprintf("- \"%d:5432\"", pgCfg.Port)
	require.True(t, containsLine(compose, wantPGPort),
		"compose.yaml does not contain %q; the postgres service's host port mapping in compose.yaml and postgresComposeDSN's port in dbtest.go have drifted apart -- update whichever one is stale so both match", wantPGPort)

	mysqlCfg, err := mysql.ParseDSN(mysqlComposeDSN)
	require.NoError(t, err, "parse mysqlComposeDSN in dbtest.go with mysql.ParseDSN")

	wantMySQLDatabase := fmt.Sprintf("MYSQL_DATABASE: %s", mysqlCfg.DBName)
	require.True(t, containsLine(compose, wantMySQLDatabase),
		"compose.yaml does not contain %q; the mysql service's MYSQL_DATABASE in compose.yaml and mysqlComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantMySQLDatabase)

	_, mysqlPort, err := net.SplitHostPort(mysqlCfg.Addr)
	require.NoError(t, err, "split host/port out of mysqlComposeDSN's address in dbtest.go")

	wantMySQLPort := fmt.Sprintf("- \"%s:3306\"", mysqlPort)
	require.True(t, containsLine(compose, wantMySQLPort),
		"compose.yaml does not contain %q; the mysql service's host port mapping in compose.yaml and mysqlComposeDSN's port in dbtest.go have drifted apart -- update whichever one is stale so both match", wantMySQLPort)

	// mysqlComposeDSN authenticates as root (see its comment in dbtest.go
	// for why: the rasql/rasql account MYSQL_USER/MYSQL_PASSWORD create is
	// scoped to MYSQL_DATABASE only and cannot CREATE DATABASE for this
	// package's fresh per-run schema, but root can), so its password must be
	// checked against compose.yaml's MYSQL_ROOT_PASSWORD, not
	// MYSQL_PASSWORD, when the parsed user is root.
	var wantMySQLPassword string
	if mysqlCfg.User == "root" {
		wantMySQLPassword = fmt.Sprintf("MYSQL_ROOT_PASSWORD: %s", mysqlCfg.Passwd)
	} else {
		wantMySQLPassword = fmt.Sprintf("MYSQL_PASSWORD: %s", mysqlCfg.Passwd)
	}
	require.True(t, containsLine(compose, wantMySQLPassword),
		"compose.yaml does not contain %q; the mysql service's password variable in compose.yaml and mysqlComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantMySQLPassword)
}
