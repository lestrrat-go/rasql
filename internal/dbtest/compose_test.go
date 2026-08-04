//go:build unix

package dbtest

import (
	"strings"
	"testing"
)

// TestComposeUnavailableError proves the compose diagnostic this rescope
// fixes: when `docker compose version` fails and no standalone
// docker-compose binary exists either, the message must quote the
// plugin's own failure output rather than falsely claiming neither command
// is available. An earlier version of probeCompose ran `docker compose
// version` with Run rather than CombinedOutput, which discarded the
// plugin's own output entirely, so there was never anything here to quote;
// see probeCompose's use of CombinedOutput for the fix that makes
// composeOut available at all.
func TestComposeUnavailableError(t *testing.T) {
	composeOut := []byte("docker: 'compose' is not a docker command.\nSee 'docker --help'\n")
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
	if !strings.Contains(message, "docker: 'compose' is not a docker command.") {
		t.Fatalf("composeUnavailableError message %q does not quote the plugin's own first output line", message)
	}
}
