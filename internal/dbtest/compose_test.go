//go:build unix

package dbtest

import (
	"strings"
	"testing"
)

// TestComposeUnavailableError proves the two diagnostics this rescope keeps
// distinct: `docker compose version` failing because the CLI does not
// recognize "compose" as a subcommand at all (neither runner is available)
// versus failing because the plugin IS recognized but does not run (a
// genuine execution failure, e.g. a broken shim or a permission error).
// Collapsing both into "the installed plugin does not run" -- what an
// earlier version of this function did unconditionally -- is actively
// wrong for the first case: there is no installed plugin to speak of.
func TestComposeUnavailableError(t *testing.T) {
	t.Run("missing subcommand reports neither runner available", func(t *testing.T) {
		composeOut := []byte("docker: 'compose' is not a docker command.\nSee 'docker --help'\n")
		err := composeUnavailableError(composeOut)
		if err == nil {
			t.Fatal("composeUnavailableError returned nil, want an error")
		}
		message := err.Error()
		if !strings.Contains(message, "neither") {
			t.Fatalf("composeUnavailableError message %q does not say neither runner is available", message)
		}
		if strings.Contains(message, "does not run") {
			t.Fatalf("composeUnavailableError message %q wrongly claims an installed plugin does not run", message)
		}
		if !strings.Contains(message, "docker: 'compose' is not a docker command.") {
			t.Fatalf("composeUnavailableError message %q does not quote the CLI's own first output line", message)
		}
	})

	t.Run("genuine execution failure keeps the installed-plugin wording", func(t *testing.T) {
		composeOut := []byte("docker: error executing plugin: fork/exec /usr/libexec/docker/cli-plugins/docker-compose: permission denied\n")
		err := composeUnavailableError(composeOut)
		if err == nil {
			t.Fatal("composeUnavailableError returned nil, want an error")
		}
		message := err.Error()
		if strings.Contains(message, "neither") {
			t.Fatalf("composeUnavailableError message %q falsely claims neither command is available", message)
		}
		if !strings.Contains(message, "does not run") {
			t.Fatalf("composeUnavailableError message %q does not say the installed plugin does not run", message)
		}
		if !strings.Contains(message, "permission denied") {
			t.Fatalf("composeUnavailableError message %q does not quote the plugin's own first output line", message)
		}
	})
}

// TestComposeServicesBringUpOnlyTheirOwnService pins the two compose.yaml
// services this package can bring up as distinct, non-overlapping targets:
// postgresComposeService and mysqlComposeService must name different
// services and different ports, so ensureComposeUp brings up exactly one
// database's container and probes exactly that one database's port on a
// collision, never the other's. An earlier version of this package passed
// no service name to `docker compose up` at all, which started both
// services regardless of which database the caller asked for, so a port
// collision on the OTHER service skipped the caller's own test and named
// the wrong RASQL_TEST_*_DSN variable.
func TestComposeServicesBringUpOnlyTheirOwnService(t *testing.T) {
	if postgresComposeService.name == mysqlComposeService.name {
		t.Fatalf("postgresComposeService and mysqlComposeService share the same compose service name %q", postgresComposeService.name)
	}
	if postgresComposeService.port == mysqlComposeService.port {
		t.Fatalf("postgresComposeService and mysqlComposeService share the same port %q", postgresComposeService.port)
	}
	if postgresComposeService.envVar == mysqlComposeService.envVar {
		t.Fatalf("postgresComposeService and mysqlComposeService share the same env var %q", postgresComposeService.envVar)
	}
	if postgresComposeService.port != "5432" || postgresComposeService.envVar != postgresEnvVar {
		t.Fatalf("postgresComposeService = %+v, want port 5432 and %s", postgresComposeService, postgresEnvVar)
	}
	if mysqlComposeService.port != "3306" || mysqlComposeService.envVar != mysqlEnvVar {
		t.Fatalf("mysqlComposeService = %+v, want port 3306 and %s", mysqlComposeService, mysqlEnvVar)
	}
}
