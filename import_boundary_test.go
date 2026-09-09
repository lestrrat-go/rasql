package rasql_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRootDoesNotImportRemovedDynamicPackage keeps the root package free of
// the retired facade without asking go list to load a package that no longer
// exists. Boundaries among the supported root and exec packages remain
// compiler-enforced import-cycle checks.
func TestRootDoesNotImportRemovedDynamicPackage(t *testing.T) {
	require.NotContains(t, deps(t, "github.com/lestrrat-go/rasql"),
		"github.com/lestrrat-go/rasql/dynamic",
		"the root package must not import the retired rasql/dynamic facade")
}

// deps returns the import paths go reports for pkg, one per element.
func deps(t *testing.T, pkg string) []string {
	t.Helper()
	output, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
	require.NoError(t, err, "%s", output)
	return strings.Fields(string(output))
}
