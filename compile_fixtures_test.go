package rasql_test

import (
	"os/exec"
	"path/filepath"
	"testing"
)

func TestPublicAPICompiles(t *testing.T) {
	t.Run("graph", func(t *testing.T) {
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
	})

	t.Run("keyset paging", func(t *testing.T) {
		command := exec.Command("go", "test", ".")
		command.Dir = "testdata/compile/keyset_api"
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("keyset API compile fixture failed: %v\n%s", err, output)
		}
	})
}
