package taskboard_test

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestCompileFailFixturesRejectLegacyConsumerAPIs(t *testing.T) {
	repoRoot := taskboardRepositoryRoot(t)
	fixtures := []struct {
		name   string
		needle string
	}{
		{name: "string_write", needle: ".Column undefined"},
		{name: "projection_result", needle: "cannot use expressions"},
	}
	for _, fixture := range fixtures {
		t.Run(fixture.name, func(t *testing.T) {
			module := copyTaskboardModule(t, repoRoot)
			fixtureSource := filepath.Join(
				module,
				"testdata",
				"compilefail",
				fixture.name,
				"main.go",
			)
			packageDir := filepath.Join(module, "internal", "compilefail", fixture.name)
			if err := os.MkdirAll(packageDir, 0o755); err != nil {
				t.Fatalf("create fixture package: %s", err)
			}
			if err := copyFile(fixtureSource, filepath.Join(packageDir, "main.go")); err != nil {
				t.Fatalf("copy fixture: %s", err)
			}

			cmd := exec.Command("go", "test", "-mod=mod", "./internal/compilefail/"+fixture.name)
			cmd.Dir = module
			cmd.Env = withoutTaskboardTestDSNs(os.Environ())
			output, err := cmd.CombinedOutput()
			if err == nil {
				t.Fatalf("fixture unexpectedly compiled:\n%s", output)
			}
			if !strings.Contains(string(output), fixture.needle) {
				t.Fatalf("compile failure omitted %q:\n%s", fixture.needle, output)
			}
		})
	}
}

func taskboardRepositoryRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(filename), "..", ".."))
}

func copyTaskboardModule(t *testing.T, repoRoot string) string {
	t.Helper()
	source := filepath.Join(repoRoot, "sample", "taskboard")
	destination := filepath.Join(t.TempDir(), "taskboard")
	if err := copyTree(source, destination); err != nil {
		t.Fatalf("copy Taskboard module: %s", err)
	}
	goMod := filepath.Join(destination, "go.mod")
	contents, err := os.ReadFile(goMod)
	if err != nil {
		t.Fatalf("read copied go.mod: %s", err)
	}
	contents = bytes.Replace(contents,
		[]byte("replace github.com/lestrrat-go/rasql => ../.."),
		[]byte("replace github.com/lestrrat-go/rasql => "+repoRoot),
		1,
	)
	if err := os.WriteFile(goMod, contents, 0o644); err != nil {
		t.Fatalf("write copied go.mod: %s", err)
	}
	return destination
}

func copyTree(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		return copyFile(path, target)
	})
}

func copyFile(source, destination string) error {
	contents, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(destination, contents, 0o644); err != nil {
		return fmt.Errorf("copy %s: %w", source, err)
	}
	return nil
}

func withoutTaskboardTestDSNs(environment []string) []string {
	result := make([]string, 0, len(environment))
	for _, value := range environment {
		if strings.HasPrefix(value, "TASKBOARD_SCHEMA_DSN=") || strings.HasPrefix(value, "TASKBOARD_DSN=") || strings.HasPrefix(value, "TASKBOARD_TEST_DSN=") {
			continue
		}
		result = append(result, value)
	}
	return result
}
