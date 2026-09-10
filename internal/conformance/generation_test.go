package conformance

import (
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/gensum"
	"github.com/stretchr/testify/require"
)

// sharedOfflineGOCACHE is the build cache directory an offline correctness
// check should pass to offlineBuildEnv (or set as its own GOCACHE, as this
// file's two callers do) when it has no reason to isolate its own cache. It
// is stable across both a single `go test ./...` run and repeated runs on
// the same machine, unlike a cache rooted under t.TempDir():
// TestGeneratedFootprintBuild's own per-fixture GOCACHE (see its CachePolicy
// field, in generated_footprint_test.go) is the deliberate exception,
// because that test measures build time itself and a shared, already-warm
// cache would make its samples measure less than the fresh-clone build it
// exists to characterize. Every other caller only needs the generated code
// to compile and run correctly; caching is orthogonal to that, so sharing
// this directory saves the repeated dependency compilation a fresh GOCACHE
// would otherwise pay on every call. Defined in this file -- which, unlike
// generated_footprint_test.go, carries no linux/darwin build constraint --
// so every caller in the package can reach it regardless of its own
// constraint.
var sharedOfflineGOCACHE = filepath.Join(os.TempDir(), "rasql-d4-gocache")

func TestGeneratedFootprintBuildScaffold(t *testing.T) {
	digest, err := PortableSignatureDigestChecked()
	require.NoError(t, err)
	require.Len(t, digest, 64)
	repoRoot := filepath.Join("..", "..")
	for _, engine := range []string{"sqlite", "postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			fixture := filepath.Join("testdata", engine)
			manifestData, err := os.ReadFile(filepath.Join(fixture, "d4-manifest.json"))
			require.NoError(t, err)
			var manifest struct {
				Engine                  string `json:"engine"`
				Profile                 string `json:"profile"`
				PortableSignatureDigest string `json:"portable_signature_digest"`
			}
			require.NoError(t, json.Unmarshal(manifestData, &manifest))
			require.Equal(t, digest, manifest.PortableSignatureDigest)
			require.Equal(t, engine, manifest.Engine)
			require.NotEmpty(t, manifest.Profile)
			sumData, err := os.ReadFile(filepath.Join(fixture, "internal", "store", "rasql.sum"))
			require.NoError(t, err)
			sum, err := gensum.Parse(sumData)
			require.NoError(t, err)
			require.Equal(t, manifest.Engine, sum.Dialect)
			require.Equal(t, manifest.Profile, sum.Profile)

			_, nestedFixture := copyFixtureAsModule(t, engine)
			command := exec.Command("go", "run", "./cmd/rasql", "codegen", "check", "-config", filepath.Join(nestedFixture, "rasql.json"))
			command.Dir = repoRoot
			command.Env = append(os.Environ(), "GOCACHE="+sharedOfflineGOCACHE)
			output, runErr := command.CombinedOutput()
			require.NoError(t, runErr, string(output))
			require.Contains(t, string(output), "internal/store is up to date")

			files, bytes := footprint(nestedFixture)
			require.GreaterOrEqual(t, files, 5)
			require.Positive(t, bytes)
		})
	}
	output := filepath.Join(t.TempDir(), "conformance.test")
	command := exec.Command("go", "test", "-c", "-o", output, "./internal/conformance")
	command.Env = append(os.Environ(), "GOCACHE="+sharedOfflineGOCACHE)
	command.Dir = repoRoot
	require.NoError(t, command.Run())
	_, err = os.Stat(output)
	require.NoError(t, err)
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

func footprint(root string) (int, int64) {
	files := 0
	var bytes int64
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			files++
			bytes += info.Size()
		}
		return nil
	})
	return files, bytes
}
