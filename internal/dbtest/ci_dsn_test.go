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

// leadingSpaces counts the run of literal space characters at the start of
// line. Both ci.yml and compose.yaml indent exclusively with spaces, never
// tabs, so this is the same measure a YAML parser would use for indent
// depth without needing one.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// namedBlock slices out the named block introduced by headerAtIndent -- for
// example "  integration:" under ci.yml's top-level "jobs:" key, or
// "  postgres:" under compose.yaml's top-level "services:" key -- so a
// caller can scope containsLine to that block instead of matching anywhere
// in the whole file. Without this, a line that has been moved into the
// WRONG job or the WRONG service still satisfies containsLine against the
// whole file, which defeats the point of a drift guard.
//
// headerAtIndent must be the exact line text of the header, indentation
// included (this package's two files both use a uniform two-space indent
// under their single top-level key, which is enough to find it without a
// YAML parser). The block is every line after the header up to, but not
// including, the first later non-blank line whose leading-space count is
// less than or equal to the header's own -- i.e. the first line back out at
// or above the header's nesting depth.
//
// If headerAtIndent is not found at all, this returns an empty slice rather
// than falling back to the whole file. That is deliberate: every
// containsLine assertion made against an empty slice fails, so a rename or
// reformat that makes the header stop matching breaks the test loudly
// instead of silently widening the search back to the whole file and
// weakening the guard it exists to provide.
//
// One limit worth being explicit about: this proves a wanted line is
// SOMEWHERE inside the named block, not that it sits on any one particular
// nested step's own env: (for ci.yml) or on the right side of a
// deeper-nested key (for compose.yaml). Proving that would need a real
// parser, which this test deliberately does not add -- see the package
// comment on TestCIWorkflowDSNsMatchComposeConstants.
func namedBlock(content, headerAtIndent string) []string {
	lines := strings.Split(content, "\n")
	headerIndent := leadingSpaces(headerAtIndent)

	start := -1
	for i, line := range lines {
		if line == headerAtIndent {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return []string{}
	}

	block := []string{}
	for _, line := range lines[start:] {
		if strings.TrimSpace(line) != "" && leadingSpaces(line) <= headerIndent {
			break
		}
		block = append(block, line)
	}
	return block
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
//
// Each assertion is scoped with namedBlock to the specific job or service it
// is actually about -- the ci.yml DSN lines to the "integration" job, the
// PostgreSQL lines to compose.yaml's "postgres" service, the MySQL lines to
// its "mysql" service -- rather than searched across the whole file. A
// whole-file search would still pass if, say, RASQL_TEST_POSTGRES_DSN were
// moved into the "check" job's env instead of "integration"'s, or if
// POSTGRES_USER were moved onto the "mysql" service: the line would still
// be somewhere in the file, just not where it needs to be for CI to
// actually use it. See namedBlock for the one thing this scoping still
// cannot prove: that a ci.yml line sits on the go test step's own env:,
// specifically, rather than merely somewhere else inside the same job.
func TestCIWorkflowDSNsMatchComposeConstants(t *testing.T) {
	ciData, err := os.ReadFile("../../.github/workflows/ci.yml")
	require.NoError(t, err, "read .github/workflows/ci.yml")
	ci := string(ciData)
	integrationJob := strings.Join(namedBlock(ci, "  integration:"), "\n")

	wantPostgres := "RASQL_TEST_POSTGRES_DSN: " + postgresComposeDSN
	require.True(t, containsLine(integrationJob, wantPostgres),
		"the integration job in ci.yml does not contain %q; postgresComposeDSN in dbtest.go and the RASQL_TEST_POSTGRES_DSN CI sets have drifted apart -- update whichever one is stale so both match", wantPostgres)

	wantMySQL := "RASQL_TEST_MYSQL_DSN: " + mysqlComposeDSN
	require.True(t, containsLine(integrationJob, wantMySQL),
		"the integration job in ci.yml does not contain %q; mysqlComposeDSN in dbtest.go and the RASQL_TEST_MYSQL_DSN CI sets have drifted apart -- update whichever one is stale so both match", wantMySQL)

	composeData, err := os.ReadFile("../../compose.yaml")
	require.NoError(t, err, "read compose.yaml")
	compose := string(composeData)
	postgresService := strings.Join(namedBlock(compose, "  postgres:"), "\n")
	mysqlService := strings.Join(namedBlock(compose, "  mysql:"), "\n")

	pgCfg, err := pgx.ParseConfig(postgresComposeDSN)
	require.NoError(t, err, "parse postgresComposeDSN in dbtest.go with pgx.ParseConfig")

	wantPGUser := fmt.Sprintf("POSTGRES_USER: %s", pgCfg.User)
	require.True(t, containsLine(postgresService, wantPGUser),
		"the postgres service in compose.yaml does not contain %q; its POSTGRES_USER and postgresComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantPGUser)

	wantPGPassword := fmt.Sprintf("POSTGRES_PASSWORD: %s", pgCfg.Password)
	require.True(t, containsLine(postgresService, wantPGPassword),
		"the postgres service in compose.yaml does not contain %q; its POSTGRES_PASSWORD and postgresComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantPGPassword)

	wantPGDatabase := fmt.Sprintf("POSTGRES_DB: %s", pgCfg.Database)
	require.True(t, containsLine(postgresService, wantPGDatabase),
		"the postgres service in compose.yaml does not contain %q; its POSTGRES_DB and postgresComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantPGDatabase)

	wantPGPort := fmt.Sprintf("- \"%d:5432\"", pgCfg.Port)
	require.True(t, containsLine(postgresService, wantPGPort),
		"the postgres service in compose.yaml does not contain %q; its host port mapping and postgresComposeDSN's port in dbtest.go have drifted apart -- update whichever one is stale so both match", wantPGPort)

	mysqlCfg, err := mysql.ParseDSN(mysqlComposeDSN)
	require.NoError(t, err, "parse mysqlComposeDSN in dbtest.go with mysql.ParseDSN")

	wantMySQLDatabase := fmt.Sprintf("MYSQL_DATABASE: %s", mysqlCfg.DBName)
	require.True(t, containsLine(mysqlService, wantMySQLDatabase),
		"the mysql service in compose.yaml does not contain %q; its MYSQL_DATABASE and mysqlComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantMySQLDatabase)

	_, mysqlPort, err := net.SplitHostPort(mysqlCfg.Addr)
	require.NoError(t, err, "split host/port out of mysqlComposeDSN's address in dbtest.go")

	wantMySQLPort := fmt.Sprintf("- \"%s:3306\"", mysqlPort)
	require.True(t, containsLine(mysqlService, wantMySQLPort),
		"the mysql service in compose.yaml does not contain %q; its host port mapping and mysqlComposeDSN's port in dbtest.go have drifted apart -- update whichever one is stale so both match", wantMySQLPort)

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
	require.True(t, containsLine(mysqlService, wantMySQLPassword),
		"the mysql service in compose.yaml does not contain %q; its password variable and mysqlComposeDSN in dbtest.go have drifted apart -- update whichever one is stale so both match", wantMySQLPassword)
}
