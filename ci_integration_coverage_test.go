package rasql_test

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const (
	modulePath       = "github.com/lestrrat-go/rasql"
	dbtestImportPath = modulePath + "/internal/dbtest"
	workflowPath     = ".github/workflows/ci.yml"
	integrationStep  = "Run the suite against live databases"
)

// TestIntegrationJobListsEveryDBTestGuardedPackage protects the invariant
// the "integration" job in .github/workflows/ci.yml relies on: every
// package holding a test guarded by internal/dbtest must be covered by
// that job's `go test -v` package list. If a package is guarded but not
// listed, the guarded test runs nowhere at all -- not in "integration"
// (the package is not passed to go test there), and not in "check"
// (no DSN is set there, so internal/dbtest skips it). That combination
// is a live test that never executes against a real engine but never
// fails either, which is the exact silent gap this repository's CI
// exists to close; see CONTRIBUTING.md's "Live database tests" and
// "Reading the integration job's log" sections.
func TestIntegrationJobListsEveryDBTestGuardedPackage(t *testing.T) {
	root, err := os.Getwd()
	require.NoError(t, err, "os.Getwd")

	guarded := guardedPackages(t, root)
	listed := integrationJobPackages(t, filepath.Join(root, workflowPath))

	for _, pkg := range guarded {
		if !anyListedCovers(listed, pkg) {
			t.Errorf("package %q has a test file importing %q, but the integration job's `go test -v` package list in %s does not cover it; add it to that list so the guarded test runs against a live database instead of nowhere at all", pkg, dbtestImportPath, workflowPath)
		}
	}
}

// guardedPackages returns the sorted, deduplicated import paths of every
// package under the root module whose directory contains at least one
// *_test.go file importing internal/dbtest. It walks the filesystem and
// parses each test file's import block directly, rather than using
// go/build package discovery, so a file's build tags (every guarded test
// file is unix-only) never hide it from this check on a non-unix host.
func guardedPackages(t *testing.T, root string) []string {
	t.Helper()

	seen := map[string]bool{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".tmp", ".worktrees", "testdata":
				return filepath.SkipDir
			}
			// A nested go.mod (e.g. sample/taskboard) marks the start of a
			// separate module; its packages are not part of this module's
			// import path space and are excluded from the workflow's
			// `go test` invocation entirely, so they are out of scope here.
			if path != root {
				if _, err := os.Stat(filepath.Join(path, "go.mod")); err == nil {
					return filepath.SkipDir
				}
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}

		file, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			value, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return err
			}
			if value != dbtestImportPath {
				continue
			}
			dir := filepath.Dir(path)
			seen[packageImportPath(root, dir)] = true
		}
		return nil
	})
	require.NoError(t, err, "walk %s for _test.go files", root)

	pkgs := make([]string, 0, len(seen))
	for pkg := range seen {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	return pkgs
}

// packageImportPath converts an absolute directory under root into the
// module import path it corresponds to.
func packageImportPath(root, dir string) string {
	rel, err := filepath.Rel(root, dir)
	if err != nil || rel == "." {
		return modulePath
	}
	return modulePath + "/" + filepath.ToSlash(rel)
}

// integrationJobPackages reads .github/workflows/ci.yml directly -- it is
// the single source of truth for what the integration job runs -- and
// returns the package specs (e.g. "./inspect/...", ".") given to `go
// test` on the integrationStep's run: line. It parses only enough of the
// YAML to find that one line, deliberately not hardcoding a second copy
// of the package list: the workflow file drifting out of step with
// itself is not a failure mode this test needs to guard against.
func integrationJobPackages(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)
	lines := strings.Split(string(data), "\n")

	stepIdx := -1
	for i, line := range lines {
		if strings.Contains(line, integrationStep) {
			stepIdx = i
			break
		}
	}
	require.NotEqualf(t, -1, stepIdx, "%s no longer has a step named %q; update this test to match", path, integrationStep)

	var runLine string
	for _, line := range lines[stepIdx:] {
		trimmed := strings.TrimSpace(line)
		if after, ok := strings.CutPrefix(trimmed, "run:"); ok {
			runLine = strings.TrimSpace(after)
			break
		}
	}
	require.NotEmptyf(t, runLine, "%s: could not find a run: line under the %q step", path, integrationStep)

	fields := strings.Fields(runLine)
	require.Truef(t, len(fields) >= 2 && fields[0] == "go" && fields[1] == "test",
		"%s: expected the %q step to run a `go test` command, got %q", path, integrationStep, runLine)

	var pkgs []string
	for _, f := range fields[2:] {
		if strings.HasPrefix(f, "-") {
			continue
		}
		pkgs = append(pkgs, f)
	}
	require.NotEmptyf(t, pkgs, "%s: found no package arguments in the %q step's run: line %q", path, integrationStep, runLine)
	return pkgs
}

// anyListedCovers reports whether pkg is covered by any of the package
// specs go test accepts on a command line: "." (exactly the module root
// package), "./dir/..." (dir and everything under it), or a literal
// "./dir" (exactly that package).
func anyListedCovers(listed []string, pkg string) bool {
	for _, spec := range listed {
		if spec == "." {
			if pkg == modulePath {
				return true
			}
			continue
		}
		trimmed := strings.TrimSuffix(strings.TrimSuffix(spec, "/..."), "/")
		trimmed = strings.TrimPrefix(trimmed, "./")
		full := modulePath
		if trimmed != "" {
			full = modulePath + "/" + trimmed
		}
		if strings.HasSuffix(spec, "/...") {
			if pkg == full || strings.HasPrefix(pkg, full+"/") {
				return true
			}
			continue
		}
		if pkg == full {
			return true
		}
	}
	return false
}
