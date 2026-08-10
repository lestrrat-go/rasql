package diff

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLoadSourcesRejectsOversizedSource(t *testing.T) {
	directory := t.TempDir()
	writeSparseSource(t, filepath.Join(directory, "oversized.sql"), maxSourceFileBytes+1)

	_, err := LoadSources(directory)
	require.ErrorContains(t, err, "per-file limit")
}

func TestLoadSourcesRejectsExcessiveAggregateSize(t *testing.T) {
	directory := t.TempDir()
	sourceSize := maxSourceFileBytes / 2
	for index := 0; index <= maxSourceBytes/sourceSize; index++ {
		writeSparseSource(t, filepath.Join(directory, fmt.Sprintf("%02d.sql", index)), sourceSize)
	}

	_, err := LoadSources(directory)
	require.ErrorContains(t, err, "aggregate byte limit")
}

func TestLoadSourcesRejectsExcessiveSourceCount(t *testing.T) {
	directory := t.TempDir()
	for index := 0; index <= maxSourceCount; index++ {
		writeSparseSource(t, filepath.Join(directory, fmt.Sprintf("%04d.sql", index)), 1)
	}

	_, err := LoadSources(directory)
	require.ErrorContains(t, err, "source count limit")
}

func writeSparseSource(t *testing.T, path string, size int) {
	t.Helper()
	file, err := os.Create(path)
	require.NoError(t, err)
	require.NoError(t, file.Truncate(int64(size)))
	require.NoError(t, file.Close())
}
