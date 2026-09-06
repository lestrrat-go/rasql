//go:build unix

package rasqlmigrate

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate/diff"
	postgresqldiff "github.com/lestrrat-go/rasql/migrate/diff/postgresql"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func TestPostgreSQLCLIConcurrentIndexRoundTrip(t *testing.T) {
	config := dbtest.PostgreSQLConfig(t)
	database := dbtest.PostgreSQLDB(t)
	dsn := stdlib.RegisterConnConfig(config)
	t.Cleanup(func() { stdlib.UnregisterConnConfig(dsn) })
	root := t.TempDir()
	migrations := filepath.Join(root, "migrations")
	analyzer := postgresqldiff.New()
	baseline, err := analyzer.Parse([]diff.Source{{Path: "baseline.sql", SQL: sqltext.Text("CREATE TABLE users (id BIGINT PRIMARY KEY);")}})
	require.NoError(t, err)
	target, err := analyzer.Parse([]diff.Source{{Path: "target.sql", SQL: sqltext.Text("CREATE TABLE users (id BIGINT PRIMARY KEY); CREATE INDEX CONCURRENTLY users_id_idx ON users (id);")}})
	require.NoError(t, err)
	tableDirectory := filepath.Join(migrations, "001_create_users")
	require.NoError(t, os.MkdirAll(tableDirectory, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(tableDirectory, "001_create_users.up.sql"), []byte("CREATE TABLE users (id BIGINT PRIMARY KEY);\n"), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(tableDirectory, "001_create_users.down.sql"), []byte("DROP TABLE users;\n"), 0o600))
	indexPlan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Equal(t, "nontransactional", string(indexPlan.Mode))
	require.NoError(t, diff.WriteMigration(filepath.Join(migrations, "002_users_index"), indexPlan))
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
