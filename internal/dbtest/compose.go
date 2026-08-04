package dbtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// composeFileName is the compose file this package brings up, checked in at
// the repository root. It mirrors the two service definitions CI's
// integration job runs (see .github/workflows/ci.yml): postgres:17-alpine
// on 127.0.0.1:5432 and mysql:8.4 on 127.0.0.1:3306, with the same
// credentials and healthchecks.
const composeFileName = "compose.yaml"

// probeTimeout bounds each Docker availability probe, so an unreachable
// daemon is reported quickly instead of hanging a test run.
const probeTimeout = 8 * time.Second

// bringUpTimeout bounds the compose bring-up itself, which can include an
// image pull on a first run.
const bringUpTimeout = 5 * time.Minute

// composeRunner invokes `docker compose` or the standalone `docker-compose`
// fallback with a fixed set of leading arguments.
type composeRunner struct {
	bin  string
	args []string
}

func (r composeRunner) command(ctx context.Context, extra ...string) *exec.Cmd {
	args := make([]string, 0, len(r.args)+len(extra))
	args = append(args, r.args...)
	args = append(args, extra...)
	return exec.CommandContext(ctx, r.bin, args...)
}

// probeCompose reports whether this machine can run `docker compose` (or
// the standalone `docker-compose`), returning the reason it can't when it
// can't. It never brings anything up itself, so a caller can turn a failure
// into a t.Skip that names exactly which condition was detected:
//
//   - the `docker` binary missing from PATH entirely
//   - the daemon unreachable, including a permission error talking to its
//     socket -- the docker CLI's own error text says which
//   - neither `docker compose` nor a standalone `docker-compose` present
func probeCompose() (composeRunner, error) {
	dockerPath, err := exec.LookPath("docker")
	if err != nil {
		return composeRunner{}, fmt.Errorf("the `docker` binary is not on PATH")
	}

	infoCtx, cancel := context.WithTimeout(context.Background(), probeTimeout)
	defer cancel()
	if out, err := exec.CommandContext(infoCtx, dockerPath, "info").CombinedOutput(); err != nil {
		return composeRunner{}, fmt.Errorf("the Docker daemon is not reachable (ran `docker info`): %s", firstLine(out))
	}

	composeCtx, composeCancel := context.WithTimeout(context.Background(), probeTimeout)
	defer composeCancel()
	if err := exec.CommandContext(composeCtx, dockerPath, "compose", "version").Run(); err == nil {
		return composeRunner{bin: dockerPath, args: []string{"compose"}}, nil
	}

	if legacyPath, err := exec.LookPath("docker-compose"); err == nil {
		return composeRunner{bin: legacyPath}, nil
	}

	return composeRunner{}, fmt.Errorf("neither `docker compose` nor a standalone `docker-compose` binary is available")
}

func firstLine(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "(no output)"
	}
	return strings.SplitN(trimmed, "\n", 2)[0]
}

// ensureComposeUp brings up the checked-in compose file if needed, or skips
// the calling test naming why Docker could not be used.
//
// Skip vs. fail here is deliberate; see the package doc for the reasoning.
// probeCompose failing skips: it is an environment fact, not something the
// test run should report as a rasql defect. A bring-up that fails after
// probeCompose has already confirmed Docker works fails loudly with
// t.Fatalf instead, because that means the compose file or an image
// reference is broken.
func ensureComposeUp(t *testing.T) {
	t.Helper()

	runner, err := probeCompose()
	if err != nil {
		t.Skipf("skipping live database test: %s; set RASQL_TEST_POSTGRES_DSN/RASQL_TEST_MYSQL_DSN to point at a running database, or start one locally with `docker compose up -d --wait` (uses %s)", err, composeFileName)
		return
	}

	root, err := repoRoot()
	if err != nil {
		t.Fatalf("dbtest: locate repository root: %v", err)
	}
	composePath := filepath.Join(root, composeFileName)

	lockPath := filepath.Join(root, ".tmp", "dbtest-compose.lock")
	if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
		t.Fatalf("dbtest: create lock directory: %v", err)
	}
	lock, err := acquireLock(lockPath)
	if err != nil {
		t.Fatalf("dbtest: acquire compose bring-up lock: %v", err)
	}
	defer func() {
		if err := lock.release(); err != nil {
			t.Errorf("dbtest: release compose bring-up lock: %v", err)
		}
	}()

	upCtx, cancel := context.WithTimeout(context.Background(), bringUpTimeout)
	defer cancel()
	cmd := runner.command(upCtx, "-f", composePath, "up", "-d", "--wait")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("dbtest: docker compose up failed even though Docker is available: %v\n%s", err, out)
	}
}

// repoRoot finds the module root from the current test binary's working
// directory (which go test sets to the package directory), so the lock file
// and compose file resolve to the same repository regardless of which
// package is calling in.
func repoRoot() (string, error) {
	out, err := exec.Command("go", "env", "GOMOD").Output()
	if err != nil {
		return "", fmt.Errorf("run `go env GOMOD`: %w", err)
	}
	gomod := strings.TrimSpace(string(out))
	if gomod == "" || gomod == os.DevNull {
		return "", fmt.Errorf("no go.mod found for the current module (GOMOD=%q)", gomod)
	}
	return filepath.Dir(gomod), nil
}
