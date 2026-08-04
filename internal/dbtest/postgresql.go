//go:build unix

package dbtest

import (
	"database/sql"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// PostgreSQLConfig returns a parsed, validated PostgreSQL connection
// configuration for a live database, resolved as described in the package
// doc. It skips the calling test rather than returning an error when no DSN
// or usable Docker fallback is available.
//
// It returns pgx's own *pgx.ConnConfig rather than a DSN string on purpose.
// A caller that needs different credentials against the same server (see
// inspect/postgresql_privilege_test.go's openAsRole) must copy this config
// and change its User/Password fields directly, never rebuild and reparse
// a DSN string: an earlier version of this harness did exactly that, and a
// net/url round-trip of a keyword/value DSN corrupted it into a connection
// string pgx accepted but resolved to an unrelated target (a Unix socket,
// no database, the OS user). Handing back the already-parsed struct makes
// that class of bug impossible to reintroduce here, because there is no
// string left to round-trip.
func PostgreSQLConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	return resolvePostgreSQLConfig(t)
}

// PostgreSQLDB opens a live PostgreSQL connection via PostgreSQLConfig,
// registers t.Cleanup to close it, and pings it before returning.
func PostgreSQLDB(t *testing.T) *sql.DB {
	t.Helper()
	config := PostgreSQLConfig(t)
	return openAndPing(t, stdlib.OpenDB(*config))
}

func resolvePostgreSQLConfig(t *testing.T) *pgx.ConnConfig {
	t.Helper()
	value, set := os.LookupEnv(postgresEnvVar)
	trimmed, useValue, shouldLog := dsnDecision(value, set)
	if !useValue {
		if shouldLog {
			blankDiagnostic(t, postgresEnvVar)
		}
		ensureComposeUp(t)
		// postgresComposeDSN is this package's own constant, not user
		// input, so a parse failure here would be a bug in this package
		// rather than something to validate against; require.NoError
		// -- unlike t.Fatalf below -- makes that distinction obvious to
		// anyone reading a failure.
		config, err := pgx.ParseConfig(postgresComposeDSN)
		if err != nil {
			t.Fatalf("dbtest: parse postgresComposeDSN: %v", err)
		}
		return config
	}

	if reason := unusablePostgreSQLTarget(trimmed); reason != "" {
		// A DSN that does not itself pin down where it connects is a
		// wrong answer, not an absent one, once RASQL_TEST_POSTGRES_DSN
		// has been set. Falling through here would recreate exactly the
		// hazard this rescope closes: pgconn.ParseConfig silently
		// substituting PG* environment variables, a service file, or its
		// own built-in defaults for whatever the DSN's own text left
		// unspecified. So this fails loudly instead.
		t.Fatalf("%s is set but %s; a set DSN must specify its own host, port, and database in its own text rather than rely on PG* environment variables, a service file, or pgx's built-in defaults", postgresEnvVar, reason)
	}

	config, err := pgx.ParseConfigWithOptions(trimmed, pgx.ParseConfigOptions{
		ParseConfigOptions: pgconn.ParseConfigOptions{ConnStringAllowedKeys: postgresConnStringAllowedKeys},
	})
	if err != nil {
		// A set-but-unparseable DSN fails rather than falls through to
		// Docker/skip: it is a concrete, wrong answer from the person who
		// set it, not the absence of one. See the package doc. This also
		// catches a DSN naming "service" or "servicefile", which
		// postgresConnStringAllowedKeys deliberately excludes: pgconn
		// merges a service file's settings ABOVE the ambient environment
		// (see postgresConnStringAllowedKeys's comment), so a service
		// reference could silently redirect host, port, and database to
		// something no text-provenance check on the DSN string alone
		// could ever catch.
		t.Fatalf("%s is set to a value pgx could not parse: %v", postgresEnvVar, err)
	}
	return config
}

// postgresConnStringAllowedKeys lists every connection-string key
// pgconn.ParseConfigWithOptions recognizes -- see pgconn/config.go's
// parseEnvSettings nameMap for the canonical set this mirrors -- except
// "service" and "servicefile". Passed as ParseConfigOptions.ConnStringAllowedKeys,
// it makes ParseConfigWithOptions itself reject a DSN naming either key,
// before any filesystem access, rather than this package hand-rolling a
// string search for "service=" or "servicefile=" across both the URL and
// keyword/value DSN forms.
//
// service and servicefile are excluded, not merely validated some other
// way, because they reintroduce exactly the hazard the rest of this file's
// text-provenance check exists to close. pgconn accepts servicefile as
// part of the connection string even though libpq only ever reads it from
// PGSERVICEFILE, and a service file's settings are merged into the config
// ABOVE (i.e. after, overriding) the ambient PG* environment settings --
// see pgconn/config.go's ParseConfigWithOptions, which merges
// defaultSettings(), then envSettings, then connStringSettings, and then
// layers serviceSettings on top of all three once "service" is present.
// A DSN's own text could name host, port, and database explicitly and
// still have every one of them silently overridden by an on-disk service
// file at connection time. No amount of inspecting the DSN string can rule
// that out, since the file's contents live outside the string entirely;
// refusing the keys that reach for it is the only closure available here.
var postgresConnStringAllowedKeys = []string{
	"host", "port", "database", "user", "password", "passfile",
	"application_name", "connect_timeout", "sslmode", "sslkey", "sslcert",
	"sslsni", "sslrootcert", "sslpassword", "sslnegotiation",
	"target_session_attrs", "timezone", "options",
	"min_protocol_version", "max_protocol_version", "channel_binding",
	"require_auth",
}

// unusablePostgreSQLTarget reports why dsn -- caller-supplied, non-blank
// DSN text -- must not be treated as usable, or "" when it is usable.
//
// pgconn.ParseConfig always merges the PG* libpq environment variables
// (PGHOST, PGPORT, PGDATABASE, ...) and its own built-in defaults (a Unix
// socket directory or "localhost" for the host, port 5432) underneath
// whatever the DSN's own text supplies -- see pgconn/config.go's
// mergeSettings(defaultSettings(), parseEnvSettings(), connStringSettings).
// An earlier version of this package tried to detect that by parsing dsn
// twice -- once in the ambient environment, once with every PG* variable
// hidden from the parser -- and rejecting a DSN whose two parses
// disagreed. A neutral audit found that approach unsound in multiple ways
// (a DSN naming the default port parses identically whether it is present
// or defaulted, so the comparison could not tell them apart and rejected
// CI's own DSN; the environment-hiding step mutated process-wide state
// requiring a mutex; and no comparison of the parsed *pgx.ConnConfig could
// ever see a service file's settings, since pgconn merges those in after
// building it). This function instead reads dsn's own text directly,
// independent of environment, service files, or defaults entirely: it is
// unusable exactly when the text itself does not name a host, a port, and
// a database, in either the DSN URL form ("postgres://...") or the
// keyword/value form ("host=... port=... dbname=...").
func unusablePostgreSQLTarget(dsn string) string {
	hasHost, hasPort, hasDatabase, err := postgresDSNProvenance(dsn)
	if err != nil {
		return fmt.Sprintf("its text could not be checked for a host, port, and database: %v", err)
	}
	switch {
	case !hasHost:
		return "it does not specify a host"
	case !hasPort:
		return "it does not specify a port"
	case !hasDatabase:
		return "it does not specify a database"
	default:
		return ""
	}
}

// postgresDSNProvenance reports whether dsn's own text -- independent of
// any environment variable, service file, or pgx built-in default -- names
// a host, a port, and a database, dispatching to the URL or keyword/value
// form depending on dsn's scheme prefix, the same way
// pgconn.ParseConfigWithOptions itself decides which form to parse (see
// pgconn/config.go's ParseConfigWithOptions).
func postgresDSNProvenance(dsn string) (hasHost, hasPort, hasDatabase bool, err error) {
	if strings.HasPrefix(dsn, "postgres://") || strings.HasPrefix(dsn, "postgresql://") {
		return postgresURLProvenance(dsn)
	}
	return postgresKeywordValueProvenance(dsn)
}

// postgresURLProvenance mirrors pgconn's own URL connection-string parsing
// (pgconn/config.go's parseURLSettings) closely enough to answer "does the
// URL's own text carry a host, a port, and a database", including its
// handling of a bracketed IPv6 host literal and of multiple comma-separated
// hosts. It is a purpose-built reimplementation -- parseURLSettings is
// unexported, so this package cannot call it directly -- used only to
// answer that question, never to build a connection.
func postgresURLProvenance(dsn string) (hasHost, hasPort, hasDatabase bool, err error) {
	parsed, err := url.Parse(dsn)
	if err != nil {
		return false, false, false, err
	}

	for hostport := range strings.SplitSeq(parsed.Host, ",") {
		if hostport == "" {
			continue
		}
		if postgresIsIPOnly(hostport) {
			hasHost = true
			continue
		}
		host, port, splitErr := net.SplitHostPort(hostport)
		if splitErr != nil {
			return false, false, false, splitErr
		}
		if host != "" {
			hasHost = true
		}
		if port != "" {
			hasPort = true
		}
	}

	hasDatabase = strings.TrimLeft(parsed.Path, "/") != ""

	// parseURLSettings also merges the URL's query parameters into its
	// settings map via canonicalConnStringKey, so host/port/dbname can in
	// principle arrive that way instead of via the authority or path (an
	// unusual DSN, but a legal one); check those too rather than only the
	// conventional locations.
	query := parsed.Query()
	if query.Get("host") != "" {
		hasHost = true
	}
	if query.Get("port") != "" {
		hasPort = true
	}
	if query.Get("dbname") != "" || query.Get("database") != "" {
		hasDatabase = true
	}

	return hasHost, hasPort, hasDatabase, nil
}

// postgresIsIPOnly mirrors pgconn's own isIPOnly (pgconn/config.go), which
// parseURLSettings uses to tell a bare host or bracketed IPv6 literal --
// with no ":port" suffix left to split off -- from a genuine "host:port"
// pair. Reimplemented here for the same reason as postgresURLProvenance:
// the original is unexported.
func postgresIsIPOnly(host string) bool {
	return net.ParseIP(strings.Trim(host, "[]")) != nil || !strings.Contains(host, ":")
}

// postgresKeywordValueProvenance mirrors pgconn's own keyword/value
// connection-string tokenizer (pgconn/config.go's parseKeywordValueSettings)
// closely enough to answer "does dsn's own text carry a non-empty host,
// port, and dbname/database key". A naive space split would mis-tokenize a
// value containing whitespace (e.g. a quoted application_name) or a
// backslash-escaped quote, and could then misread a later key as one that
// never actually appeared, or vice versa; this walks the same quoting and
// escaping rules parseKeywordValueSettings does, for the same reason
// postgresURLProvenance mirrors parseURLSettings.
func postgresKeywordValueProvenance(dsn string) (hasHost, hasPort, hasDatabase bool, err error) {
	const whitespace = " \t\n\r\v\f"

	s := strings.TrimLeft(dsn, whitespace)
	for len(s) > 0 {
		eqIdx := strings.IndexByte(s, '=')
		if eqIdx < 0 {
			return false, false, false, errors.New("invalid keyword/value")
		}
		key := strings.Trim(s[:eqIdx], whitespace)
		s = strings.TrimLeft(s[eqIdx+1:], whitespace)

		var val string
		switch {
		case len(s) == 0:
		case s[0] != '\'':
			end := 0
			for ; end < len(s); end++ {
				if strings.ContainsRune(whitespace, rune(s[end])) {
					break
				}
				if s[end] == '\\' {
					end++
					if end == len(s) {
						return false, false, false, errors.New("invalid backslash")
					}
				}
			}
			val = s[:end]
			s = strings.TrimLeft(s[end:], whitespace)
		default: // quoted string
			s = s[1:]
			end := 0
			for ; end < len(s); end++ {
				if s[end] == '\'' {
					break
				}
				if s[end] == '\\' {
					end++
				}
			}
			if end >= len(s) {
				return false, false, false, errors.New("unterminated quoted string in connection info string")
			}
			val = s[:end]
			s = strings.TrimLeft(s[end+1:], whitespace)
		}

		if key == "" {
			return false, false, false, errors.New("invalid keyword/value")
		}
		switch key {
		case "host":
			hasHost = hasHost || val != ""
		case "port":
			hasPort = hasPort || val != ""
		case "dbname", "database":
			hasDatabase = hasDatabase || val != ""
		}
	}
	return hasHost, hasPort, hasDatabase, nil
}
