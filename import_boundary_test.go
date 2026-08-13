package rasql_test

import (
	"os/exec"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRootDoesNotImportDynamic states the rule the whole layer split turns on:
// dynamic imports rasql for DB, so rasql can never import dynamic. The Go
// compiler already refuses the cycle; this names the rule when it is broken.
func TestRootDoesNotImportDynamic(t *testing.T) {
	output, err := exec.Command("go", "list", "-deps", "github.com/lestrrat-go/rasql").CombinedOutput()
	require.NoError(t, err, "%s", output)
	require.NotContains(t, string(output), "github.com/lestrrat-go/rasql/dynamic",
		"the root package must not depend on rasql/dynamic: dynamic imports rasql for DB, so anything both layers need belongs in internal/")
}
