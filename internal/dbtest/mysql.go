//go:build unix

package dbtest

import (
	"database/sql"
	"os"
	"strings"
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
	if reason := unusableMySQLTarget(trimmed, config); reason != "" {
		t.Fatalf("%s is set but %s; a set DSN must specify its own network address and database in its own text rather than rely on the mysql driver's built-in default", mysqlEnvVar, reason)
	}
	return config
}

// unusableMySQLTarget reports why dsn -- caller-supplied, non-blank DSN
// text that has already parsed successfully into config -- must not be
// treated as usable, or "" when it is usable.
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
// DSN (e.g. a bare "/") still parses successfully, with config.Addr
// defaulting to the driver's hardcoded "127.0.0.1:3306" and DBName
// defaulting to "". An earlier version of this function reasoned that
// there was no way for an omitted host/port to resolve anywhere other than
// that same default address, and so declined to check Addr's provenance at
// all -- but that reasoning was wrong: "rasql:rasql@unix()/rasql" parses
// with an empty address inside the unix() protocol, and
// Config.normalize() resolves that empty address to "/tmp/mysql.sock", not
// to 127.0.0.1:3306, proving an omitted address can reach a target a
// correct DSN never names. mysql.Config also cannot answer "did the DSN's
// own text specify this" after the fact: ParseDSN calls normalize() before
// returning, so an explicit "tcp(127.0.0.1:3306)" and an entirely omitted
// address both leave identical *mysql.Config values behind.
//
// So, exactly as unusablePostgreSQLTarget does for PostgreSQL, this
// function reads dsn's own text directly via mysqlAddrText -- the DSN
// string the resolver still holds, before ParseDSN's normalization -- and
// requires it to name its own network address, plus a port when that
// address is (or defaults to) tcp. Database is unaffected by this hazard:
// mysql.Config never defaults DBName to anything but "", so requiring
// config.DBName to be non-empty, read directly off the already-parsed
// struct, remains both meaningful and safe.
func unusableMySQLTarget(dsn string, config *mysql.Config) string {
	if config.DBName == "" {
		return "it does not specify a database"
	}
	netText, addrText := mysqlAddrText(dsn)
	if addrText == "" {
		return "it does not specify its own network address (host or socket path) rather than relying on the driver's built-in default"
	}
	if (netText == "" || netText == "tcp") && !mysqlAddrHasPort(addrText) {
		return "it does not specify a port"
	}
	return ""
}

// mysqlAddrText mirrors go-sql-driver/mysql's own DSN scanner (dsn.go's
// ParseDSN) far enough to recover the network protocol and address exactly
// as they appear in dsn's own text -- i.e. cfg.Net and cfg.Addr as ParseDSN
// computes them, before Config.normalize() runs and overwrites an empty
// Net with "tcp" and an empty Addr with "127.0.0.1:3306". Those
// pre-normalize values are the only place the DSN's own explicitness about
// its address survives; see unusableMySQLTarget's comment for why the
// already-parsed, already-normalized *mysql.Config cannot answer this.
//
// dsn is only ever passed here after mysql.ParseDSN has already accepted
// it (see resolveMySQLConfig), so it is known to contain the
// dbname-separating slash ParseDSN itself requires; this walks the same
// right-to-left scan ParseDSN uses -- last '/' before the database name,
// then last '@' before that -- specifically because a naive left-to-right
// split would misparse a password containing '@' or '/' as if it were
// part of the network address or an extra path segment.
func mysqlAddrText(dsn string) (netText, addrText string) {
	slash := strings.LastIndexByte(dsn, '/')
	if slash <= 0 {
		return "", ""
	}
	left := dsn[:slash]

	// [user[:password]@][net[(addr)]] -- find the last '@' in left, since
	// the password may itself contain one; at == -1 (no '@' present) is
	// handled the same way ParseDSN handles it, by treating the whole of
	// left as the net[(addr)] segment.
	at := strings.LastIndexByte(left, '@')
	netAddr := left[at+1:]

	open := strings.IndexByte(netAddr, '(')
	if open < 0 {
		return netAddr, ""
	}
	if len(netAddr) == 0 || netAddr[len(netAddr)-1] != ')' {
		// ParseDSN requires the character immediately before the
		// dbname-separating slash to be ')' whenever '(' is present, or
		// it rejects the DSN outright; since dsn has already been
		// accepted by ParseDSN, this branch should be unreachable, but
		// it fails safe (an empty address is treated as unspecified)
		// rather than panicking on a slice bound if that invariant is
		// ever violated.
		return netAddr[:open], ""
	}
	return netAddr[:open], netAddr[open+1 : len(netAddr)-1]
}

// mysqlAddrHasPort reports whether addrText -- a tcp network address as it
// appears literally inside a DSN's protocol(...) segment -- names its own
// port, treating a bracketed IPv6 literal ("[::1]:3306") the way
// net.SplitHostPort does rather than naively looking for the first colon,
// which would misread an IPv6 literal's own colons as a port separator.
func mysqlAddrHasPort(addrText string) bool {
	if strings.HasPrefix(addrText, "[") {
		return strings.Contains(addrText, "]:")
	}
	return strings.Contains(addrText, ":")
}
