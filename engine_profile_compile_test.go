package rasql_test

import (
	"os/exec"
	"strings"
	"testing"
)

func TestEngineProfileCompileFixtures(t *testing.T) {
	pass := exec.Command("go", "test", "./testdata/compile/engine_profile")
	if output, err := pass.CombinedOutput(); err != nil {
		t.Fatalf("compile-pass fixture failed: %v\n%s", err, output)
	}
	fail := exec.Command("go", "test", "-tags=compile_fail", "./testdata/compile/engine_profile")
	output, err := fail.CombinedOutput()
	if err == nil {
		t.Fatal("compile-fail fixture unexpectedly passed")
	}
	if !strings.Contains(string(output), "profile") {
		t.Fatalf("compile-fail fixture failed for an unexpected reason: %s", output)
	}
}
