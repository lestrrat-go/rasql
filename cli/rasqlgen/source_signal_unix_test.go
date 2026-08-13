//go:build unix

package rasqlgen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// This test sends a real signal and reads the wait status it produces, both
// of which are Unix-only.

// blockingTablesSource is a schema package whose Tables announces that the
// child is running and then blocks. The announcement is what makes the test
// deterministic: the signal is sent only once the child is known to be
// running, so the parent is certainly inside its own `go run` and cannot
// have finished the work before the signal arrives.
const blockingTablesSource = `package tables

import (
	"os"
	"time"

	"github.com/lestrrat-go/rasql/schema"
)

func Tables() []schema.TableDef {
	if err := os.WriteFile("child-running", nil, 0o600); err != nil {
		panic(err)
	}
	time.Sleep(30 * time.Second)
	return []schema.TableDef{
		schema.MustTableDef("users",
			schema.Integer("id"),
			schema.PrimaryKey("id"),
		),
	}
}
`

// TestSchemaSourceInterruptRemovesTemporaryDirectory interrupts a running
// `rasqlgen schema -source` and asserts both halves of what a handled
// signal has to deliver: the temporary directory rasqlgen wrote into the
// module is gone, and the process still ends on its signal, so a caller
// that tells an interrupted run from a failed one still can.
//
// It runs a built binary rather than `go run ./cmd/rasqlgen`, because
// `go run` reports a signalled child as its own exit status 1 and the wait
// status this asserts would never be visible.
func TestSchemaSourceInterruptRemovesTemporaryDirectory(t *testing.T) {
	moduleDir := newSchemaSourceFixture(t, blockingTablesSource)
	binary := buildRasqlgenBinary(t)

	logPath := filepath.Join(t.TempDir(), "rasqlgen.log")
	log, err := os.Create(logPath)
	require.NoError(t, err)
	t.Cleanup(func() { _ = log.Close() })

	command := exec.Command(binary, "schema", "-source", "./internal/tables", "-package", "store", "-output", "internal/store")
	command.Dir = moduleDir
	// An *os.File rather than a buffer: exec hands a file to the child as
	// a descriptor, while any other writer makes it create a pipe and
	// wait for every holder of the write end to close it, which the
	// process left behind by the interrupted child would keep open.
	command.Stdout = log
	command.Stderr = log
	require.NoError(t, command.Start())
	t.Cleanup(func() { _ = command.Process.Kill() })

	requireFileEventually(t, filepath.Join(moduleDir, "child-running"), logPath)
	require.NoError(t, command.Process.Signal(os.Interrupt))
	_ = command.Wait()

	status, ok := command.ProcessState.Sys().(syscall.WaitStatus)
	require.True(t, ok)
	require.True(t, status.Signaled(),
		"expected the run to end on its signal, got %s; output: %s", command.ProcessState, readLog(t, logPath))
	require.Equal(t, syscall.SIGINT, status.Signal())

	requireNoLeftoverSourceTempDir(t, moduleDir)
}

// buildRasqlgenBinary builds the command under test once for this test.
func buildRasqlgenBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "rasqlgen")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "./cmd/rasqlgen")
	build.Dir = filepath.Join("..", "..")
	output, err := build.CombinedOutput()
	require.NoError(t, err, string(output))
	return binary
}

// requireFileEventually waits for path to appear. The wait is generous
// because the run it watches includes a `go run` compile, which on a cold
// build cache is far slower than the work itself.
func requireFileEventually(t *testing.T, path, logPath string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Minute)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("%s never appeared, so the child never ran; output: %s", path, readLog(t, logPath))
}

func readLog(t *testing.T, path string) string {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		return "<unreadable: " + err.Error() + ">"
	}
	return string(contents)
}
