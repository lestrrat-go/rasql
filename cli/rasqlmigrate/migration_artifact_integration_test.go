//go:build unix

package rasqlmigrate

import (
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
		{name: "postgresql", dsn: func(t *testing.T) string { return dbtest.PostgreSQLConfig(t).ConnString() }},
		{name: "mysql", dsn: func(t *testing.T) string { return dbtest.MySQLConfig(t).FormatDSN() }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			baseline := filepath.Join(root, "baseline")
			target := filepath.Join(root, "target")
			migrationRoot := filepath.Join(root, "migrations")
			table := "artifact_" + filenamePart(t.Name())
			writeTestSchema(t, baseline, "tables/table.sql", fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY);\n", table))
			writeTestSchema(t, target, "tables/table.sql", fmt.Sprintf("CREATE TABLE %s (id INTEGER PRIMARY KEY, email TEXT);\n", table))
			directory := filepath.Join(migrationRoot, "001_add_email")
			setCommandOutput(t)
			require.NoError(t, run([]string{"diff", "-dialect", test.name, "-from", baseline, "-to", target, "-output", directory}))
			dsn := test.dsn(t)
			require.NoError(t, run([]string{"apply", "-dir", migrationRoot, "-dialect", test.name, "-dsn", dsn}))
			require.NoError(t, run([]string{"revert", "-dir", migrationRoot, "-dialect", test.name, "-dsn", dsn, "-steps", "1"}))
		})
	}
}
