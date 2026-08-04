package dbtest

import (
	"fmt"
	"regexp"
	"strings"
)

// portCollisionPatterns lists the substrings (matched case-insensitively)
// that identify a failed `docker compose up` as a host port already being
// in use, rather than a broken compose file or image reference. Docker
// Engine, its compose plugin, the standalone docker-compose, and podman's
// Docker-CLI shim each word this failure differently, and the wording has
// changed across Docker Engine versions too. Every variant below was
// confirmed against real error text (Docker's own docs/forums, GitHub
// issues against containers/podman, and this repository's own CI log --
// see the comment on each pattern), not guessed.
//
// Bias: an unfamiliar or ambiguous message is deliberately NOT classified
// as a collision. The fail-loudly rule this carve-out lives inside of
// exists to surface a broken compose file or image reference; a pattern
// that over-matches would silently skip that real breakage, which is
// exactly the harm the rule exists to prevent. When in doubt, this list
// stays narrow and the caller falls through to failing loudly.
var portCollisionPatterns = []string{
	// Docker Engine's long-standing wording, produced when the daemon
	// can't bind the published port while wiring up the container's
	// network endpoint. This is what this repository's own CI hit when
	// its service containers already held 5432 and 3306:
	//   Error response from daemon: failed to set up container
	//   networking: driver failed programming external connectivity on
	//   endpoint rasql-postgres-1: Bind for 0.0.0.0:5432 failed: port is
	//   already allocated
	"port is already allocated",

	// Docker Engine >=23.x's replacement wording, after published ports
	// moved off the userland-proxy path onto a native Go listener:
	//   Error response from daemon: Ports are not available: exposing
	//   port TCP 0.0.0.0:5432 -> 0.0.0.0:0: listen tcp 0.0.0.0:5432:
	//   bind: address already in use
	"ports are not available",

	// The Go net package's own bind error text. It is the tail of the
	// message above, and is also what podman's rootlessport port
	// forwarder surfaces verbatim under the podman-as-docker shim, e.g.
	//   Error: rootlessport listen tcp 0.0.0.0:9080: bind: address
	//   already in use
	// A standalone docker-compose (the Python v1 binary) passes the
	// daemon's own error text through unchanged, so it is covered by
	// this and the two patterns above without a dedicated entry.
	"address already in use",

	// podman's own higher-level phrasing, produced when a host port is
	// found already claimed before rootlessport is even invoked,
	// distinct from Docker's "already allocated" wording above:
	//   Error: unable to start container 4f3c...: port 8080 is already
	//   in use
	"is already in use",
}

// portNumberPatterns extracts the host port number from a port-collision
// message, tried in the order listed; the first match wins. Each regexp is
// anchored to the specific wording it was written against (see the
// comments on portCollisionPatterns for what produces each one), not to
// "any number that looks like a port", so a message that happens to
// contain an unrelated number does not get mis-extracted.
var portNumberPatterns = []*regexp.Regexp{
	// "Bind for 0.0.0.0:5432 failed: port is already allocated"
	regexp.MustCompile(`(?i)Bind for \S*:(\d+) failed`),

	// "... listen tcp 0.0.0.0:5432: bind: address already in use" and
	// its IPv6 form "... listen tcp6 [::]:5432: bind: address already in
	// use" both end in "<port>: bind: address already in use"
	// regardless of address family, so anchor there rather than on the
	// address that precedes it.
	regexp.MustCompile(`(?i):(\d+): ?bind: ?address already in use`),

	// "port 8080 is already in use"
	regexp.MustCompile(`(?i)\bport (\d+) is already in use\b`),
}

// classifyPortCollision reports whether combinedOutput -- the combined
// stdout/stderr of a failed `docker compose up` -- names a host port
// already in use, and the port number when extractPort could determine
// one from the matched text. port is "" when a collision was detected but
// no pattern in portNumberPatterns could pull a number out of this
// particular message; the caller must not guess at a port in that case.
func classifyPortCollision(combinedOutput string) (collision bool, port string) {
	lower := strings.ToLower(combinedOutput)
	for _, pattern := range portCollisionPatterns {
		if strings.Contains(lower, pattern) {
			return true, extractPort(combinedOutput)
		}
	}
	return false, ""
}

func extractPort(output string) string {
	for _, re := range portNumberPatterns {
		if m := re.FindStringSubmatch(output); m != nil {
			return m[1]
		}
	}
	return ""
}

// envVarForPort names the RASQL_TEST_*_DSN environment variable for the
// compose service that owns port, matching the fixed ports documented in
// compose.yaml and the package doc (5432 for PostgreSQL, 3306 for MySQL).
// It returns "" when port matches neither -- including when the port could
// not be determined at all -- so the caller can fall back to naming both
// variables instead of one that might be wrong.
func envVarForPort(port string) string {
	switch port {
	case "5432":
		return postgresEnvVar
	case "3306":
		return mysqlEnvVar
	default:
		return ""
	}
}

// classifyBringUpFailure is the decision at the heart of ensureComposeUp's
// failure handling: given the combined output of a failed `docker compose
// up`, should the calling test skip -- naming the conflicting port -- or
// fail loudly? See the package doc for why those are different outcomes.
//
// This function runs no commands and touches no *testing.T; it is pure
// input to output, so it -- and the classification it is built on -- can
// be unit tested directly without Docker. See this function's test and
// classifyPortCollision's test for why that separation exists, matching
// the same pattern dsnDecision uses elsewhere in this package.
func classifyBringUpFailure(combinedOutput []byte) (skip bool, message string) {
	collision, port := classifyPortCollision(string(combinedOutput))
	if !collision {
		return false, ""
	}
	if port == "" {
		return true, fmt.Sprintf(
			"a host port docker compose wanted appears to already be in use, but the port could not be determined from its output; set %s or %s to use the database you already have running instead",
			postgresEnvVar, mysqlEnvVar,
		)
	}
	if envVar := envVarForPort(port); envVar != "" {
		return true, fmt.Sprintf(
			"host port %s is already in use; set %s to use the database you already have running instead of bringing up compose",
			port, envVar,
		)
	}
	return true, fmt.Sprintf(
		"host port %s is already in use; set %s or %s to use the database you already have running instead of bringing up compose",
		port, postgresEnvVar, mysqlEnvVar,
	)
}
