package rasql_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestGraphPublicCompileFixture(t *testing.T) {
	fixture, err := filepath.Abs("testdata/compile/graph_api")
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("go", "test", ".")
	cmd.Dir = fixture
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("graph public compile fixture failed: %v\n%s", err, output)
	}
}
