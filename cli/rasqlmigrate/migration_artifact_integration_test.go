//go:build unix

package rasqlmigrate

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/stretchr/testify/require"
)

func TestGeneratedMigrationArtifactRoundTripsLiveEngines(t *testing.T) {
	tests := []struct {
		name string
		dsn  func(*testing.T) string
	}{
		{name: "postgresql", dsn: func(t *testing.T) string {
			config := dbtest.PostgreSQLConfig(t)
			database := dbtest.PostgreSQLDB(t)
			_, err := database.ExecContext(t.Context(), "CREATE TABLE artifact_users (id INTEGER PRIMARY KEY)")
			require.NoError(t, err)
			return config.ConnString()
		}},
		{name: "mysql", dsn: func(t *testing.T) string {
			config := dbtest.MySQLConfig(t)
			database := dbtest.MySQLDB(t)
			_, err := database.ExecContext(t.Context(), "CREATE TABLE artifact_users (id INTEGER PRIMARY KEY)")
			require.NoError(t, err)
			return config.FormatDSN()
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			baseline := filepath.Join(root, "baseline")
			target := filepath.Join(root, "target")
			migrationRoot := filepath.Join(root, "migrations")
			table := "artifact_users"
			writeTestSchema(t, baseline, "tables/table.sql", fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY);\n", table))
			writeTestSchema(t, target, "tables/table.sql", fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, email TEXT);\n", table))
			directory := filepath.Join(migrationRoot, "001_add_email")
			setCommandOutput(t)
			require.NoError(t, run([]string{"diff", "-dialect", test.name, "-from", baseline, "-to", target, "-output", directory}))
			dsn := test.dsn(t)
			require.NoError(t, run([]string{"apply", "-dir", migrationRoot, "-dialect", test.name, "-dsn", dsn}))
			var database *sql.DB
			if test.name == "postgresql" {
				database = dbtest.PostgreSQLDB(t)
			} else {
				database = dbtest.MySQLDB(t)
			}
			var emailCount int
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'artifact_users' AND column_name = 'email'").Scan(&emailCount))
			require.Equal(t, 1, emailCount)
			require.NoError(t, run([]string{"revert", "-dir", migrationRoot, "-dialect", test.name, "-dsn", dsn, "-steps", "1"}))
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM information_schema.columns WHERE table_name = 'artifact_users' AND column_name = 'email'").Scan(&emailCount))
			require.Zero(t, emailCount)
			var tableCount int
			require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM information_schema.tables WHERE table_name = 'artifact_users'").Scan(&tableCount))
			require.Equal(t, 1, tableCount)
		})
	}
}
