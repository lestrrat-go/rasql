//go:build unix

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

// composeService names a single compose.yaml service ensureComposeUp brings
// up in isolation -- never the whole file -- together with the
// RASQL_TEST_*_DSN variable that points at it. compose.yaml declares no
// depends_on between its services and each healthcheck runs inside its own
// container, so bringing up one by name never waits on or starts the other.
type composeService struct {
	name   string
	envVar string
}

// probeTimeout bounds each Docker availability probe, so an unreachable
// daemon is reported quickly instead of hanging a test run.
const probeTimeout = 8 * time.Second

// bringUpTimeout bounds the compose bring-up itself, which can include an
// image pull on a first run.
const bringUpTimeout = 5 * time.Minute

// composeRunner invokes `docker compose` with a fixed set of leading
// arguments.
type composeRunner struct {
	bin string
}

func (r composeRunner) command(ctx context.Context, extra ...string) *exec.Cmd {
	args := make([]string, 0, len(extra)+1)
	args = append(args, "compose")
	args = append(args, extra...)
	return exec.CommandContext(ctx, r.bin, args...)
}

// probeCompose reports whether this machine can run `docker compose`,
// returning the reason it can't when it can't. It never brings anything up
// itself, so a caller can turn a failure into a t.Skip that names exactly
// which condition was detected:
//
//   - the `docker` binary missing from PATH entirely
//   - the daemon unreachable, including a permission error talking to its
//     socket -- the docker CLI's own error text says which
//   - the `compose` subcommand not available
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
	if out, err := exec.CommandContext(composeCtx, dockerPath, "compose", "version").CombinedOutput(); err != nil {
		return composeRunner{}, fmt.Errorf("the `compose` subcommand is not available (ran `docker compose version`): %s", firstLine(out))
	}

	return composeRunner{bin: dockerPath}, nil
}

// displayName reports the command this runner invokes, for use in
// diagnostic messages.
func (r composeRunner) displayName() string {
	return filepath.Base(r.bin) + " compose"
}

func firstLine(output []byte) string {
	trimmed := strings.TrimSpace(string(output))
	if trimmed == "" {
		return "(no output)"
	}
	return strings.SplitN(trimmed, "\n", 2)[0]
}

// ensureComposeUp brings up service, and only service, from the checked-in
// compose file if needed, or skips the calling test naming why Docker could
// not be used.
//
// It never brings up the whole compose file: an earlier version passed no
// service name to `docker compose up`, which started both the postgres and
// mysql services regardless of which one the caller actually needed. Naming
// service here keeps a PostgreSQL caller's bring-up independent of whatever
// is or isn't running on MySQL's port, and vice versa. This works safely
// against compose.yaml as written because it declares no depends_on between
// its two services and each healthcheck runs inside its own container, so
// `--wait` on one never waits on the other.
//
// Skip vs. fail here is deliberate; see the package doc for the reasoning.
// probeCompose failing skips: it is an environment fact, not something the
// test run should report as a rasql defect. A bring-up that fails after
// probeCompose has already confirmed Docker works fails loudly with
// t.Fatalf instead, carrying Docker's own message: that means the compose
// file or an image reference is broken, or a host port it wants is already
// in use by something else (commonly a PostgreSQL or MySQL server already
// running locally) -- either way, something the person running the suite
// can see and fix, for example by setting RASQL_TEST_POSTGRES_DSN or
// RASQL_TEST_MYSQL_DSN to point at the database they already have.
func ensureComposeUp(t *testing.T, service composeService) {
	t.Helper()

	runner, err := probeCompose()
	if err != nil {
		t.Skipf("skipping live database test: %s; set %s to point at a running database, or start one locally with `docker compose up -d --wait %s` (uses %s)", err, service.envVar, service.name, composeFileName)
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
	cmd := runner.command(upCtx, "-f", composePath, "up", "-d", "--wait", service.name)
	out, err := cmd.CombinedOutput()
	if err == nil {
		return
	}
	t.Fatalf("dbtest: %s up failed even though Docker is available: %v\n%s\nset %s to point at a database you already have running instead of bringing up compose", runner.displayName(), err, out, service.envVar)
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
