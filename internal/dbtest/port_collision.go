//go:build unix

package dbtest

import (
	"fmt"
	"net"
	"regexp"
)

// portCollisionPatterns lists the case-insensitive regular expressions that
// identify a failed `docker compose up` as a host port already being in
// use, rather than a broken compose file or image reference. Docker
// Engine, its compose plugin, the standalone docker-compose, and podman's
// Docker-CLI shim each word this failure differently, and the wording has
// changed across Docker Engine versions too. Every variant below was
// confirmed against real error text (Docker's own docs/forums, GitHub
// issues against containers/podman and docker/for-mac, moby's own source,
// and this repository's own CI log -- see the comment on each pattern),
// not guessed.
//
// Bias: an unfamiliar or ambiguous message is deliberately NOT classified
// as a collision. The fail-loudly rule this carve-out lives inside of
// exists to surface a broken compose file or image reference; a pattern
// that over-matches would silently skip that real breakage, which is
// exactly the harm the rule exists to prevent. When in doubt, this list
// stays narrow and the caller falls through to failing loudly. Regular
// expressions (rather than plain substrings) are what let each entry stay
// narrow: two earlier substring entries over-matched non-port failures
// (see the comments on the third and fourth patterns below) and were
// tightened after a review confirmed both false positives against real
// daemon/podman error text.
//
// This list is used for classification only -- deciding skip vs. fail --
// and never for extracting a port number. An earlier version of this file
// also kept a second, parallel pattern list to pull the port number out of
// this same text, but that list already missed real wording (Docker
// Desktop for Windows' WSA bind error names the port but not in a shape
// any of those patterns matched), which made a skip that should have named
// the port instead falsely claim the port could not be determined.
// findConflictingPort below replaces text extraction with directly
// probing the ports compose.yaml publishes, so that class of miss cannot
// recur; see its comment.
var portCollisionPatterns = []*regexp.Regexp{
	// Docker Engine's long-standing wording, produced when the daemon
	// can't bind the published port while wiring up the container's
	// network endpoint. This is what this repository's own CI hit when
	// its service containers already held 5432 and 3306:
	//   Error response from daemon: failed to set up container
	//   networking: driver failed programming external connectivity on
	//   endpoint rasql-postgres-1: Bind for 0.0.0.0:5432 failed: port is
	//   already allocated
	regexp.MustCompile(`(?i)port is already allocated`),

	// Docker Engine >=23.x's replacement wording, after published ports
	// moved off the userland-proxy path onto a native Go listener. Also
	// what Docker Desktop for Windows wraps its own WSA error text in:
	//   Error response from daemon: Ports are not available: exposing
	//   port TCP 0.0.0.0:5432 -> 0.0.0.0:0: listen tcp 0.0.0.0:5432:
	//   bind: address already in use
	regexp.MustCompile(`(?i)ports are not available`),

	// The Go net package's own bind error text, produced by
	// os.NewSyscallError("bind", ...) and so always prefixed with
	// "bind: " regardless of where the port appears in the surrounding
	// message. It is the tail of the message above, and is also what
	// podman's rootlessport port forwarder surfaces verbatim (both IPv4
	// and IPv6) and what podman's "cannot listen on the TCP port"
	// wrapper surfaces at its own tail, e.g.
	//   Error: rootlessport listen tcp 0.0.0.0:9080: bind: address
	//   already in use
	//   Error: rootlessport listen tcp6 [::]:5432: bind: address
	//   already in use
	//   cannot listen on the TCP port: listen tcp4 :5432: bind: address
	//   already in use
	// A standalone docker-compose (the Python v1 binary) passes the
	// daemon's own error text through unchanged, so it is covered by
	// this and the two patterns above without a dedicated entry.
	//
	// The bare "address already in use" this pattern replaces also
	// matched moby's static-IP conflict, ErrIPAlreadyAllocated in
	// daemon/libnetwork/ipamapi/contract.go ("Address already in use"),
	// which has no "bind: " prefix because it never goes through a
	// socket bind call -- it is raised by libnetwork's own IPAM
	// bookkeeping before any bind is attempted. Requiring the prefix
	// excludes that false positive without losing any known collision
	// variant, all of which go through an actual bind syscall.
	regexp.MustCompile(`(?i)bind: address already in use`),

	// podman's own higher-level phrasing, produced when a host port is
	// found already claimed before rootlessport is even invoked,
	// distinct from Docker's "already allocated" wording above:
	//   Error: unable to start container 4f3c...: port 8080 is already
	//   in use
	//
	// The bare "is already in use" this pattern replaces also matched
	// moby's container-name conflict, nameConflictError.Error() in
	// daemon/errors.go: `Conflict. The container name %q is already in
	// use by container %q. You have to remove (or rename) that
	// container to be able to reuse that name.` -- podman's equivalent
	// wording matches identically. Requiring "port <number>" immediately
	// before "is already in use" scopes the match to podman's port
	// wording and excludes the name conflict, which never has a bare
	// port number in that position.
	regexp.MustCompile(`(?i)\bport \d+ is already in use\b`),

	// Docker's userland-proxy wording, from the pre-native-listener path
	// still used on Docker Desktop for Mac/Windows and on Linux when the
	// userland proxy is enabled:
	//   Error starting userland proxy: Bind for 0.0.0.0:80: unexpected
	//   error (Failure EADDRINUSE)
	// Confirmed as real, current wording via docker/for-mac issue #3755
	// ("Unable To Use Localhost Port 80 ; Error starting userland proxy:
	// Bind for 0.0.0.0:80: unexpected error (Failure EADDRINUSE)"),
	// which quotes this exact daemon message verbatim. This was missing
	// from the earlier pattern list, so this specific collision wording
	// fell through to failing loudly instead of skipping.
	regexp.MustCompile(`(?i)unexpected error \(failure eaddrinuse\)`),
}

// classifyPortCollision reports whether combinedOutput -- the combined
// stdout/stderr of a failed `docker compose up` -- names a host port
// already in use, as opposed to a broken compose file or image reference.
//
// This function runs no commands, opens no sockets, and touches no
// *testing.T; it is pure input to output, so it can be unit tested
// directly without Docker or the network. See this function's test and
// dsnDecision in dbtest.go for why that separation exists.
func classifyPortCollision(combinedOutput string) bool {
	for _, pattern := range portCollisionPatterns {
		if pattern.MatchString(combinedOutput) {
			return true
		}
	}
	return false
}

// composePublishedPort pairs a host port compose.yaml publishes with the
// RASQL_TEST_*_DSN variable that points a test at whatever already holds
// that port.
type composePublishedPort struct {
	port   string
	envVar string
}

// composePublishedPorts lists the ports compose.yaml publishes, matching
// the fixed ports documented in the package doc (5432 for PostgreSQL, 3306
// for MySQL).
var composePublishedPorts = []composePublishedPort{
	{port: "5432", envVar: postgresEnvVar},
	{port: "3306", envVar: mysqlEnvVar},
}

// findConflictingPort determines which port in candidates is already bound
// by attempting to bind each one directly -- with the same address family
// and wildcard address Docker itself binds ("0.0.0.0:<port>", per the
// vendor wording collected in portCollisionPatterns) -- rather than
// extracting a number from Docker's failure text. A bind that fails is
// treated as that port already being held by something else; the first
// such port found is returned. Docker's own bring-up attempt has already
// failed and exited by the time this runs, so it is not still holding the
// port itself.
//
// This must only be called after classifyPortCollision has already
// reported a collision for the same bring-up failure -- see
// ensureComposeUp in compose.go for why that order matters. Calling this
// on its own, without that prior classification, would say "port 5432 is
// taken" any time something else on the machine happens to be listening
// there, regardless of whether that has anything to do with the compose
// failure under investigation.
//
// port is "" when none of candidates could be bound to determine a
// conflict -- e.g. the collision was real but resolved between Docker's
// attempt and this probe, or Docker's own wording matched a collision
// pattern for a port this compose file does not publish. The caller must
// not guess at a port in that case; see bringUpFailureMessage.
func findConflictingPort() (string, string) {
	return findConflictingPortAmong(composePublishedPorts)
}

// findConflictingPortAmong is findConflictingPort's logic over an explicit
// candidate list, split out so a test can supply ports it controls (an
// ephemeral port it is deliberately holding open, or one it knows is free)
// instead of the real 5432/3306 compose.yaml publishes, which may or may
// not be free on the machine running the test.
func findConflictingPortAmong(candidates []composePublishedPort) (string, string) {
	for _, candidate := range candidates {
		listener, err := net.Listen("tcp", "0.0.0.0:"+candidate.port)
		if err != nil {
			return candidate.port, candidate.envVar
		}
		listener.Close()
	}
	return "", ""
}

// bringUpFailureMessage builds the skip diagnostic for a bring-up failure
// classifyPortCollision has already identified as a port collision, once
// findConflictingPort has run. port/envVar being "" means the probe could
// not determine which port was the problem, which is reported truthfully
// rather than guessed at.
func bringUpFailureMessage(port, envVar string) string {
	if port == "" {
		return fmt.Sprintf(
			"a host port docker compose wanted appears to already be in use, but the port could not be determined; set %s or %s to use the database you already have running instead",
			postgresEnvVar, mysqlEnvVar,
		)
	}
	return fmt.Sprintf(
		"host port %s is already in use; set %s to use the database you already have running instead of bringing up compose",
		port, envVar,
	)
}
