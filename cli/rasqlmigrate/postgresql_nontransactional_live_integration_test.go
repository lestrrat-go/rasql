//go:build unix

package rasqlmigrate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestPostgreSQLCLIConcurrentIndexRoundTrip(t *testing.T) {
	config := dbtest.PostgreSQLConfig(t)
	database := dbtest.PostgreSQLDB(t)
	dsn := stdlib.RegisterConnConfig(config)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(dsn) })
	root := t.TempDir()
	migrations := filepath.Join(root, "migrations")
	write := func(id, name, source string) {
		directory := filepath.Join(migrations, id)
		require.NoError(t, os.MkdirAll(directory, 0o700))
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(source), 0o600))
	}
	write("001_create_users", "001_create_users.up.sql", "CREATE TABLE users (id BIGINT PRIMARY KEY);\n")
	write("001_create_users", "001_create_users.down.sql", "DROP TABLE users;\n")
	write("002_users_index", "001_users_index.up.sql", "CREATE INDEX CONCURRENTLY users_id_idx ON users (id);\n")
	write("002_users_index", "001_users_index.down.sql", "DROP INDEX CONCURRENTLY users_id_idx;\n")
	var output bytes.Buffer
	require.NoError(t, Run([]string{"apply", "-dir", migrations, "-dialect", "postgresql", "-dsn", dsn}, &output, &output))
	var valid, ready bool
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT indisvalid, indisready FROM pg_index WHERE indexrelid = 'users_id_idx'::regclass").Scan(&valid, &ready))
	require.True(t, valid)
	require.True(t, ready)
	output.Reset()
	require.NoError(t, Run([]string{"verify", "-dir", migrations, "-dialect", "postgresql", "-dsn", dsn}, &output, &output))
	output.Reset()
	require.NoError(t, Run([]string{"revert", "-dir", migrations, "-dialect", "postgresql", "-dsn", dsn, "-steps", "1"}, &output, &output))
	var remaining int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM pg_class WHERE relname = 'users_id_idx'").Scan(&remaining))
	require.Zero(t, remaining)
}
