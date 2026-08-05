//go:build unix

package dbtest

import (
	"testing"
)

// TestComposeServicesBringUpOnlyTheirOwnService pins the two compose.yaml
// services this package can bring up as distinct, non-overlapping targets:
// postgresComposeService and mysqlComposeService must name different
// services and different environment variables, so ensureComposeUp brings
// up exactly one database's container per call. An earlier version of this
// package passed no service name to `docker compose up` at all, which
// started both services regardless of which database the caller asked for.
func TestComposeServicesBringUpOnlyTheirOwnService(t *testing.T) {
	if postgresComposeService.name == mysqlComposeService.name {
		t.Fatalf("postgresComposeService and mysqlComposeService share the same compose service name %q", postgresComposeService.name)
	}
	if postgresComposeService.envVar == mysqlComposeService.envVar {
		t.Fatalf("postgresComposeService and mysqlComposeService share the same env var %q", postgresComposeService.envVar)
	}
	if postgresComposeService.envVar != postgresEnvVar {
		t.Fatalf("postgresComposeService.envVar = %q, want %s", postgresComposeService.envVar, postgresEnvVar)
	}
	if mysqlComposeService.envVar != mysqlEnvVar {
		t.Fatalf("mysqlComposeService.envVar = %q, want %s", mysqlComposeService.envVar, mysqlEnvVar)
	}
}
