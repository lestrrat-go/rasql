//go:build unix

package generate_test

import (
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/stretchr/testify/require"
)

func TestQueryPackagePlanSnapshotsSharedInputOnce(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "query.sql")
	require.NoError(t, syscall.Mkfifo(input, 0o600))

	done := make(chan struct{})
	writes := writeFIFOQueryInputs(
		input,
		done,
		`SELECT id FROM users WHERE id = {{bind "id"}}`,
		`SELECT email FROM users WHERE email = {{bind "email"}}`,
	)

	plan, err := (generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{
			{Input: "query.sql", Function: "First", Output: "first_gen.go"},
			{Input: "query.sql", Function: "Second", Output: "second_gen.go"},
		},
	}).Plan()
	close(done)
	result := <-writes
	require.NoError(t, result.err)
	require.Equal(t, 1, result.count)
	require.NoError(t, err)

	files := plan.Files()
	require.Len(t, files, 2)
	sources := make(map[string]string, len(files))
	for _, file := range files {
		sources[filepath.Base(file.Path)] = string(file.Source)
	}
	require.Contains(t, sources["first_gen.go"], "func First(id any)")
	require.Contains(t, sources["second_gen.go"], "func Second(id any)")
	require.NotContains(t, sources["second_gen.go"], "email any")
}

type fifoQueryInputResult struct {
	count int
	err   error
}

func writeFIFOQueryInputs(path string, done <-chan struct{}, sources ...string) <-chan fifoQueryInputResult {
	results := make(chan fifoQueryInputResult, 1)
	go func() {
		var result fifoQueryInputResult
		defer func() { results <- result }()
		for index, source := range sources {
			for {
				select {
				case <-done:
					return
				default:
				}
				fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
				if err == syscall.ENXIO {
					timer := time.NewTimer(10 * time.Millisecond)
					select {
					case <-done:
						timer.Stop()
						return
					case <-timer.C:
					}
					continue
				}
				if err != nil {
					result.err = err
					return
				}
				file := os.NewFile(uintptr(fd), path)
				_, writeErr := file.Write([]byte(source))
				closeErr := file.Close()
				if writeErr != nil {
					result.err = writeErr
					return
				}
				if closeErr != nil {
					result.err = closeErr
					return
				}
				result.count++
				if index+1 < len(sources) && !waitFIFOQueryInputReaderClosed(path, done, &result) {
					return
				}
				break
			}
		}
	}()
	return results
}

func waitFIFOQueryInputReaderClosed(path string, done <-chan struct{}, result *fifoQueryInputResult) bool {
	for {
		select {
		case <-done:
			return false
		default:
		}
		fd, err := syscall.Open(path, syscall.O_WRONLY|syscall.O_NONBLOCK, 0)
		if err == syscall.ENXIO {
			return true
		}
		if err != nil {
			result.err = err
			return false
		}
		if err := syscall.Close(fd); err != nil {
			result.err = err
			return false
		}
		timer := time.NewTimer(10 * time.Millisecond)
		select {
		case <-done:
			timer.Stop()
			return false
		case <-timer.C:
		}
	}
}

func TestQueryPackageWriteRefusesMarkedGeneratedSymlinkOrphan(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	dir := filepath.Join(root, "store")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	target := filepath.Join(root, "old_gen.go")
	require.NoError(t, os.WriteFile(target, []byte(genfile.Marker+"\n\npackage store\n\nfunc Existing() {}\n"), 0o600))
	orphan := filepath.Join(dir, "old_gen.go")
	require.NoError(t, os.Symlink(target, orphan))

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "NewQuery", Output: "new_gen.go"}},
	}

	err := queries.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale), err)
	require.ErrorContains(t, err, "old_gen.go")

	err = queries.Write()
	require.ErrorContains(t, err, "old_gen.go")
	require.NoFileExists(t, filepath.Join(dir, "new_gen.go"))
	require.FileExists(t, orphan)
}

func TestQueryPackageRejectsMarkedGeneratedSymlinkDeclarationCollision(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	dir := filepath.Join(root, "store")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	target := filepath.Join(root, "old_gen.go")
	require.NoError(t, os.WriteFile(target, []byte(genfile.Marker+"\n\npackage store\n\nfunc Existing() {}\n"), 0o600))
	require.NoError(t, os.Symlink(target, filepath.Join(dir, "old_gen.go")))

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "Existing", Output: "new_gen.go"}},
	}

	_, err := queries.Plan()
	require.ErrorContains(t, err, `query function "Existing" collides with package-level declaration "Existing" in old_gen.go`)
	require.ErrorContains(t, queries.Write(), `query function "Existing" collides with package-level declaration "Existing" in old_gen.go`)
	require.NoFileExists(t, filepath.Join(dir, "new_gen.go"))
}

func TestQueryPackageCommitRefusesADirectoryFirstCreatedAsASymlink(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	dir := filepath.Join(root, "store")
	elsewhere := filepath.Join(root, "elsewhere")
	require.NoError(t, os.MkdirAll(elsewhere, 0o700))
	require.NoDirExists(t, dir)

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
	}
	plan, err := queries.Plan()
	require.NoError(t, err)
	require.NoError(t, os.Symlink(elsewhere, dir))

	err = plan.Commit()
	require.ErrorContains(t, err, "refusing to commit into "+dir)
	entries, readErr := os.ReadDir(elsewhere)
	require.NoError(t, readErr)
	require.Empty(t, entries, "Commit must write nothing into a directory the plan never read")
	info, statErr := os.Lstat(dir)
	require.NoError(t, statErr)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "Commit must leave the link alone")
}

func TestQueryPackageCommitRefusesADirectoryReplacedWithASymlink(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	dir := filepath.Join(root, "store")
	elsewhere := filepath.Join(root, "elsewhere")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.MkdirAll(elsewhere, 0o700))

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
	}
	plan, err := queries.Plan()
	require.NoError(t, err)
	require.NoError(t, os.Rename(dir, filepath.Join(root, "store-original")))
	require.NoError(t, os.Symlink(elsewhere, dir))

	err = plan.Commit()
	require.ErrorContains(t, err, "refusing to commit into "+dir)
	entries, readErr := os.ReadDir(elsewhere)
	require.NoError(t, readErr)
	require.Empty(t, entries, "Commit must write nothing into a directory the plan never read")
	info, statErr := os.Lstat(dir)
	require.NoError(t, statErr)
	require.NotZero(t, info.Mode()&os.ModeSymlink, "Commit must leave the link alone")
}

func TestQueryPackageCommitRefusesADestinationThatAppearedAsASymlink(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	dir := filepath.Join(root, "store")
	victim := filepath.Join(root, "victim_gen.go")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	original := []byte(genfile.Marker + "\n\npackage store\n\nfunc Victim() {}\n")
	require.NoError(t, os.WriteFile(victim, original, 0o600))

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
	}
	plan, err := queries.Plan()
	require.NoError(t, err)
	planned := filepath.Join(dir, "query_gen.go")
	require.NoError(t, os.Symlink(victim, planned))

	err = plan.Commit()
	require.ErrorContains(t, err, "destination changed after QueryPackage.Plan")
	got, readErr := os.ReadFile(victim)
	require.NoError(t, readErr)
	require.Equal(t, original, got)
	info, statErr := os.Lstat(planned)
	require.NoError(t, statErr)
	require.NotZero(t, info.Mode()&os.ModeSymlink)
}

func TestQueryPackageCheckRefusesADestinationThatChangedToASymlink(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	dir := filepath.Join(root, "store")
	require.NoError(t, os.MkdirAll(dir, 0o700))

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
	}
	plan, err := queries.Plan()
	require.NoError(t, err)
	require.NoError(t, plan.Commit())

	planned := filepath.Join(dir, "query_gen.go")
	victim := filepath.Join(root, "victim_gen.go")
	require.NoError(t, os.Rename(planned, victim))
	require.NoError(t, os.Symlink(victim, planned))

	err = plan.Check()
	require.ErrorContains(t, err, "destination changed after QueryPackage.Plan")
	require.False(t, errors.Is(err, generate.ErrStale), "a changed destination is a refusal, not staleness")
}
