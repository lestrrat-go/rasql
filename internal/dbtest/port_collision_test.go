package dbtest

import (
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
// classified as a collision with the right port extracted, plus several
// unrelated bring-up failures that must NOT be -- including two messages
// that share vocabulary with real collisions (moby's and podman's
// container-name conflicts, and moby's static-IP conflict) but are not
// port collisions -- so a message this classifier has never seen keeps
// failing loudly rather than silently skipping. This needs no Docker: it
// drives the classifier directly on fixed strings.
func TestClassifyPortCollision(t *testing.T) {
	collisions := []struct {
		name     string
		output   string
		wantPort string
	}{
		{
			name: "docker engine classic wording, wrapped by external connectivity error",
			output: "Error response from daemon: failed to set up container networking: " +
				"driver failed programming external connectivity on endpoint rasql-postgres-1: " +
				"Bind for 0.0.0.0:5432 failed: port is already allocated",
			wantPort: "5432",
		},
		{
			name: "docker engine >=23.x wording after moving off the userland proxy",
			output: "Error response from daemon: Ports are not available: exposing port TCP " +
				"0.0.0.0:3306 -> 0.0.0.0:0: listen tcp 0.0.0.0:3306: bind: address already in use",
			wantPort: "3306",
		},
		{
			name:     "podman rootlessport shim",
			output:   `Error: rootlessport listen tcp 0.0.0.0:9080: bind: address already in use`,
			wantPort: "9080",
		},
		{
			name:     "podman rootlessport shim, ipv6 form",
			output:   `Error: rootlessport listen tcp6 [::]:5432: bind: address already in use`,
			wantPort: "5432",
		},
		{
			name:     "podman's own higher-level phrasing",
			output:   "Error: unable to start container 4f3c9e2b: port 8080 is already in use",
			wantPort: "8080",
		},
		{
			name: "standalone docker-compose v1 wrapping the daemon's own text",
			output: "ERROR: for postgres  Cannot start service postgres: driver failed programming " +
				"external connectivity on endpoint rasql_postgres_1: Bind for 0.0.0.0:5432 failed: " +
				"port is already allocated",
			wantPort: "5432",
		},
		{
			name:     "podman's cannot-listen wrapper around the Go net bind error",
			output:   "cannot listen on the TCP port: listen tcp4 :5432: bind: address already in use",
			wantPort: "5432",
		},
		{
			// The WSA tail text doesn't match any portNumberPatterns
			// entry (those are anchored to "bind: address already in
			// use" or the userland-proxy's EADDRINUSE wording, neither
			// of which this message ends in), so the port is
			// unextractable here -- the same "collision but no port"
			// outcome as the last case in this table.
			name: "docker desktop for windows wrapping its own WSA bind error",
			output: "Error response from daemon: Ports are not available: exposing port TCP " +
				"0.0.0.0:5432 -> 0.0.0.0:0: listen tcp 0.0.0.0:5432: bind: Only one usage of each " +
				"socket address (protocol/network address/port) is normally permitted.",
			wantPort: "",
		},
		{
			name:     "docker's userland-proxy wording",
			output:   "Error starting userland proxy: Bind for 0.0.0.0:80: unexpected error (Failure EADDRINUSE)",
			wantPort: "80",
		},
		{
			name:     "collision recognized even when the port cannot be extracted",
			output:   "Error response from daemon: Ports are not available: something unexpected happened",
			wantPort: "",
		},
	}
	for _, tc := range collisions {
		t.Run(tc.name, func(t *testing.T) {
			collision, port := classifyPortCollision(tc.output)
			if !collision {
				t.Fatalf("classifyPortCollision(%q) reported no collision; want collision=true", tc.output)
			}
			if port != tc.wantPort {
				t.Fatalf("classifyPortCollision(%q) port = %q, want %q", tc.output, port, tc.wantPort)
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
			collision, port := classifyPortCollision(tc.output)
			if collision {
				t.Fatalf("classifyPortCollision(%q) reported a collision (port %q); want collision=false so this keeps failing loudly", tc.output, port)
			}
		})
	}
}

// TestClassifyBringUpFailure proves the port-collision carve-out narrows
// the fail-loudly rule and nothing more: a collision skips and names the
// port, and every other bring-up failure still reports skip=false, which is
// what tells ensureComposeUp to t.Fatalf instead of t.Skipf. This is the
// same seam dsnDecision uses elsewhere in this package -- a pure decision
// function tested directly -- so this needs no Docker either.
func TestClassifyBringUpFailure(t *testing.T) {
	t.Run("port collision skips and names the port", func(t *testing.T) {
		output := []byte("Error response from daemon: driver failed programming external connectivity on endpoint x: " +
			"Bind for 0.0.0.0:5432 failed: port is already allocated")
		skip, message := classifyBringUpFailure(output)
		if !skip {
			t.Fatal("classifyBringUpFailure reported skip=false for a port collision; want skip=true")
		}
		if !containsAll(message, "5432", postgresEnvVar) {
			t.Fatalf("classifyBringUpFailure message %q does not name both the port and %s", message, postgresEnvVar)
		}
	})

	t.Run("mysql port collision names the mysql env var, not the postgres one", func(t *testing.T) {
		output := []byte("Error: rootlessport listen tcp 0.0.0.0:3306: bind: address already in use")
		skip, message := classifyBringUpFailure(output)
		if !skip {
			t.Fatal("classifyBringUpFailure reported skip=false for a port collision; want skip=true")
		}
		if !containsAll(message, "3306", mysqlEnvVar) {
			t.Fatalf("classifyBringUpFailure message %q does not name both the port and %s", message, mysqlEnvVar)
		}
	})

	t.Run("collision with an unextractable port still skips, without inventing a port", func(t *testing.T) {
		output := []byte("Error response from daemon: Ports are not available: something unexpected happened")
		skip, message := classifyBringUpFailure(output)
		if !skip {
			t.Fatal("classifyBringUpFailure reported skip=false for a recognized collision; want skip=true")
		}
		if !containsAll(message, "could not be determined") {
			t.Fatalf("classifyBringUpFailure message %q does not say the port could not be determined", message)
		}
	})

	t.Run("broken compose file still fails loudly, not skips", func(t *testing.T) {
		output := []byte(`service "postgres" refers to undefined network custom_net: invalid compose project`)
		skip, message := classifyBringUpFailure(output)
		if skip {
			t.Fatalf("classifyBringUpFailure reported skip=true (message %q) for a broken compose file; want skip=false so ensureComposeUp fails loudly", message)
		}
	})

	t.Run("broken image reference still fails loudly, not skips", func(t *testing.T) {
		output := []byte("Error response from daemon: manifest for postgres:99-alpine not found: manifest unknown")
		skip, message := classifyBringUpFailure(output)
		if skip {
			t.Fatalf("classifyBringUpFailure reported skip=true (message %q) for a broken image reference; want skip=false so ensureComposeUp fails loudly", message)
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
