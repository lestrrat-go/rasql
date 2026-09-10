package rasql

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestRuntimeAPICompileFixture(t *testing.T) {
	directory, err := filepath.Abs(filepath.Join("testdata", "compile", "runtime_api", "positive"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = directory
	// GOCACHE is deliberately not overridden here: see the matching comment
	// in query_compile_test.go's TestQueryAPICompileFixture.
	command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture failed: %v\n%s", err, output)
	}
}
