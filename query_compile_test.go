package rasql

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestQueryAPICompileFixture(t *testing.T) {
	directory, err := filepath.Abs(filepath.Join("testdata", "compile", "query_api", "positive"))
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

func TestQueryAPINegativeCompileFixtures(t *testing.T) {
	for _, fixture := range []struct{ name, diagnostic string }{{"wrong_value", "cannot use"}, {"is_null_nonnull", "does not match"}, {"scalar_where", "cannot use"}, {"mismatched_projection", "cannot use"}, {"optional_required", "does not match"}} {
		directory, err := filepath.Abs(filepath.Join("testdata", "compile", "query_api", fixture.name))
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("go", "test", "./...")
		command.Dir = directory
		command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "GOCACHE="+filepath.Join(t.TempDir(), "cache"))
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), fixture.diagnostic) {
			t.Fatalf("fixture %s diagnostic = %s", fixture.name, output)
		}
	}
}
