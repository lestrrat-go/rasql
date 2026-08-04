//go:build unix

package dbtest

import (
	"database/sql"
	"os"
	"testing"

	"github.com/go-sql-driver/mysql"
)

// MySQLConfig returns a parsed, validated MySQL connection configuration
// for a live database, resolved as described in the package doc. It skips
// the calling test rather than returning an error when no DSN or usable
// Docker fallback is available.
//
// Like PostgreSQLConfig, it returns the driver's own *mysql.Config rather
// than a DSN string, so a caller needing different credentials modifies the
// parsed struct instead of rebuilding and reparsing a string. See
// MySQLConfig's PostgreSQL sibling for the round-trip bug that guards
// against; nothing in this repository currently rebuilds a MySQL DSN, but
// returning the parsed form keeps that true rather than merely accidental.
func MySQLConfig(t *testing.T) *mysql.Config {
	t.Helper()
	return resolveMySQLConfig(t)
}

// MySQLDB opens a live MySQL connection via MySQLConfig, registers
// t.Cleanup to close it, and pings it before returning.
func MySQLDB(t *testing.T) *sql.DB {
	t.Helper()
	config := MySQLConfig(t)
	connector, err := mysql.NewConnector(config)
	if err != nil {
		t.Fatalf("dbtest: build mysql connector: %v", err)
	}
	return openAndPing(t, sql.OpenDB(connector))
}

func resolveMySQLConfig(t *testing.T) *mysql.Config {
	t.Helper()
	value, set := os.LookupEnv(mysqlEnvVar)
	trimmed, useValue, shouldLog := dsnDecision(value, set)
	if !useValue {
		if shouldLog {
			blankDiagnostic(t, mysqlEnvVar)
		}
		ensureComposeUp(t)
		// mysqlComposeDSN is this package's own constant, not user input;
		// see the identical comment in resolvePostgreSQLConfig.
		config, err := mysql.ParseDSN(mysqlComposeDSN)
		if err != nil {
			t.Fatalf("dbtest: parse mysqlComposeDSN: %v", err)
		}
		return config
	}

	config, err := mysql.ParseDSN(trimmed)
	if err != nil {
		// Same reasoning as resolvePostgreSQLConfig's identical branch: a
		// set-but-unparseable DSN fails rather than falls through.
		t.Fatalf("%s is set to a value the mysql driver could not parse: %v", mysqlEnvVar, err)
	}
	if reason := unusableMySQLTarget(config); reason != "" {
		t.Fatalf("%s is set but %s; a set DSN must specify its own database rather than rely on the mysql driver's built-in default", mysqlEnvVar, reason)
	}
	return config
}

// unusableMySQLTarget reports why config -- parsed from a caller-supplied,
// non-blank DSN -- must not be treated as usable, or "" when it is usable.
//
// The go-sql-driver/mysql package has no PostgreSQL-style PG* environment
// variable layer at all: mysql.ParseDSN and mysql.Config.normalize() read
// no environment variables anywhere (confirmed by inspecting dsn.go and
// config.go in the driver module; the only os.Getenv calls in the module
// are in its own tests), so a MySQL DSN can never silently pick up an
// unrelated target the way an under-specified PostgreSQL DSN can pick up
// PGHOST/PGPORT/PGDATABASE. That is the core hazard this rescope closes,
// and MySQL simply does not have it.
//
// What MySQL does have is a narrower, different hazard: an under-specified
// DSN (e.g. a bare "/") still parses successfully, with Addr defaulting to
// the driver's hardcoded "127.0.0.1:3306" and DBName defaulting to "". The
// same baseline-comparison this package uses for PostgreSQL does not carry
// over to Addr, though: mysqlComposeDSN -- this repository's own,
// deliberately correct DSN -- also resolves to "127.0.0.1:3306", because
// that literally is where compose.yaml publishes MySQL. Comparing Addr
// against a "no DSN" baseline would therefore reject every legitimate DSN
// this harness produces, not just broken ones, so this function does not
// perform that comparison; there is no way for an omitted host/port to
// resolve anywhere other than the same address a correct DSN would also
// name, so there is nothing a comparison could usefully catch there.
// Database is different: mysql.Config never defaults DBName to anything
// but "", so requiring it to be non-empty is both meaningful and safe,
// which is the one check this function performs.
func unusableMySQLTarget(config *mysql.Config) string {
	if config.DBName == "" {
		return "it does not specify a database"
	}
	return ""
}
