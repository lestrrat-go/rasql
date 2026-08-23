package rasql_test

import (
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestRootAndDynamicDoNotImportEachOther states the layering rule the compiler
// stopped enforcing when the runtime moved into rasql/exec. Before that move
// dynamic imported rasql, so rasql importing dynamic was a cycle the compiler
// refused. Now dynamic imports rasql/exec instead, and neither direction is a
// cycle: either import would compile, and only this test refuses it. The two
// directions involving rasql/exec are left out on purpose, because both are
// still cycles the compiler catches.
func TestRootAndDynamicDoNotImportEachOther(t *testing.T) {
	require.NotContains(t, deps(t, "github.com/lestrrat-go/rasql"),
		"github.com/lestrrat-go/rasql/dynamic",
		"the root package must not import rasql/dynamic: the typed facade is the layer above it")
	require.NotContains(t, deps(t, "github.com/lestrrat-go/rasql/dynamic"),
		"github.com/lestrrat-go/rasql",
		"rasql/dynamic must not import the root package: it needs the runtime, which is rasql/exec")
}

// deps returns the import paths go reports for pkg, one per element.
func deps(t *testing.T, pkg string) []string {
	t.Helper()
	output, err := exec.Command("go", "list", "-deps", pkg).CombinedOutput()
	require.NoError(t, err, "%s", output)
	return strings.Fields(string(output))
}
