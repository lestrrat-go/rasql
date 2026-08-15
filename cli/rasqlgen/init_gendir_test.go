package rasqlgen

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestInitRejectsGenDirOutsideModule requires that -gen-dir is refused
// before anything is written when it resolves outside the module the
// scaffold is meant to join, whether that is spelled as an absolute path
// or as a relative one that walks out of the module with "..".
//
// A test that only inspected gen/main.go's content would pass even if this
// check were missing entirely, since a missing check simply never runs;
// the assertion that actually pins the defect is the one at the end of
// each case, which drives the real "go" binary against the rejected
// location and requires it to report the location the way it reports any
// directory outside the main module -- proving the write init refused
// really would have produced a scaffold `go generate ./...` could never
// reach, not merely one this check dislikes on principle.
func TestInitRejectsGenDirOutsideModule(t *testing.T) {
	root := initModuleDir(t)
	outside := t.TempDir()

	testCases := map[string]struct {
		genDir func() string
	}{
		"absolute path outside the module": {
			genDir: func() string { return outside },
		},
		"relative path that walks out of the module": {
			genDir: func() string {
				rel, err := filepath.Rel(root, outside)
				require.NoError(t, err)
				return rel
			},
		},
	}
	for name, testCase := range testCases {
		t.Run(name, func(t *testing.T) {
			genDir := testCase.genDir()

			var out bytes.Buffer
			err := Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "-gen-dir", genDir}, &out)
			require.Error(t, err)
			require.ErrorContains(t, err, "-gen-dir")
			require.ErrorContains(t, err, "outside the module")
			require.Empty(t, out.String())

			_, statErr := os.Stat(filepath.Join(outside, "main.go"))
			require.ErrorIs(t, statErr, os.ErrNotExist)

			// Ask the "go" tool itself whether it can even see a package at
			// the rejected location from this module: it cannot, which is
			// exactly the defect this check exists to prevent.
			rel, relErr := filepath.Rel(root, outside)
			require.NoError(t, relErr)
			command := exec.CommandContext(t.Context(), "go", "list", filepath.ToSlash(rel)+"/...")
			command.Dir = root
			toolOutput, toolErr := command.CombinedOutput()
			require.Error(t, toolErr, string(toolOutput))
			require.Contains(t, string(toolOutput), "does not contain main module")
		})
	}
}

// TestInitAcceptsGenDirAtTheModuleRoot is the control for the test above:
// -gen-dir resolving to the module root itself is the boundary the check
// must not refuse, since it is not outside the module.
func TestInitAcceptsGenDirAtTheModuleRoot(t *testing.T) {
	initModuleDir(t)

	var out bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "-gen-dir", "."}, &out))
	require.FileExists(t, "main.go")
}

// TestInitRejectsGenDirWithForeignPackage requires that -gen-dir is
// refused before anything is written when it already holds a Go file
// declaring some package other than "main", and names that package.
//
// As with the test above, the assertion that actually pins the defect is
// the last one: it writes the exact scaffold init refused to write -- the
// way an unfixed init would have -- into the same directory by hand, and
// then drives a real "go build" against it. The toolchain's own refusal is
// what proves the check was warranted, rather than trusting this test's
// own idea of what the toolchain would say.
func TestInitRejectsGenDirWithForeignPackage(t *testing.T) {
	root := initModuleDir(t)
	toolsDir := filepath.Join(root, "tools")
	require.NoError(t, os.MkdirAll(toolsDir, 0o755))
	helperSource := "package tools\n\nfunc Helper() {}\n"
	require.NoError(t, os.WriteFile(filepath.Join(toolsDir, "helper.go"), []byte(helperSource), 0o600))

	var out bytes.Buffer
	err := Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "-gen-dir", "tools"}, &out)
	require.Error(t, err)
	require.ErrorContains(t, err, "-gen-dir")
	require.ErrorContains(t, err, `"tools"`)
	require.ErrorContains(t, err, "helper.go")
	require.Empty(t, out.String())

	_, statErr := os.Stat(filepath.Join(toolsDir, "main.go"))
	require.ErrorIs(t, statErr, os.ErrNotExist)
	after, readErr := os.ReadFile(filepath.Join(toolsDir, "helper.go"))
	require.NoError(t, readErr)
	require.Equal(t, helperSource, string(after))

	require.NoError(t, os.WriteFile(filepath.Join(toolsDir, "main.go"), []byte("package main\n\nfunc main() {}\n"), 0o600))
	command := exec.CommandContext(t.Context(), "go", "build", "./tools/...")
	command.Dir = root
	toolOutput, toolErr := command.CombinedOutput()
	require.Error(t, toolErr, string(toolOutput))
	require.Contains(t, string(toolOutput), "found packages")
}

// TestInitAcceptsGenDirWithOnlyAMainPackage is the control for the test
// above: a -gen-dir already holding a file that declares "package main" is
// not a foreign package -- it is the same package the scaffold declares --
// so it must not be refused. Whether an existing main.go itself may be
// overwritten is -force's separate question, covered by
// TestInitRefusesAnExistingFile and TestInitForceOverwrites.
func TestInitAcceptsGenDirWithOnlyAMainPackage(t *testing.T) {
	initModuleDir(t)
	require.NoError(t, os.MkdirAll("tools", 0o755))
	require.NoError(t, os.WriteFile(filepath.Join("tools", "helper.go"), []byte("package main\n\nfunc helper() {}\n"), 0o600))

	var out bytes.Buffer
	require.NoError(t, Run([]string{"init", "-dialect", "sqlite", "-package", "store", "-output", "internal/store", "-gen-dir", "tools"}, &out))
	require.FileExists(t, filepath.Join("tools", "main.go"))
}
