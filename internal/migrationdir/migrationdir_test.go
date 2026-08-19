package migrationdir_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/stretchr/testify/require"
)

// writeMigration writes one migration directory holding the named sources.
func writeMigration(t *testing.T, root string, id string, sources map[string]string) {
	t.Helper()
	directory := filepath.Join(root, id)
	require.NoError(t, os.MkdirAll(directory, 0o700))
	for name, sql := range sources {
		require.NoError(t, os.WriteFile(filepath.Join(directory, name), []byte(sql), 0o600))
	}
}

func TestLoadReadsForwardAndReverseSources(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_initial", map[string]string{
		"001_users.up.sql":      "CREATE TABLE users (id INTEGER);\n",
		"001_users.down.sql":    "DROP TABLE users;\n",
		"002_users_ix.up.sql":   "CREATE INDEX users_ix ON users (id);\n",
		"002_users_ix.down.sql": "DROP INDEX users_ix;\n",
	})

	migrations, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, migrations, 1)

	require.Equal(t, []string{"001_users.up.sql", "002_users_ix.up.sql"}, sourceNames(migrations[0].Statements),
		"forward sources run in ascending name order")
	require.Equal(t, []string{"002_users_ix.down.sql", "001_users.down.sql"}, sourceNames(migrations[0].Down),
		"reverse sources run in descending name order, undoing the migration in reverse")
}

// TestLoadAllowsFewerReverseSourcesThanForwardOnes requires one reverse
// source to be able to undo several forward ones, so nobody writes an empty
// file to satisfy a pairing rule.
func TestLoadAllowsFewerReverseSourcesThanForwardOnes(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_initial", map[string]string{
		"001_users.up.sql":    "CREATE TABLE users (id INTEGER);\n",
		"002_users_ix.up.sql": "CREATE INDEX users_ix ON users (id);\n",
		"001_users.down.sql":  "DROP TABLE users;\n",
	})

	migrations, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, migrations[0].Statements, 2)
	require.Len(t, migrations[0].Down, 1)
}

func TestLoadRefusals(t *testing.T) {
	testCases := []struct {
		name     string
		sources  map[string]string
		expected string
	}{
		{
			name: "plain .sql source",
			sources: map[string]string{
				"001_users.sql":      "CREATE TABLE users (id INTEGER);\n",
				"001_users.down.sql": "DROP TABLE users;\n",
			},
			expected: `contains "001_users.sql", which is neither a .up.sql nor a .down.sql source`,
		},
		{
			// This is what the suffix rule buys: a misspelled reverse
			// suffix cannot become a silent extra forward source.
			name: "misspelled reverse suffix",
			sources: map[string]string{
				"001_users.up.sql":   "CREATE TABLE users (id INTEGER);\n",
				"001_users.dwon.sql": "DROP TABLE users;\n",
			},
			expected: `contains "001_users.dwon.sql", which is neither a .up.sql nor a .down.sql source`,
		},
		{
			name: "no reverse source",
			sources: map[string]string{
				"001_users.up.sql": "CREATE TABLE users (id INTEGER);\n",
			},
			expected: `migration "001_initial" has no .down.sql source; every migration must be reversible`,
		},
		{
			name: "no forward source",
			sources: map[string]string{
				"001_users.down.sql": "DROP TABLE users;\n",
			},
			expected: `migration "001_initial" has no .up.sql source`,
		},
		{
			// A reverse source naming a forward source that does not
			// exist is almost always a typo in the stem, and it would
			// otherwise run at revert time against nothing.
			name: "reverse source matching no forward source",
			sources: map[string]string{
				"001_users.up.sql":   "CREATE TABLE users (id INTEGER);\n",
				"001_users.down.sql": "DROP TABLE users;\n",
				"001_uesrs.down.sql": "DROP TABLE users;\n",
			},
			expected: `reverse source "001_uesrs.down.sql" matches no .up.sql source`,
		},
		{
			name: "blank source",
			sources: map[string]string{
				"001_users.up.sql":   "   \n",
				"001_users.down.sql": "DROP TABLE users;\n",
			},
			expected: `SQL source "001_users.up.sql" is empty`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeMigration(t, root, "001_initial", testCase.sources)
			_, err := migrationdir.Load(root)
			require.ErrorContains(t, err, testCase.expected)
		})
	}
}

func TestLoadRefusesASubdirectory(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_initial", map[string]string{
		"001_users.up.sql":   "CREATE TABLE users (id INTEGER);\n",
		"001_users.down.sql": "DROP TABLE users;\n",
	})
	require.NoError(t, os.MkdirAll(filepath.Join(root, "001_initial", "down"), 0o700))

	_, err := migrationdir.Load(root)
	require.ErrorContains(t, err, `migration "001_initial" contains subdirectory "down"`)
}

func TestLoadIgnoresDotPrefixedEntries(t *testing.T) {
	root := t.TempDir()
	writeMigration(t, root, "001_initial", map[string]string{
		"001_users.up.sql":    "CREATE TABLE users (id INTEGER);\n",
		"001_users.down.sql":  "DROP TABLE users;\n",
		".001_users.up.sql":   "nonsense",
		".001_users.down.swp": "nonsense",
	})
	require.NoError(t, os.WriteFile(filepath.Join(root, ".hidden"), []byte("nonsense"), 0o600))

	migrations, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Len(t, migrations, 1)
	require.Len(t, migrations[0].Statements, 1)
	require.Len(t, migrations[0].Down, 1)
}

func TestLoadOrdersMigrationsByName(t *testing.T) {
	root := t.TempDir()
	for _, id := range []string{"010_third", "002_second", "001_first"} {
		writeMigration(t, root, id, map[string]string{
			"001_x.up.sql":   "CREATE TABLE " + id + " (id INTEGER);\n",
			"001_x.down.sql": "DROP TABLE " + id + ";\n",
		})
	}

	migrations, err := migrationdir.Load(root)
	require.NoError(t, err)
	require.Equal(t, []string{"001_first", "002_second", "010_third"}, migrationIDs(migrations))
}

func sourceNames(statements []migrate.Statement) []string {
	names := make([]string, len(statements))
	for index, statement := range statements {
		names[index] = statement.Source
	}
	return names
}

func migrationIDs(migrations []migrate.Migration) []string {
	ids := make([]string, len(migrations))
	for index, migration := range migrations {
		ids[index] = migration.ID
	}
	return ids
}
