//go:build unix

package dbtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
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
// up in isolation -- never the whole file -- together with the host port it
// publishes and the RASQL_TEST_*_DSN variable that names a database already
// using that port. compose.yaml declares no depends_on between its
// services and each healthcheck runs inside its own container, so bringing
// up one by name never waits on or starts the other.
type composeService struct {
	name   string
	port   string
	envVar string
}

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
	// CombinedOutput, not Run: a failure here is common (many machines
	// have Docker but not the compose plugin, or a plugin shim that
	// doesn't run), and composeOut is the only way to quote what actually
	// went wrong instead of discarding it -- see composeUnavailableError.
	composeOut, composeErr := exec.CommandContext(composeCtx, dockerPath, "compose", "version").CombinedOutput()
	if composeErr == nil {
		return composeRunner{bin: dockerPath, args: []string{"compose"}}, nil
	}

	if legacyPath, err := exec.LookPath("docker-compose"); err == nil {
		legacyCtx, legacyCancel := context.WithTimeout(context.Background(), probeTimeout)
		defer legacyCancel()
		if out, err := exec.CommandContext(legacyCtx, legacyPath, "version").CombinedOutput(); err != nil {
			return composeRunner{}, fmt.Errorf("the `docker-compose` binary is present but does not run (ran `docker-compose version`): %s", firstLine(out))
		}
		return composeRunner{bin: legacyPath}, nil
	}

	return composeRunner{}, composeUnavailableError(composeOut)
}

// composeSubcommandMissingPattern matches the Docker CLI's own wording when
// it does not recognize "compose" as a subcommand at all -- neither a
// built-in command nor an installed CLI plugin -- as opposed to a compose
// plugin that IS recognized but fails when actually invoked (a broken shim,
// an incompatible plugin layout, a permission error, ...). The Docker CLI's
// wording for the former is stable across versions:
//
//	docker: 'compose' is not a docker command.
//	See 'docker --help'
var composeSubcommandMissingPattern = regexp.MustCompile(`(?i)'compose' is not a docker command`)

// composeUnavailableError reports why no usable compose runner was found,
// once `docker compose version` has failed and no standalone
// docker-compose binary was found either. composeOut is that `docker
// compose version` failure's own combined output, captured by the caller
// with CombinedOutput rather than Run precisely so there is something here
// to quote.
//
// composeSubcommandMissingPattern separates two different facts composeOut
// can report. When it matches, the Docker CLI itself says "compose" is not
// a command it knows at all, so neither runner this package can use exists,
// and the message says so plainly. Otherwise, `docker compose` IS a
// recognized subcommand -- the plugin is installed -- and this reached here
// because invoking it failed for some other reason (a broken shim, an
// incompatible plugin layout, a permission error, ...); an earlier version
// of this package reported the "neither is available" message
// unconditionally, which was actively wrong for exactly this second, more
// common case, so that wording is now reserved for the case
// composeSubcommandMissingPattern actually confirms.
func composeUnavailableError(composeOut []byte) error {
	if composeSubcommandMissingPattern.Match(composeOut) {
		return fmt.Errorf("neither `docker compose` nor a standalone `docker-compose` binary is available (ran `docker compose version`): %s", firstLine(composeOut))
	}
	return fmt.Errorf("the `docker compose` plugin is installed but does not run (ran `docker compose version`): %s", firstLine(composeOut))
}

// displayName reports the command this runner invokes, for use in
// diagnostic messages -- "docker compose" for the plugin, "docker-compose"
// for the standalone binary -- so a message naming the runner never names a
// command that was never run.
func (r composeRunner) displayName() string {
	return strings.Join(append([]string{filepath.Base(r.bin)}, r.args...), " ")
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
// mysql services regardless of which one the caller actually needed, so a
// port collision on the OTHER service -- one the caller never asked for --
// skipped the caller's own test, naming the wrong RASQL_TEST_*_DSN
// variable and the wrong port. Naming service here, and probing only its
// own published port on a collision, keeps a PostgreSQL caller's skip
// decision independent of whatever is or isn't running on MySQL's port, and
// vice versa. This works safely against compose.yaml as written because it
// declares no depends_on between its two services and each healthcheck
// runs inside its own container, so `--wait` on one never waits on the
// other.
//
// Skip vs. fail here is deliberate; see the package doc for the reasoning.
// probeCompose failing skips: it is an environment fact, not something the
// test run should report as a rasql defect. A bring-up that fails after
// probeCompose has already confirmed Docker works fails loudly with
// t.Fatalf instead, because that means the compose file or an image
// reference is broken -- except when the failure is a host port already
// being in use, which is neither of those; see classifyPortCollision and
// findConflictingPortAmong in port_collision.go.
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
	// classifyPortCollision runs first and decides skip vs. fail entirely
	// from Docker's own wording; only once it has already said this
	// failure IS a port collision does findConflictingPortAmong run. That
	// order matters: findConflictingPortAmong binds a real socket, and if
	// it ran before classification it could turn some other, unrelated
	// bring-up failure into a skip just because the port happened to be
	// free or busy for reasons that have nothing to do with why `docker
	// compose up` actually failed. Classification alone gates skip vs.
	// fail; the probe only ever confirms whether service's own port is the
	// one actually held.
	if classifyPortCollision(string(out)) {
		port, _ := findConflictingPortAmong([]composePublishedPort{{port: service.port, envVar: service.envVar}})
		t.Skipf("skipping live database test: %s", bringUpFailureMessage(service, port))
		return
	}
	t.Fatalf("dbtest: %s up failed even though Docker is available: %v\n%s", runner.displayName(), err, out)
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
