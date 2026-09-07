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
	command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture failed: %v\n%s", err, output)
	}
}
