package conformance

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/gensum"
	"github.com/stretchr/testify/require"
)

// sharedOfflineGOCACHE is the build cache directory an offline correctness
// check passes to offlineBuildEnv when it has no reason to isolate its own
// cache. It is stable across both a single `go test ./...` run and repeated
// runs on the same machine, unlike a cache rooted under t.TempDir(). Every
// caller only needs the generated code to compile and run correctly, and
// caching is orthogonal to that, so sharing this directory saves the repeated
// dependency compilation a fresh GOCACHE would otherwise pay on every call.
var sharedOfflineGOCACHE = filepath.Join(os.TempDir(), "rasql-d4-gocache")

// offlineBuildEnv builds the environment a `go` subprocess runs under: it drops
// every live DSN so a scratch module cannot reach a real server, and pins the
// module-resolution variables so the subprocess compiles from the module cache
// alone rather than reaching the network.
func offlineBuildEnv(cache string) []string {
	blocked := map[string]struct{}{
		"RASQL_TEST_POSTGRES_DSN": {}, "RASQL_TEST_MYSQL_DSN": {},
		"TASKBOARD_DSN": {}, "TASKBOARD_TEST_DSN": {},
		"GOPROXY": {}, "GOSUMDB": {}, "GOTOOLCHAIN": {}, "GOFLAGS": {}, "GOCACHE": {}, "GOWORK": {},
	}
	env := make([]string, 0, len(os.Environ())+5)
	for _, value := range os.Environ() {
		name, _, ok := strings.Cut(value, "=")
		if ok {
			if _, blocked := blocked[name]; blocked {
				continue
			}
		}
		env = append(env, value)
	}
	return append(env, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod", "GOWORK=off", "GOCACHE="+cache)
}

func TestOfflineBuildEnvRemovesLiveDSNs(t *testing.T) {
	t.Setenv("RASQL_TEST_POSTGRES_DSN", "postgres://secret")
	t.Setenv("RASQL_TEST_MYSQL_DSN", "mysql://secret")
	t.Setenv("GOPROXY", "https://proxy.invalid")
	t.Setenv("GOSUMDB", "sumdb.invalid")
	t.Setenv("GOTOOLCHAIN", "go1.99")
	t.Setenv("GOFLAGS", "-mod=vendor")
	t.Setenv("GOCACHE", "stale-cache")
	t.Setenv("GOWORK", "stale.work")
	cache := filepath.Join(t.TempDir(), "cache")
	command := exec.Command(os.Args[0], "-test.run", "^TestOfflineBuildEnvChild$")
	command.Env = append(offlineBuildEnv(cache), "RASQL_D4_ENV_CHILD=1", "RASQL_D4_EXPECT_CACHE="+cache)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestOfflineBuildEnvChild(t *testing.T) {
	if os.Getenv("RASQL_D4_ENV_CHILD") != "1" {
		return
	}
	require.Empty(t, os.Getenv("RASQL_TEST_POSTGRES_DSN"))
	require.Empty(t, os.Getenv("RASQL_TEST_MYSQL_DSN"))
	require.Equal(t, "off", os.Getenv("GOPROXY"))
	require.Equal(t, "off", os.Getenv("GOSUMDB"))
	require.Equal(t, "local", os.Getenv("GOTOOLCHAIN"))
	require.Equal(t, "-mod=mod", os.Getenv("GOFLAGS"))
	require.Equal(t, "off", os.Getenv("GOWORK"))
	require.Equal(t, os.Getenv("RASQL_D4_EXPECT_CACHE"), os.Getenv("GOCACHE"))
	for _, name := range []string{"GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "GOCACHE"} {
		count := 0
		for _, value := range os.Environ() {
			if strings.HasPrefix(value, name+"=") {
				count++
			}
		}
		require.Equal(t, 1, count, name)
	}
}

// TestFixtureStoreSumsNameTheirEngine reads each fixture's checked-in rasql.sum and requires it to
// record the engine whose directory it sits in, along with some engine profile. codegen generate
// writes both fields, so a fixture regenerated against the wrong server fails here.
//
// TestConformanceFixturesAreCurrent owns the stronger claim -- that the whole checked-in store is
// what codegen generate produces from this fixture's migrations today -- by running codegen check
// in-process against a live server.
func TestFixtureStoreSumsNameTheirEngine(t *testing.T) {
	digest, err := PortableSignatureDigestChecked()
	require.NoError(t, err)
	require.Len(t, digest, 64)
	for _, engine := range []string{"sqlite", "postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			sumData, err := os.ReadFile(filepath.Join("testdata", engine, "internal", "store", "rasql.sum"))
			require.NoError(t, err)
			sum, err := gensum.Parse(sumData)
			require.NoError(t, err)
			require.Equal(t, engine, sum.Dialect)
			require.NotEmpty(t, sum.Profile)
		})
	}
}

func copyTree(source, destination string) error {
	return filepath.Walk(source, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if info.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		input, err := os.Open(path)
		if err != nil {
			return err
		}
		defer func() { _ = input.Close() }()
		output, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode().Perm())
		if err != nil {
			return err
		}
		defer func() { _ = output.Close() }()
		_, err = io.Copy(output, input)
		return err
	})
}

// copyFixtureAsModule copies testdata/<engine> into a fresh temporary directory, nested under
// internal/conformance/testdata/<engine> exactly as it sits in the real repository, with go.mod
// (copied from the repository's own) at the new root. rasql.sum's migration and query entries are
// named relative to the module root, and the checked-in sum was generated with the real repository
// as that root, so a copy that flattened the fixture to its own module root would make every path
// a fresh check or generate computed there disagree with the sum copied alongside it. It returns
// the new module root and the nested fixture directory, which holds rasql.json.
func copyFixtureAsModule(t *testing.T, engine string) (moduleRoot, fixtureDir string) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	moduleRoot = t.TempDir()
	fixtureDir = filepath.Join(moduleRoot, "internal", "conformance", "testdata", engine)
	require.NoError(t, copyTree(filepath.Join("testdata", engine), fixtureDir))
	goMod, err := os.ReadFile(filepath.Join(repoRoot, "go.mod"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, "go.mod"), goMod, 0o644))
	return moduleRoot, fixtureDir
}
