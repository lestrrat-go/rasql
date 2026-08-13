//go:build unix

package genfile

import (
	"io/fs"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// These tests use Unix-only resource limits, signals, and symbolic links.
// generatedSource, the source they write, lives in genfile_test.go as
// writtenSource so the portable tests can write it too.
const generatedSource = writtenSource

// staleGeneratedSource stands in for output an earlier run left behind.
// It carries the generated marker on its first line, because that is what
// makes Write willing to replace a destination at all: a file without the
// marker is refused, which is what TestWriteRefusesHandWrittenDestination
// covers.
const staleGeneratedSource = Marker + "\n\npackage stale\n"

func TestWriteGeneratedFilePreservesExistingOutputWhenWriteFails(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "output_gen.go")
	sentinel := []byte(Marker + "\n\n// SENTINEL-DATA-DO-NOT-TRUNCATE\n")
	require.NoError(t, os.WriteFile(output, sentinel, 0o600))

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGXFSZ)
	t.Cleanup(func() {
		signal.Stop(signals)
	})
	var limit syscall.Rlimit
	require.NoError(t, syscall.Getrlimit(syscall.RLIMIT_FSIZE, &limit))
	original := limit
	limit.Cur = 1
	require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_FSIZE, &limit))
	t.Cleanup(func() {
		require.NoError(t, syscall.Setrlimit(syscall.RLIMIT_FSIZE, &original))
	})

	err := Write(output, []byte(generatedSource))
	require.Error(t, err)
	got, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, sentinel, got)
}

func TestWriteGeneratedFileKeepsOutputModeBits(t *testing.T) {
	for _, tc := range []struct {
		name     string
		existing fs.FileMode
		want     fs.FileMode
	}{
		{name: "new output is created at 0600", want: 0o600},
		{name: "existing 0644 output keeps 0644", existing: 0o644, want: 0o644},
		{name: "existing sticky output keeps the sticky bit", existing: 0o755 | fs.ModeSticky, want: 0o755 | fs.ModeSticky},
		{name: "existing setuid output does not keep setuid", existing: 0o755 | fs.ModeSetuid, want: 0o755},
		{name: "existing setgid output does not keep setgid", existing: 0o755 | fs.ModeSetgid, want: 0o755},
	} {
		t.Run(tc.name, func(t *testing.T) {
			directory := t.TempDir()
			output := filepath.Join(directory, "output_gen.go")
			if tc.existing != 0 {
				require.NoError(t, os.WriteFile(output, []byte(staleGeneratedSource), tc.existing))
				require.NoError(t, os.Chmod(output, tc.existing))
			}

			require.NoError(t, Write(output, []byte(generatedSource)))
			info, err := os.Stat(output)
			require.NoError(t, err)
			require.Equal(t, tc.want, info.Mode()&(fs.ModePerm|fs.ModeSticky|fs.ModeSetuid|fs.ModeSetgid))
		})
	}
}

// TestWriteGeneratedFileAcceptsGenTestSuffix confirms the suffix guard's
// unix-specific write path (mode preservation, atomic rename) also covers a
// _gen_test.go destination, the one a schema run writes that does not end in
// plain _gen.go.
func TestWriteGeneratedFileAcceptsGenTestSuffix(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "schema_gen_test.go")
	require.NoError(t, os.WriteFile(output, []byte(staleGeneratedSource), 0o644))
	require.NoError(t, os.Chmod(output, 0o644))

	require.NoError(t, Write(output, []byte(generatedSource)))
	source, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Equal(t, []byte(generatedSource), source)
	info, err := os.Stat(output)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o644), info.Mode().Perm())
}

func TestWriteGeneratedFileRejectsDirectory(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "outdir")
	require.NoError(t, os.Mkdir(output, 0o700))

	err := Write(output, []byte(generatedSource))
	require.ErrorContains(t, err, "is a directory")
	require.Empty(t, mustReadDir(t, output))
}

func TestWriteGeneratedFileRejectsSymlinkToDirectory(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target")
	require.NoError(t, os.Mkdir(target, 0o700))
	output := filepath.Join(directory, "output_gen.go")
	require.NoError(t, os.Symlink("target", output))

	err := Write(output, []byte(generatedSource))
	require.ErrorContains(t, err, "is a directory")
	info, err := os.Lstat(output)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSymlink)
	require.Empty(t, mustReadDir(t, target))
}

func TestWriteGeneratedFileWritesThroughOutputSymlink(t *testing.T) {
	directory := t.TempDir()
	target := filepath.Join(directory, "target_gen.go")
	output := filepath.Join(directory, "output_gen.go")
	require.NoError(t, os.WriteFile(target, []byte(staleGeneratedSource), 0o644))
	require.NoError(t, os.Chmod(target, 0o644))
	require.NoError(t, os.Symlink("target_gen.go", output))

	require.NoError(t, Write(output, []byte(generatedSource)))
	info, err := os.Lstat(output)
	require.NoError(t, err)
	require.NotZero(t, info.Mode()&fs.ModeSymlink)
	source, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, []byte(generatedSource), source)
	info, err = os.Stat(target)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o644), info.Mode().Perm())
}

func TestWriteGeneratedFileRejectsOutputSymlinkCycle(t *testing.T) {
	directory := t.TempDir()
	output := filepath.Join(directory, "output_gen.go")
	other := filepath.Join(directory, "other_gen.go")
	require.NoError(t, os.Symlink("other_gen.go", output))
	require.NoError(t, os.Symlink("output_gen.go", other))

	err := Write(output, []byte(generatedSource))
	require.ErrorContains(t, err, "too many levels of symbolic links")
}

func mustReadDir(t *testing.T, directory string) []os.DirEntry {
	t.Helper()
	entries, err := os.ReadDir(directory)
	require.NoError(t, err)
	return entries
}
