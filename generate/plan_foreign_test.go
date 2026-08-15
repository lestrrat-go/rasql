package generate

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestScanForeignPackageSkipsUnreadableMarkerRead(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "helper.go"), []byte("package tools\n"), 0o600))

	original := readForeignMarker
	t.Cleanup(func() { readForeignMarker = original })
	readForeignMarker = func(string) (os.FileInfo, error) {
		return nil, errors.New("permission denied")
	}

	name, declared, err := scanForeignPackage(dir, "store", map[string]struct{}{})
	require.NoError(t, err)
	require.Empty(t, name)
	require.Empty(t, declared)
}
