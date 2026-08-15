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
// by a different route. Reading that list correctly is a third: goTestArgs
// owns which arguments the command may carry, and refuses to guess at one
// it cannot classify, since a package list read out of a command this test
// misunderstands reports a coverage nothing actually provides.
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

// goSkipsDirName reports whether the go tool passes over a directory of this
// name when it expands the package patterns CI writes -- `./...` in the
// "check" job, `./dir/...` and `.` in the "integration" job. `go help
// packages` states the rule: directory names beginning with "." or "_" are
// ignored, as are directories named "testdata", and a ./... pattern never
// matches a package inside a vendor directory.
//
// This is the one place that rule is written down, and both halves of this
// guard read it: guardedPackages does not walk into such a directory, and
// anyListedCovers refuses to call an import path under one covered. Keeping
// the two halves on a single definition is what stops them disagreeing, since
// a disagreement here is exactly the false coverage claim this guard exists
// to prevent -- a test file parked in ".hidden" or "_scratch" runs under no
// pattern either CI job uses, so calling it covered would report a coverage
// nothing provides.
func goSkipsDirName(name string) bool {
	return strings.HasPrefix(name, ".") ||
		strings.HasPrefix(name, "_") ||
		name == "testdata" ||
		name == "vendor"
}

// goSkipsImportPath reports whether pkg names a package the go tool passes
// over for the patterns CI writes, because some element of its path below the
// module root is a directory goSkipsDirName names.
func goSkipsImportPath(pkg string) bool {
	rel, nested := strings.CutPrefix(pkg, modulePath+"/")
	if !nested {
		return false
	}
	for _, elem := range strings.Split(rel, "/") {
		if goSkipsDirName(elem) {
			return true
		}
	}
	return false
}

// guardedPackages returns the sorted, deduplicated import paths of every
// package under the root module whose directory contains at least one
// *_test.go file importing internal/dbtest. It walks the filesystem and
// parses each test file's import block directly, rather than using
// go/build package discovery, so a file's build tags (every guarded test
// file is unix-only) never hide it from this check on a non-unix host.
//
// Parsing directly is why the walk has to reproduce the go tool's own
// directory rules itself (goSkipsDirName): a directory go passes over holds
// no package either CI job can name, and a scratch copy of this repository
// under ".worktrees" would otherwise be discovered as a second set of guarded
// packages that no workflow command ever lists.
func guardedPackages(t *testing.T, root string) []string {
	t.Helper()

	seen := map[string]bool{}
	fset := token.NewFileSet()

	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// The root is the module itself, whose own directory name is not
			// part of any package pattern and so is never skipped.
			if path != root {
				if goSkipsDirName(d.Name()) {
					return filepath.SkipDir
				}
				// A nested go.mod (e.g. sample/taskboard) marks the start of a
				// separate module; its packages are not part of this module's
				// import path space and are excluded from the workflow's
				// `go test` invocation entirely, so they are out of scope here.
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
// package operands (see goTestArgs) so that a missing -v is an error rather
// than a silently dropped token. Both matter. A step name is not unique in a workflow --
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

	flags, pkgs, err := goTestArgs(fields[2:])
	if err != nil {
		return nil, err
	}
	if !slices.Contains(flags, verboseFlag) {
		return nil, fmt.Errorf("the %q step's command %q does not pass %s; without it a live test that skipped and one that ran leave the same package-level `ok` line, so the job's log cannot show which happened (see CONTRIBUTING.md's \"Reading the integration job's log\")", integrationStep, run, verboseFlag)
	}
	if len(pkgs) == 0 {
		return nil, fmt.Errorf("found no package arguments in the %q step's command %q", integrationStep, run)
	}
	return pkgs, nil
}

// goTestBoolFlags names the `go test` flags that take no argument of their
// own, so the token after one is a package operand or another flag. A flag
// missing from this table is not accepted in its bare form (see goTestArgs),
// which is the safe direction to be wrong in: a value-taking flag mistaken
// for a boolean one donates its value to the package list, and this guard
// then reports a coverage it cannot actually see.
var goTestBoolFlags = map[string]bool{
	"-a":          true,
	"-asan":       true,
	"-benchmem":   true,
	"-c":          true,
	"-cover":      true,
	"-failfast":   true,
	"-fullpath":   true,
	"-json":       true,
	"-modcacherw": true,
	"-msan":       true,
	"-n":          true,
	"-race":       true,
	"-short":      true,
	"-trimpath":   true,
	"-v":          true,
	"-work":       true,
	"-x":          true,
}

// goTestValueFlags names the `go test` flags that take one argument, which
// go accepts either joined to the flag (-count=1) or as the following token
// (-count 1). In the second form that token belongs to the flag and is not a
// package, however much it looks like one: `go test -run ./inspect/... .`
// runs the root package alone, with ./inspect/... as the -run regexp.
//
// The table lists the flags a `go test` command in this workflow plausibly
// uses rather than every flag go accepts, since goTestArgs rejects a bare
// flag it does not know instead of guessing its arity.
var goTestValueFlags = map[string]bool{
	"-bench":        true,
	"-benchtime":    true,
	"-coverpkg":     true,
	"-covermode":    true,
	"-coverprofile": true,
	"-cpu":          true,
	"-cpuprofile":   true,
	"-exec":         true,
	"-count":        true,
	"-fuzz":         true,
	"-fuzztime":     true,
	"-gcflags":      true,
	"-ldflags":      true,
	"-memprofile":   true,
	"-mod":          true,
	"-o":            true,
	"-outputdir":    true,
	"-p":            true,
	"-parallel":     true,
	"-shuffle":      true,
	"-tags":         true,
	"-timeout":      true,
	"-trace":        true,
}

// goTestSelectionFlags names the flags that choose which of a package's
// tests actually run. The integration job exists to run every live test in
// the packages it names, so a filter there recreates the gap this guard
// closes from the other end: the package list stays complete while the
// guarded tests inside it never execute. Both forms are rejected, since
// -run=X silences the same tests -X does.
//
// The names here are the canonical ones goTestFlagName produces, so the
// -test.run and -test.skip spellings of the same flags are rejected too.
var goTestSelectionFlags = map[string]bool{
	"-run":  true,
	"-skip": true,
}

// goTestNonRunningFlags names the flags that make `go test` report on a
// package's tests instead of running them. -list prints the names of the
// tests matching its regexp and executes none of them, so a command carrying
// it exits 0 with every listed package reporting ok while not one live test
// touched a database -- the same silent gap a -run filter opens, reached
// without naming a single test.
var goTestNonRunningFlags = map[string]bool{
	"-list": true,
}

// goTestFlagName reduces a flag token's name, as written, to the single
// canonical spelling the tables above are keyed by: leading dashes collapse
// to one, since go accepts -flag and --flag alike, and a leading "test." is
// dropped, since the test binary's own flags are reachable through `go test`
// under both spellings and -test.run means exactly what -run means.
//
// Normalizing that prefix is what keeps a rejected flag rejected in every
// spelling. Written as -test.run, a filter would otherwise reach goTestArgs
// as a name no table holds, carrying a joined value; the command would be
// accepted with -v and every package still in place, while `go test -v
// -test.run=^$` runs no test at all and exits 0.
func goTestFlagName(name string) string {
	return "-" + strings.TrimPrefix(strings.TrimLeft(name, "-"), "test.")
}

// goTestArgs splits the arguments following `go test` into the flags and the
// package operands of the integrationStep's command, consuming each flag's
// argument according to that flag's arity so that a value written apart from
// its flag is never collected as a package. It returns flag tokens exactly as
// written, so a joined value stays attached to its flag and the caller's -v
// check keeps meaning the command passes plain -v.
//
// Anything this parser cannot classify is an error rather than a guess:
// a bare flag of unknown arity, a value-taking flag with no value left to
// take, -args (after which go stops reading packages at all), and an operand
// written in neither ./dir/... nor . form.
//
// The flags that would leave the job green without running the tests it
// lists packages for -- a selection filter, a listing flag -- are refused
// before arity is consulted at all, so neither the joined form nor the
// separate one can slip past on the shape of its value.
func goTestArgs(args []string) (flags, pkgs []string, err error) {
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if !strings.HasPrefix(arg, "-") {
			// Every package spec in this step is written in ./dir/... or "."
			// form. Anything else is a package named some other way, and
			// treating it as one of these would make this guard report a
			// coverage it cannot actually see.
			if !strings.HasPrefix(arg, ".") {
				return nil, nil, fmt.Errorf("the %q step's command has the argument %q, which is neither a flag nor a ./... or . package spec; this test only understands those package forms", integrationStep, arg)
			}
			pkgs = append(pkgs, arg)
			continue
		}

		name, _, joined := strings.Cut(arg, "=")
		flag := goTestFlagName(name)
		switch {
		case flag == "-args":
			return nil, nil, fmt.Errorf("the %q step's command passes %s, after which go hands every remaining argument to the test binary rather than reading it as a package; keep this step a plain `go test` command or update this test", integrationStep, arg)
		case goTestSelectionFlags[flag]:
			return nil, nil, fmt.Errorf("the %q step's command passes %s, which selects a subset of each listed package's tests; the job exists to run every live test in the packages it names, so a package would stay listed here while its guarded tests never ran", integrationStep, arg)
		case goTestNonRunningFlags[flag]:
			return nil, nil, fmt.Errorf("the %q step's command passes %s, which makes go test report on the listed packages' tests without running any of them; the job would end green, with -v and every package still in place, having touched no database at all", integrationStep, arg)
		case joined, goTestBoolFlags[flag]:
			flags = append(flags, arg)
		case goTestValueFlags[flag]:
			if i+1 == len(args) {
				return nil, nil, fmt.Errorf("the %q step's command ends with %s, which takes a value", integrationStep, arg)
			}
			// The next token is this flag's value, not a package, even when
			// it is shaped like one.
			i++
			flags = append(flags, arg)
		default:
			return nil, nil, fmt.Errorf("the %q step's command passes %s, and this test does not know whether that flag takes a separate value; write it as %s=value so its value cannot be read as a package, or add it to goTestBoolFlags or goTestValueFlags", integrationStep, arg, flag)
		}
	}
	return flags, pkgs, nil
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
				fixtureStep(integrationStep, "go test -v ./alpha/... TestSomething"),
			),
			wantErr: `argument "TestSomething"`,
		},
		{
			name: "does not read a flag's separate value as a package",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -count=1 -v -coverpkg ./alpha/... -timeout 20m ./beta/... ."),
			),
			want: []string{"./beta/...", "."},
		},
		{
			name: "rejects a test filter whose value looks like a package list",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -count=1 -v -run ./alpha/... ./beta/... ."),
			),
			wantErr: `passes -run`,
		},
		{
			name: "rejects a test filter written in joined form",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -v -skip=TestSomething ./alpha/..."),
			),
			wantErr: `passes -skip=TestSomething`,
		},
		{
			// `go test -v -test.run=^$ ./...` prints "no tests to run" for
			// every package and exits 0, so this spelling has to be refused
			// as firmly as the -run one; before goTestFlagName normalized the
			// prefix away, the joined value made this command acceptable.
			name: "rejects the test-binary spelling of a filter in joined form",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -count=1 -v -test.run=^$ ./alpha/... ."),
			),
			wantErr: `passes -test.run=^$`,
		},
		{
			name: "rejects the test-binary spelling of a filter in separate form",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -count=1 -v -test.skip .* ./alpha/... ."),
			),
			wantErr: `passes -test.skip`,
		},
		{
			// -list prints test names and runs nothing, in either form.
			name: "rejects a listing command in joined form",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -count=1 -v -list=.* ./alpha/... ."),
			),
			wantErr: `passes -list=.*`,
		},
		{
			name: "rejects a listing command in separate form",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -count=1 -v -list .* ./alpha/... ."),
			),
			wantErr: `passes -list`,
		},
		{
			name: "rejects the test-binary spelling of a listing command",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -count=1 -v -test.list=.* ./alpha/... ."),
			),
			wantErr: `passes -test.list=.*`,
		},
		{
			// Normalizing the prefix must not change what a value-taking flag
			// does with the token after it.
			name: "does not read a test-binary flag's separate value as a package",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -v -test.timeout 20m ./alpha/... ."),
			),
			want: []string{"./alpha/...", "."},
		},
		{
			name: "rejects a bare flag of unknown arity",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -v -notaflagthisknows ./alpha/..."),
			),
			wantErr: "does not know whether that flag takes a separate value",
		},
		{
			name: "rejects a value-taking flag with no value",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -v ./alpha/... -timeout"),
			),
			wantErr: "ends with -timeout",
		},
		{
			name: "rejects packages listed after -args",
			workflow: fixtureWorkflow(
				fixtureStep("Unit tests", "go test ./..."),
				fixtureStep(integrationStep, "go test -v ./alpha/... -args ./beta/..."),
			),
			wantErr: "passes -args",
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

// TestGuardedPackageDiscoveryFollowsGoPackagePatternRules pins the rule
// goSkipsDirName owns at both the places that read it. The checked-in module
// has no directory the go tool passes over, so neither half can be shown
// against the real tree, and the half that matters most is invisible without
// a fixture: a test file under a directory named ".hidden" or "_scratch"
// executes under no pattern CI writes -- not the integration job's
// ./inspect/..., not the check job's ./... -- so discovery must not report
// its directory as a package of this module, and coverage must never answer
// that some listed spec covers it.
//
// Every directory name below is invented for this fixture. This module has
// none of them, so no name here can be read as a report about a real one.
func TestGuardedPackageDiscoveryFollowsGoPackagePatternRules(t *testing.T) {
	t.Run("discovery skips the directories go passes over", func(t *testing.T) {
		root := t.TempDir()
		for _, dir := range []string{
			".",
			"alpha",
			".hidden",
			"_scratch",
			"beta/testdata",
			"gamma/_experimental",
			"vendor/example.com/dep",
			"delta",
		} {
			path := filepath.Join(root, filepath.FromSlash(dir))
			require.NoError(t, os.MkdirAll(path, 0o750), "mkdir %s", dir)
			source := fmt.Sprintf("package p\n\nimport _ %q\n", dbtestImportPath)
			require.NoError(t, os.WriteFile(filepath.Join(path, "live_test.go"), []byte(source), 0o600), "write %s", dir)
		}
		// A nested module is out of this module's import path space, and the
		// workflow's `go test` never reaches it either.
		require.NoError(t, os.WriteFile(filepath.Join(root, "delta", "go.mod"), []byte("module example.com/delta\n"), 0o600))

		require.Equal(t, []string{modulePath, modulePath + "/alpha"}, guardedPackages(t, root))
	})

	t.Run("coverage refuses the import paths go passes over", func(t *testing.T) {
		// The specs on the left are the shape ci.yml uses; each package on
		// the right sits under the directory the spec names.
		listed := []string{"./alpha/...", "."}
		for _, tc := range []struct {
			pkg  string
			want bool
		}{
			{pkg: modulePath, want: true},
			{pkg: modulePath + "/alpha", want: true},
			{pkg: modulePath + "/alpha/beta", want: true},
			{pkg: modulePath + "/alpha/.hidden", want: false},
			{pkg: modulePath + "/alpha/_scratch", want: false},
			{pkg: modulePath + "/alpha/testdata", want: false},
			{pkg: modulePath + "/alpha/vendor/example.com/dep", want: false},
			{pkg: modulePath + "/alpha/_experimental/deeper", want: false},
			{pkg: modulePath + "/gamma", want: false},
		} {
			t.Run(tc.pkg, func(t *testing.T) {
				require.Equal(t, tc.want, anyListedCovers(listed, tc.pkg))
			})
		}
	})
}

// anyListedCovers reports whether pkg is covered by any of the package
// specs go test accepts on a command line: "." (exactly the module root
// package), "./dir/..." (dir and everything under it), or a literal
// "./dir" (exactly that package).
//
// "everything under it" is the go tool's own reading, not a plain string
// prefix: a ./dir/... pattern skips the directories goSkipsDirName names, so
// an import path below one of them is covered by nothing, whatever spec is
// listed.
func anyListedCovers(listed []string, pkg string) bool {
	if goSkipsImportPath(pkg) {
		return false
	}
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
