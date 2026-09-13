//go:build unix

package rasqlmigrate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

// TestApplyMySQLAlreadyAppliedWarnsAndCompletes pins what the command prints
// when MySQL reports that a source's work was already done: the warning goes
// to the diagnostics stream and the applied line to the output stream.
func TestApplyMySQLAlreadyAppliedWarnsAndCompletes(t *testing.T) {
	database := dbtest.MySQLDB(t)
	dsn := dbtest.MySQLConfig(t).FormatDSN()
	root := t.TempDir()
	directory := filepath.Join(root, "001_create_widgets")
	require.NoError(t, os.MkdirAll(directory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "001_create.up.sql"), []byte("CREATE TABLE cli_widgets (id BIGINT NOT NULL PRIMARY KEY) ENGINE=InnoDB;\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "002_seed.up.sql"), []byte("INSERT INTO cli_widget_kinds VALUES (1);\n"), 0o600))

	var output, diagnostics bytes.Buffer
	err := Run([]string{"apply", "-dir", root, "-dialect", "mysql", "-dsn", dsn}, &output, &diagnostics)
	require.ErrorContains(t, err, `migrate: execute migration "001_create_widgets" SQL source "002_seed.up.sql"`)
	require.Empty(t, diagnostics.String())

	_, execErr := database.ExecContext(t.Context(), "CREATE TABLE cli_widget_kinds (id BIGINT NOT NULL) ENGINE=InnoDB")
	require.NoError(t, execErr)
	output.Reset()
	diagnostics.Reset()
	require.NoError(t, Run([]string{"apply", "-dir", root, "-dialect", "mysql", "-dsn", dsn}, &output, &diagnostics))
	require.Equal(t, "migrate: warning: migration \"001_create_widgets\" SQL source \"001_create.up.sql\" was already applied: Error 1050 (42S01): Table 'cli_widgets' already exists\n", diagnostics.String())
	require.Equal(t, "applied\t001_create_widgets\nmigration apply completed: 1 applied\n", output.String())
}
