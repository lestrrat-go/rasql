package rasql

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMutationCompileFixtures(t *testing.T) {
	positive, err := filepath.Abs(filepath.Join("testdata", "compile", "mutation_api", "positive"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = positive
	command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("positive mutation fixture failed: %v\n%s", err, output)
	}
	for _, fixture := range []struct{ name, diagnostic string }{
		{"wrong_value", "cannot use"},
		{"wrong_nullable", "does not match"},
		{"clear_nonnull", "does not match"},
		{"foreign_row", "does not match"},
	} {
		negative, err := filepath.Abs(filepath.Join("testdata", "compile", "mutation_api", fixture.name))
		if err != nil {
			t.Fatal(err)
		}
		command = exec.Command("go", "test", "./...")
		command.Dir = negative
		command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), fixture.diagnostic) {
			t.Fatalf("negative mutation fixture %s diagnostic = %s", fixture.name, output)
		}
	}
}
