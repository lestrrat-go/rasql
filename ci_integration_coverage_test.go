package rasql_test

import (
	"fmt"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
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
	integrationJob   = "integration"
	integrationStep  = "Run the suite against live databases"
	verboseFlag      = "-v"
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
//
// The package list is only half of the invariant, so integrationRunPackages
// also fails the test when that command stops passing -v: a listed package
// whose live test skipped and one whose live test ran leave the same
// package-level "ok" line without it, which is the same silent gap arriving
// by a different route.
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
// YAML to find that one step, deliberately not hardcoding a second copy
// of the package list: the workflow file drifting out of step with
// itself is not a failure mode this test needs to guard against. Anything
// that stops that command from proving what the job exists to prove --
// a dropped -v included -- fails the test here rather than returning a
// package list, since a list read out of the wrong command is worse than
// no list at all.
func integrationJobPackages(t *testing.T, path string) []string {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err, "read %s", path)

	pkgs, err := integrationRunPackages(string(data))
	require.NoErrorf(t, err, "%s: could not read the %q job's %q step", path, integrationJob, integrationStep)
	return pkgs
}

// integrationRunPackages returns the package specs the integrationStep of
// the integrationJob passes to `go test` in the given ci.yml document.
//
// It locates the job and the step exactly instead of searching the whole
// file for the step's text, and it splits the command's flags from its
// package operands so that a missing -v is an error rather than a silently
// dropped token. Both matter. A step name is not unique in a workflow --
// GitHub Actions accepts a second step, in any job, carrying the same
// name -- so a first-match text search can end up validating a run: line
// the integration job never executes while the real one quietly loses a
// package. And a run: search that keeps walking past the end of its step
// picks up a neighbouring step's command for the same reason.
func integrationRunPackages(workflow string) ([]string, error) {
	lines := strings.Split(workflow, "\n")

	jobs := blockUnder(lines, "jobs:")
	if len(jobs) == 0 {
		return nil, fmt.Errorf("the workflow has no top-level jobs: key, so its %q job cannot be located", integrationJob)
	}
	job := blockUnder(jobs, integrationJob+":")
	if len(job) == 0 {
		return nil, fmt.Errorf("no %q job under jobs:", integrationJob)
	}
	steps := blockUnder(job, "steps:")
	if len(steps) == 0 {
		return nil, fmt.Errorf("the %q job has no steps: key", integrationJob)
	}

	var matched []workflowStep
	for _, step := range splitSteps(steps) {
		if step.name == integrationStep {
			matched = append(matched, step)
		}
	}
	if len(matched) != 1 {
		return nil, fmt.Errorf("the %q job has %d steps named %q, want exactly 1; update this test to match the workflow", integrationJob, len(matched), integrationStep)
	}

	run := matched[0].run
	if run == "" {
		return nil, fmt.Errorf("the %q step has no run: key of its own", integrationStep)
	}
	// A block scalar ("run: |") carries its command on the following
	// lines, which this parser deliberately does not read: the step it
	// guards is one command long, and accepting a multi-line body would
	// mean deciding which of its lines is the `go test` invocation.
	if strings.HasPrefix(run, "|") || strings.HasPrefix(run, ">") {
		return nil, fmt.Errorf("the %q step's run: is a multi-line block (%q), which this test cannot parse; keep it a single `go test` command or update this test", integrationStep, run)
	}

	fields := strings.Fields(run)
	if len(fields) < 2 || fields[0] != "go" || fields[1] != "test" {
		return nil, fmt.Errorf("expected the %q step to run a `go test` command, got %q", integrationStep, run)
	}

	var flags, pkgs []string
	for _, f := range fields[2:] {
		if strings.HasPrefix(f, "-") {
			flags = append(flags, f)
			continue
		}
		// Every package spec in this step is written in ./dir/... or "."
		// form. Anything else is either a package named some other way or
		// the separate value of a flag such as `-run Name`, and treating
		// either as a package would make this guard report a coverage it
		// cannot actually see.
		if !strings.HasPrefix(f, ".") {
			return nil, fmt.Errorf("the %q step's command has the argument %q, which is neither a flag nor a ./... or . package spec; this test only understands those package forms", integrationStep, f)
		}
		pkgs = append(pkgs, f)
	}
	if !slices.Contains(flags, verboseFlag) {
		return nil, fmt.Errorf("the %q step's command %q does not pass %s; without it a live test that skipped and one that ran leave the same package-level `ok` line, so the job's log cannot show which happened (see CONTRIBUTING.md's \"Reading the integration job's log\")", integrationStep, run, verboseFlag)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("found no package arguments in the %q step's command %q", integrationStep, run)
	}
	return pkgs, nil
}

// workflowStep is one step of a workflow job: the value of its name: key
// and the value of its run: key, each empty when the step has no such key.
type workflowStep struct {
	name string
	run  string
}

// leadingSpaces counts the run of literal space characters at the start of
// line. GitHub Actions workflows indent with spaces only -- YAML forbids
// tabs for indentation -- so this is the same measure a YAML parser would
// use for nesting depth without needing one as a dependency.
func leadingSpaces(line string) int {
	return len(line) - len(strings.TrimLeft(line, " "))
}

// shallowestIndent returns the smallest indentation of any non-blank line
// in block, which for a YAML mapping block is the column its direct
// children share.
func shallowestIndent(block []string) int {
	indent := -1
	for _, line := range block {
		if strings.TrimSpace(line) == "" {
			continue
		}
		if depth := leadingSpaces(line); indent == -1 || depth < indent {
			indent = depth
		}
	}
	return indent
}

// blockUnder returns the lines nested under key, which must be a direct
// child of block -- that is, a line at block's own shallowest indentation
// whose trimmed text is key. Nested means every following line up to, but
// not including, the first non-blank line indented no deeper than that key.
// A block sequence written in the same column as the key that owns it
// ("steps:" followed by "- name:" at the same indent) counts as nested,
// since YAML allows that form too.
//
// Requiring the key to be a direct child is what keeps a lookup from
// reaching into a grandchild that happens to share the name, and a key that
// is not found returns no lines rather than the whole block, so a rename
// fails the caller loudly instead of widening its search.
func blockUnder(block []string, key string) []string {
	indent := shallowestIndent(block)

	start := -1
	for i, line := range block {
		if leadingSpaces(line) == indent && strings.TrimSpace(line) == key {
			start = i + 1
			break
		}
	}
	if start == -1 {
		return nil
	}

	var nested []string
	for _, line := range block[start:] {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			nested = append(nested, line)
			continue
		}
		depth := leadingSpaces(line)
		if depth > indent || (depth == indent && strings.HasPrefix(trimmed, "- ")) {
			nested = append(nested, line)
			continue
		}
		break
	}
	return nested
}

// splitSteps parses the step items of a steps: block. Items are the lines
// starting with "- " at the shallowest such indentation in the block, and
// each item ends where the next one begins, so no step's keys can be read
// out of its neighbour.
func splitSteps(block []string) []workflowStep {
	itemIndent := -1
	var starts []int
	for i, line := range block {
		if !strings.HasPrefix(strings.TrimSpace(line), "- ") {
			continue
		}
		indent := leadingSpaces(line)
		if itemIndent == -1 {
			itemIndent = indent
		}
		if indent == itemIndent {
			starts = append(starts, i)
		}
	}

	steps := make([]workflowStep, 0, len(starts))
	for n, start := range starts {
		end := len(block)
		if n+1 < len(starts) {
			end = starts[n+1]
		}
		steps = append(steps, parseStep(block[start:end], itemIndent))
	}
	return steps
}

// parseStep reads one step item's own name: and run: values. Only keys at
// the step's own key indentation are read -- that is, the first line's text
// after its "- " marker, and later lines exactly two columns deeper -- so a
// run: nested further inside the step, in a with: mapping for instance, is
// never mistaken for the step's own command.
func parseStep(item []string, itemIndent int) workflowStep {
	const marker = "- "
	keyIndent := itemIndent + len(marker)

	var step workflowStep
	for i, line := range item {
		key := strings.TrimSpace(line)
		if i == 0 {
			key = strings.TrimPrefix(key, marker)
		} else if leadingSpaces(line) != keyIndent {
			continue
		}
		if after, ok := strings.CutPrefix(key, "name:"); ok && step.name == "" {
			step.name = unquoteScalar(strings.TrimSpace(after))
			continue
		}
		if after, ok := strings.CutPrefix(key, "run:"); ok && step.run == "" {
			step.run = strings.TrimSpace(after)
		}
	}
	return step
}

// unquoteScalar strips one matching pair of surrounding quotes from a YAML
// scalar, so a step name written in quotes matches the same constant an
// unquoted one does.
func unquoteScalar(value string) string {
	for _, quote := range []string{`"`, `'`} {
		if len(value) >= 2*len(quote) && strings.HasPrefix(value, quote) && strings.HasSuffix(value, quote) {
			return value[len(quote) : len(value)-len(quote)]
		}
	}
	return value
}

// fixtureWorkflow renders a two-job workflow around the given steps: blocks
// so integrationRunPackages can be tested against workflow shapes that are
// legal for GitHub Actions but wrong for this repository. The job and step
// names come from the same constants the parser reads, and every package
// spec in these fixtures names a directory this module does not have, so no
// fixture line can be mistaken for a real command from ci.yml.
func fixtureWorkflow(checkSteps, integrationSteps string) string {
	return fmt.Sprintf(`name: CI

jobs:
  check:
    runs-on: ubuntu-latest
    steps:
%s  %s:
    runs-on: ubuntu-latest
    steps:
%s`, checkSteps, integrationJob, integrationSteps)
}

// fixtureStep renders one step with a name: and a run: for fixtureWorkflow.
func fixtureStep(name, run string) string {
	return fmt.Sprintf("      - name: %s\n        run: %s\n", name, run)
}

// TestIntegrationRunPackagesReadsTheIntegrationStepsOwnCommand pins the two
// properties TestIntegrationJobListsEveryDBTestGuardedPackage depends on and
// cannot show by itself, since it only ever sees the one workflow that is
// checked in: the command it validates is the one the integration job's own
// step runs, and that command passes -v.
func TestIntegrationRunPackagesReadsTheIntegrationStepsOwnCommand(t *testing.T) {
	for _, tc := range []struct {
		name     string
		workflow string
		want     []string
		wantErr  string
	}{
		{
			name: "reads the step's package list",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -count=1 -v ./alpha/... ."),
			),
			want: []string{"./alpha/...", "."},
		},
		{
			name: "rejects a command that dropped -v",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -count=1 ./alpha/... ."),
			),
			wantErr: "does not pass -v",
		},
		{
			name: "reads past a same-named step in another job",
			workflow: fixtureWorkflow(
				fixtureStep(integrationStep, "go test -v ./alpha/... ./beta/... ."),
				fixtureStep(integrationStep, "go test -v ./alpha/..."),
			),
			want: []string{"./alpha/..."},
		},
		{
			name: "rejects the same step name twice in the integration job",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -v ./alpha/...")+
					fixtureStep(integrationStep, "go test -v ./beta/..."),
			),
			wantErr: "has 2 steps named",
		},
		{
			name: "does not borrow a later step's command",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				"      - name: "+integrationStep+"\n        uses: actions/setup-go@v6\n"+
					fixtureStep("Live tests", "go test -v ./alpha/..."),
			),
			wantErr: "has no run: key of its own",
		},
		{
			name: "does not read a run: nested deeper in the step",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				"      - name: "+integrationStep+"\n        uses: example/action@v1\n        with:\n          run: go test -v ./alpha/...\n",
			),
			wantErr: "has no run: key of its own",
		},
		{
			name: "rejects an argument that is neither a flag nor a package",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -v -run TestSomething ./alpha/..."),
			),
			wantErr: `argument "TestSomething"`,
		},
		{
			name: "rejects a workflow without the integration job",
			workflow: fmt.Sprintf(`name: CI

jobs:
  check:
    runs-on: ubuntu-latest
    steps:
%s`, fixtureStep(integrationStep, "go test -v ./alpha/...")),
			wantErr: fmt.Sprintf("no %q job", integrationJob),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := integrationRunPackages(tc.workflow)
			if tc.wantErr != "" {
				require.ErrorContains(t, err, tc.wantErr, "integrationRunPackages(%s)", tc.workflow)
				require.Nil(t, got, "integrationRunPackages returned packages alongside an error")
				return
			}
			require.NoError(t, err, "integrationRunPackages(%s)", tc.workflow)
			require.Equal(t, tc.want, got)
		})
	}
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
