//go:build unix

package rasqlgen

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// This test uses Unix-only symbolic links.

func TestRunSchemaWritesToOutputDirectorySymlink(t *testing.T) {
	directory := t.TempDir()
	databasePath := filepath.Join(directory, "schema.db")
	database, err := sql.Open("sqlite", databasePath)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	require.NoError(t, database.Close())

	target := filepath.Join(directory, "generated")
	require.NoError(t, os.Mkdir(target, 0o700))
	output := filepath.Join(directory, "output")
	require.NoError(t, os.Symlink("generated", output))

	require.NoError(t, run([]string{"schema", "-dsn", databasePath, "-dialect", "sqlite", "-table", "users", "-package", "generated", "-output", output}))
	require.FileExists(t, filepath.Join(target, "users_gen.go"))
}
