package main

import (
	"bytes"
	"testing"
)

func TestRun(t *testing.T) {
	var output bytes.Buffer
	err := run(t.Context(), &output)
	if err != nil {
		t.Fatalf("run taskboard: %s", err)
	}

	const expected = "Open tasks for Website refresh:\n" +
		"- P1 Draft rollout plan\n" +
		"- P2 Review onboarding emails\n"
	if output.String() != expected {
		t.Errorf("run taskboard output = %q, want %q", output.String(), expected)
	}
}
