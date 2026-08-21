package examples_test

import (
	"os"
	"os/exec"
	"path/filepath"
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
	"README.md":           {},
	"scripts/generate.sh": {},
	"scripts/migrate.sh":  {},
	"scripts/rasql.sh":    {},
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

	compared := 0
	err = filepath.WalkDir(clone, func(path string, entry os.DirEntry, err error) error {
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
		if _, diverges := bundleDivergences[filepath.ToSlash(relative)]; diverges {
			return nil
		}

		step, err := os.ReadFile(path)
		require.NoError(t, err)
		checkedIn, err := os.ReadFile(filepath.Join(samplePath, relative))
		require.NoError(t, err, "%s is in the bundle and not in %s; rebuild one from the other", relative, samplePath)
		require.Equal(t, normalizeTrailer(string(step)), normalizeTrailer(withoutRegionMarkers(string(checkedIn))),
			"%s differs from the step that produced it; see CONTRIBUTING.md's \"Rebuilding the walkthrough's application\"", relative)
		compared++
		return nil
	})
	require.NoError(t, err)
	require.NotZero(t, compared, "the bundle holds no files to compare")
}

// withoutRegionMarkers drops the include-block markers the checked-in copy
// carries, along with the blank line a marker leaves behind when it sits
// between two declarations.
func withoutRegionMarkers(contents string) string {
	lines := strings.Split(contents, "\n")
	kept := make([]string, 0, len(lines))
	dropped := false
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "// BEGIN(") || strings.HasPrefix(trimmed, "// END(") {
			dropped = true
			continue
		}
		// A marker placed inside a doc comment is separated from the prose
		// above it by an empty comment line, which belongs to the marker
		// rather than to the declaration being documented.
		if dropped && trimmed == "//" {
			dropped = false
			continue
		}
		dropped = false
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
