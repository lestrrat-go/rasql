package rasql_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

const workflowPath = ".github/workflows/ci.yml"

// TestWorkflowRunsTheCheckedInScripts pins the one thing .github/workflows/ci.yml
// still decides on its own: which checked-in script each job runs. The commands
// those scripts run, and the package list the live one covers, live in
// scripts/test.sh and scripts/conformance.sh, so a step that stopped calling a
// script is the only edit to this file that can leave a job green while it runs
// nothing the job exists to run.
//
// This test reads the file as lines and does not work out which job a step sits
// in. That mattered while the package list was read out of a step and had to
// come from the right one; it decides nothing now, since no text in this file
// names a package or a flag.
func TestWorkflowRunsTheCheckedInScripts(t *testing.T) {
	data, err := os.ReadFile(workflowPath)
	require.NoError(t, err, "read %s", workflowPath)

	for _, command := range []string{
		"./scripts/test.sh check",
		"./scripts/test.sh live",
		"./scripts/conformance.sh live",
	} {
		t.Run(command, func(t *testing.T) {
			require.Equalf(t, 1, countRunLines(string(data), command),
				"%s must hold exactly one step whose run: is %q", workflowPath, command)
		})
	}
}

// countRunLines counts the steps of workflow whose run: key is exactly command.
// A commented-out line is not a step, so lines that start with # are passed
// over.
func countRunLines(workflow, command string) int {
	want := "run: " + command
	count := 0
	for _, line := range strings.Split(workflow, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if trimmed == want {
			count++
		}
	}
	return count
}

// TestLiveTestScriptFailsWhenNoPackageIsGuarded pins the one way scripts/test.sh
// can report success having tested nothing. Its live mode derives its package
// list from the tree rather than carrying one, so a discovery that comes back
// empty -- a renamed internal/dbtest, a go list this script reads wrongly --
// would otherwise leave `go test` with no operand, which tests the current
// directory and exits 0.
//
// The fake go on PATH prints no package, which is what an empty discovery looks
// like to the script.
func TestLiveTestScriptFailsWhenNoPackageIsGuarded(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("scripts/test.sh is a shell script")
	}

	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "go"), []byte("#!/bin/sh\nexit 0\n"), 0o755))

	cmd := exec.Command("./scripts/test.sh", "live")
	cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	out, err := cmd.CombinedOutput()
	require.Errorf(t, err, "scripts/test.sh live exited 0 having found no package to test:\n%s", out)
	require.Contains(t, string(out), "would run no live test at all")
}
