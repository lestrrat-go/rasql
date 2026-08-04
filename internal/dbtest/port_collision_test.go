//go:build unix

package dbtest

import (
	"net"
	"strconv"
	"strings"
	"testing"
)

// TestClassifyPortCollision pins classifyPortCollision against real
// `docker compose up` failure text: several worded-differently port-in-use
// messages (Docker Engine's classic and newer wording, podman's
// rootlessport shim, podman's own higher-level phrasing, podman's
// cannot-listen wrapper, Docker Desktop for Windows' WSA bind error, and
// Docker's userland-proxy EADDRINUSE wording -- see the comments on
// portCollisionPatterns for where each was confirmed) that must all be
// classified as a collision, plus several unrelated bring-up failures that
// must NOT be -- including two messages that share vocabulary with real
// collisions (moby's and podman's container-name conflicts, and moby's
// static-IP conflict) but are not port collisions -- so a message this
// classifier has never seen keeps failing loudly rather than silently
// skipping. This needs no Docker and no network: it drives the classifier
// directly on fixed strings.
func TestClassifyPortCollision(t *testing.T) {
	collisions := []struct {
		name   string
		output string
	}{
		{
			name: "docker engine classic wording, wrapped by external connectivity error",
			output: "Error response from daemon: failed to set up container networking: " +
				"driver failed programming external connectivity on endpoint rasql-postgres-1: " +
				"Bind for 0.0.0.0:5432 failed: port is already allocated",
		},
		{
			name: "docker engine >=23.x wording after moving off the userland proxy",
			output: "Error response from daemon: Ports are not available: exposing port TCP " +
				"0.0.0.0:3306 -> 0.0.0.0:0: listen tcp 0.0.0.0:3306: bind: address already in use",
		},
		{
			name:   "podman rootlessport shim",
			output: `Error: rootlessport listen tcp 0.0.0.0:9080: bind: address already in use`,
		},
		{
			name:   "podman rootlessport shim, ipv6 form",
			output: `Error: rootlessport listen tcp6 [::]:5432: bind: address already in use`,
		},
		{
			name:   "podman's own higher-level phrasing",
			output: "Error: unable to start container 4f3c9e2b: port 8080 is already in use",
		},
		{
			name: "standalone docker-compose v1 wrapping the daemon's own text",
			output: "ERROR: for postgres  Cannot start service postgres: driver failed programming " +
				"external connectivity on endpoint rasql_postgres_1: Bind for 0.0.0.0:5432 failed: " +
				"port is already allocated",
		},
		{
			name:   "podman's cannot-listen wrapper around the Go net bind error",
			output: "cannot listen on the TCP port: listen tcp4 :5432: bind: address already in use",
		},
		{
			// Text extraction could never pull a port out of this
			// message's WSA tail (it doesn't end in "bind: address
			// already in use" or the userland-proxy's EADDRINUSE
			// wording), which used to make a real collision falsely
			// report "the port could not be determined". Classification
			// alone is what this test proves; findConflictingPort (see
			// TestFindConflictingPortAmong) is what actually determines
			// the port now, and it does not depend on this wording at
			// all.
			name: "docker desktop for windows wrapping its own WSA bind error",
			output: "Error response from daemon: Ports are not available: exposing port TCP " +
				"0.0.0.0:5432 -> 0.0.0.0:0: listen tcp 0.0.0.0:5432: bind: Only one usage of each " +
				"socket address (protocol/network address/port) is normally permitted.",
		},
		{
			name:   "docker's userland-proxy wording",
			output: "Error starting userland proxy: Bind for 0.0.0.0:80: unexpected error (Failure EADDRINUSE)",
		},
		{
			name:   "collision recognized even when no port number appears in the text at all",
			output: "Error response from daemon: Ports are not available: something unexpected happened",
		},
	}
	for _, tc := range collisions {
		t.Run(tc.name, func(t *testing.T) {
			if !classifyPortCollision(tc.output) {
				t.Fatalf("classifyPortCollision(%q) = false, want true", tc.output)
			}
		})
	}

	notCollisions := []struct {
		name   string
		output string
	}{
		{
			name:   "broken image reference",
			output: "Error response from daemon: manifest for postgres:99-alpine not found: manifest unknown",
		},
		{
			name:   "pull access denied for a nonexistent image",
			output: "Error response from daemon: pull access denied for rasql/nonexistent, repository does not exist or may require 'docker login'",
		},
		{
			name:   "broken compose file referencing an undefined network",
			output: `service "postgres" refers to undefined network custom_net: invalid compose project`,
		},
		{
			name:   "healthcheck never passing",
			output: "container rasql-postgres-1 did not become healthy in time",
		},
		{
			name:   "unrelated network failure reaching the registry",
			output: `Error response from daemon: Get "https://registry-1.docker.io/v2/": dial tcp: lookup registry-1.docker.io: no such host`,
		},
		{
			name: "docker container-name conflict, not a port collision",
			output: `Error response from daemon: Conflict. The container name "/rasql-postgres-1" is already ` +
				`in use by container "abc123def456789". You have to remove (or rename) that container ` +
				`to be able to reuse that name.`,
		},
		{
			name: "podman's equivalent container-name conflict, not a port collision",
			output: `Error: error creating container storage: the container name "rasql-postgres-1" is already ` +
				`in use by container "abc123def456789789abc123def456789abc123def456789abc123def456789". ` +
				`You have to remove that container to be able to reuse that name.`,
		},
		{
			name:   "moby's static-IP conflict, not a port collision",
			output: "Error response from daemon: Address already in use",
		},
	}
	for _, tc := range notCollisions {
		t.Run(tc.name, func(t *testing.T) {
			if classifyPortCollision(tc.output) {
				t.Fatalf("classifyPortCollision(%q) = true, want false so this keeps failing loudly", tc.output)
			}
		})
	}
}

// TestFindConflictingPortAmong proves the port probe itself: given a port
// this test is deliberately holding open, it is reported as the conflict;
// given only free ports, none is reported, truthfully, rather than a guess.
// It uses ports this test controls -- an OS-assigned ephemeral port it
// binds and holds itself -- rather than the real 5432/3306 compose.yaml
// publishes, since whether those are free depends on the machine running
// the test.
func TestFindConflictingPortAmong(t *testing.T) {
	t.Run("a genuinely bound port is found and its env var reported", func(t *testing.T) {
		held, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("bind an ephemeral port to hold open: %v", err)
		}
		defer held.Close()
		heldPort := held.Addr().(*net.TCPAddr).Port

		free, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("bind an ephemeral port to find its number: %v", err)
		}
		freePort := free.Addr().(*net.TCPAddr).Port
		if err := free.Close(); err != nil {
			t.Fatalf("release the free port before probing: %v", err)
		}

		candidates := []composePublishedPort{
			{port: strconv.Itoa(freePort), envVar: postgresEnvVar},
			{port: strconv.Itoa(heldPort), envVar: mysqlEnvVar},
		}
		port, envVar := findConflictingPortAmong(candidates)
		if port != strconv.Itoa(heldPort) {
			t.Fatalf("findConflictingPortAmong port = %q, want %q (the port this test is holding open)", port, strconv.Itoa(heldPort))
		}
		if envVar != mysqlEnvVar {
			t.Fatalf("findConflictingPortAmong envVar = %q, want %q", envVar, mysqlEnvVar)
		}
	})

	t.Run("no candidate bound reports that the port could not be determined", func(t *testing.T) {
		free, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("bind an ephemeral port to find its number: %v", err)
		}
		freePort := free.Addr().(*net.TCPAddr).Port
		if err := free.Close(); err != nil {
			t.Fatalf("release the free port before probing: %v", err)
		}

		port, envVar := findConflictingPortAmong([]composePublishedPort{{port: strconv.Itoa(freePort), envVar: postgresEnvVar}})
		if port != "" {
			t.Fatalf("findConflictingPortAmong port = %q, want \"\" since no candidate was actually bound", port)
		}
		if envVar != "" {
			t.Fatalf("findConflictingPortAmong envVar = %q, want \"\" since no candidate was actually bound", envVar)
		}
	})
}

// TestBringUpFailureMessage pins the two skip-message shapes: a determined
// port names both the port and its env var, and an undetermined port says
// so truthfully instead of guessing.
func TestBringUpFailureMessage(t *testing.T) {
	t.Run("determined port names the port and env var", func(t *testing.T) {
		message := bringUpFailureMessage("5432", postgresEnvVar)
		if !containsAll(message, "5432", postgresEnvVar) {
			t.Fatalf("bringUpFailureMessage message %q does not name both the port and %s", message, postgresEnvVar)
		}
	})

	t.Run("undetermined port says so instead of guessing", func(t *testing.T) {
		message := bringUpFailureMessage("", "")
		if !containsAll(message, "could not be determined") {
			t.Fatalf("bringUpFailureMessage message %q does not say the port could not be determined", message)
		}
	})
}

func containsAll(s string, substrings ...string) bool {
	for _, sub := range substrings {
		if !strings.Contains(s, sub) {
			return false
		}
	}
	return true
}
