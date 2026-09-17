package examples_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// bundlePath is the walkthrough's own repository, one commit per step, stored
// as a single file. Cloning it gives the Taskboard project back at any step the
// walkthrough describes.
const bundlePath = "../sample/taskboard/walkthrough/steps.bundle"

// samplePath is the checked-in copy of what that repository's last commit
// holds.
const samplePath = "../sample/taskboard"

// bundleDivergences are the paths the checked-in copy is allowed to spell
// differently from the bundle's last commit. CONTRIBUTING.md's "Rebuilding the
// walkthrough's application" section owns why each one differs.
var bundleDivergences = map[string]struct{}{
	"README.md":                            {},
	"go.mod":                               {},
	"internal/store/docs_examples_test.go": {},
	"scripts/generate.sh":                  {},
	"scripts/migrate.sh":                   {},
	"scripts/rasql.sh":                     {},
}

// generatedStoreDir is where rasql codegen generate writes the sample's store
// package.
const generatedStoreDir = "internal/store/"

// isGeneratedStoreOutput reports whether relative names a file directly
// inside generatedStoreDir that codegen writes rather than a reader types: a
// _gen.go source file, or the rasql.sum fingerprint file codegen writes
// beside them. A reader following the walkthrough runs one command and gets
// whatever the generator writes that day, so those bytes record the
// generator's version rather than anything the reader did, and comparing
// them here would turn every change to the generator red. The files are
// still checked, against a fresh run of the generator rather than against
// the bundle: .github/workflows/ci.yml's "check" job runs
// `./scripts/rasql.sh codegen check` offline, and its "integration" job runs
// the same check against a live PostgreSQL database.
func isGeneratedStoreOutput(relative string) bool {
	name, direct := strings.CutPrefix(relative, generatedStoreDir)
	if !direct || strings.Contains(name, "/") {
		return false
	}
	return name == "rasql.sum" || strings.HasSuffix(name, "_gen.go")
}

// skipComparison reports whether relative is exempt from the byte-for-byte
// comparison the walk in TestWalkthroughBundleMatchesSample runs: either
// bundleDivergences names it, or isGeneratedStoreOutput does.
func skipComparison(relative string) bool {
	_, diverges := bundleDivergences[relative]
	return diverges || isGeneratedStoreOutput(relative)
}

// TestWalkthroughBundleMatchesSample holds the checked-in application to the
// repository that produced it. Editing sample/taskboard without redoing the
// walkthrough's steps leaves the bundle describing an application that no
// longer exists, and every later chapter's transcript reporting a state its own
// commands never produced.
//
// Region markers are the one difference the comparison ignores everywhere. A
// chapter includes part of a file through // BEGIN(name) / // END(name), and
// those lines are added to the checked-in copy rather than to the steps, since
// a reader following the walkthrough never types them.
func TestWalkthroughBundleMatchesSample(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}

	clone := t.TempDir()
	output, err := exec.Command("git", "clone", "--quiet", bundlePath, clone).CombinedOutput()
	require.NoError(t, err, "clone %s: %s", bundlePath, output)
	goMod, err := os.ReadFile(filepath.Join(clone, "go.mod"))
	require.NoError(t, err)
	require.Contains(t, string(goMod), "replace github.com/lestrrat-go/rasql => ../rasql\n")
	_, err = os.Stat(filepath.Join(clone, "scripts", "rasql.sh"))
	require.ErrorIs(t, err, os.ErrNotExist)
	for _, script := range []string{"generate.sh", "migrate.sh"} {
		source, readErr := os.ReadFile(filepath.Join(clone, "scripts", script))
		require.NoError(t, readErr)
		require.Contains(t, string(source), "rasql ", "%s must invoke the installed rasql command", script)
		require.NotContains(t, string(source), "scripts/rasql.sh", "%s must not use a repository wrapper", script)
		require.NotContains(t, string(source), "go run github.com/lestrrat-go/rasql/cmd/rasql", "%s must not use a module path command", script)
	}

	compared := compareTree(t, clone, samplePath, skipComparison)
	require.NotZero(t, compared, "the bundle holds no files to compare")

	// The walk above only checks that every file the bundle holds also exists
	// in the checked-in copy. A file added to sample/taskboard and never
	// carried into the walkthrough's steps is invisible to it, so this walks
	// the other direction: every tracked file under sample/taskboard, except
	// walkthrough/ itself and the paths bundleDivergences excuses, must exist
	// in the bundle clone. git ls-files drives this rather than a filesystem
	// walk so a stale untracked file some working copies carry (such as
	// internal/store/.taskboard-schema.db) can't fail a check about what's
	// checked in.
	tracked, err := exec.Command("git", "-C", "..", "ls-files", "--", "sample/taskboard").CombinedOutput()
	require.NoError(t, err, "git ls-files sample/taskboard: %s", tracked)

	for _, line := range strings.Split(strings.TrimRight(string(tracked), "\n"), "\n") {
		if line == "" {
			continue
		}
		relative := strings.TrimPrefix(line, "sample/taskboard/")
		if strings.HasPrefix(relative, "walkthrough/") {
			continue
		}
		if skipComparison(relative) {
			continue
		}
		_, err := os.Stat(filepath.Join(clone, filepath.FromSlash(relative)))
		require.NoError(t, err, "%s is in %s and not in the bundle; see CONTRIBUTING.md's \"Rebuilding the walkthrough's application\"", relative, samplePath)
	}
}

// treeT is the subset of *testing.T that compareTree needs. Declaring it as
// an interface lets a test substitute a fake for *testing.T, to observe a
// failing comparison without failing the real test itself.
type treeT interface {
	require.TestingT
	Helper()
}

// compareTree walks every file under clone and requires its contents, once
// stripped of region markers and normalized to one trailing newline, match
// the file at the same relative path under sample. It skips a path skip
// reports true for, comparing neither its content nor its presence under
// sample. It returns how many files it compared.
func compareTree(t treeT, clone, sample string, skip func(relative string) bool) int {
	t.Helper()

	compared := 0
	err := filepath.WalkDir(clone, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(clone, path)
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if relative == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		slashRelative := filepath.ToSlash(relative)
		if skip(slashRelative) {
			return nil
		}

		step, err := os.ReadFile(path)
		require.NoError(t, err)
		checkedIn, err := os.ReadFile(filepath.Join(sample, relative))
		require.NoError(t, err, "%s is in the bundle and not in %s; rebuild one from the other", relative, sample)
		require.Equal(t, normalizeTrailer(string(step)), normalizeTrailer(withoutRegionMarkers(string(checkedIn))),
			"%s differs from the step that produced it; see CONTRIBUTING.md's \"Rebuilding the walkthrough's application\"", relative)
		compared++
		return nil
	})
	require.NoError(t, err)
	return compared
}

// TestCompareTreeSkipsGeneratedStoreOutputButCatchesHandWrittenFiles proves
// what skipComparison buys the walk above: a difference inside a generated
// store file never reaches the byte comparison, while the same kind of
// difference in a hand-written file still fails it. It builds its own clone
// and sample directories rather than reusing the walkthrough's, so it never
// touches sample/taskboard.
func TestCompareTreeSkipsGeneratedStoreOutputButCatchesHandWrittenFiles(t *testing.T) {
	clone := t.TempDir()
	sample := t.TempDir()
	writeTestFile(t, clone, "internal/store/schema_gen.go", "package store\n// clone's generated schema\n")
	writeTestFile(t, sample, "internal/store/schema_gen.go", "package store\n// sample's generated schema, deliberately different\n")
	writeTestFile(t, clone, "internal/store/repository.go", "package store\n// same on both sides\n")
	writeTestFile(t, sample, "internal/store/repository.go", "package store\n// same on both sides\n")

	compared, failed := captureCompareTree(clone, sample, skipComparison)
	require.False(t, failed, "a generated store file that differs between clone and sample must not fail the comparison")
	require.Equal(t, 1, compared, "only repository.go should have reached the byte comparison")

	writeTestFile(t, sample, "internal/store/repository.go", "package store\n// sample's repository, deliberately different\n")
	_, failed = captureCompareTree(clone, sample, skipComparison)
	require.True(t, failed, "a hand-written file that differs between clone and sample must fail the comparison")
}

// captureCompareTree runs compareTree against a fakeT in a goroutine of its
// own, the same way testing.T.Run isolates a subtest, so the FailNow a failed
// require.Equal raises inside compareTree stops only that goroutine. It
// reports how many files compareTree reached the byte comparison for, and
// whether that comparison failed.
func captureCompareTree(clone, sample string, skip func(relative string) bool) (compared int, failed bool) {
	fake := &fakeT{}
	done := make(chan struct{})
	go func() {
		defer close(done)
		compared = compareTree(fake, clone, sample, skip)
	}()
	<-done
	return compared, fake.failed
}

// fakeT is the treeT captureCompareTree substitutes for *testing.T. It
// records a failure instead of stopping the real test, and its FailNow calls
// runtime.Goexit the same way *testing.T.FailNow does, so it unwinds only the
// goroutine compareTree runs in.
type fakeT struct {
	failed bool
}

func (f *fakeT) Helper() {}

func (f *fakeT) Errorf(string, ...interface{}) {
	f.failed = true
}

func (f *fakeT) FailNow() {
	f.failed = true
	runtime.Goexit()
}

// writeTestFile creates relative under root, including any parent
// directories, and writes contents to it.
func writeTestFile(t *testing.T, root, relative, contents string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(relative))
	require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
	require.NoError(t, os.WriteFile(full, []byte(contents), 0o644))
}

// withoutRegionMarkers drops the include-block markers the checked-in copy
// carries, along with the blank line a marker leaves behind when it sits
// between two declarations. A top-level marker is always followed by a blank
// line, which TestDocRegionMarkersStayOutOfDocComments enforces so Go never
// reads the marker as the next declaration's doc comment, so dropping the
// marker line always leaves a blank run for collapseBlankRuns to close up.
func withoutRegionMarkers(contents string) string {
	lines := strings.Split(contents, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "// BEGIN(") || strings.HasPrefix(trimmed, "// END(") {
			continue
		}
		kept = append(kept, line)
	}
	return collapseBlankRuns(strings.Join(kept, "\n"))
}

// collapseBlankRuns rewrites a run of two or more blank lines as one. Removing
// a marker line can leave two blank lines where the step's file has one.
func collapseBlankRuns(contents string) string {
	for strings.Contains(contents, "\n\n\n") {
		contents = strings.ReplaceAll(contents, "\n\n\n", "\n\n")
	}
	return contents
}

// normalizeTrailer gives a file exactly one closing newline. A region that ends
// at the end of a file leaves a blank line behind once its marker is dropped.
func normalizeTrailer(contents string) string {
	return strings.TrimRight(contents, "\n") + "\n"
}
