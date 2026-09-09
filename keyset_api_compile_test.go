package rasql_test

import (
	"os/exec"
	"testing"
)

func TestR5ExternalKeysetCompileFixture(t *testing.T) {
	command := exec.Command("go", "test", ".")
	command.Dir = "testdata/compile/keyset_api"
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("keyset API compile fixture failed: %v\n%s", err, output)
	}
}
