package rasqlgen

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// writeConfigNames rewrites the fixture's rasql.json so that its tables.names block is exactly
// names, keyed the way rasql.json keys it, and leaves every other setting alone.
func writeConfigNames(t *testing.T, fixture liveWorkflowFixture, names map[string]any) {
	t.Helper()
	data, err := os.ReadFile(fixture.configPath)
	require.NoError(t, err)
	var settings map[string]any
	require.NoError(t, json.Unmarshal(data, &settings))
	settings["tables"] = map[string]any{"names": names}
	updated, err := json.Marshal(settings)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(fixture.configPath, updated, 0o600))
}

// TestGenerateReadsANameKeyQualifiedByTheConnectedNamespace generates against a real SQLite
// database and pins that both spellings of one table's names key reach it.
//
// The generated descriptor carries no namespace, because SQLite answers "main" when asked which
// database the connection is using and schemasource.Read clears that one. A key written
// "main.users" is the same table all the same, so it has to keep matching: a reader who wrote the
// qualified form would otherwise get a store with the row name they did not ask for and no
// message saying why.
func TestGenerateReadsANameKeyQualifiedByTheConnectedNamespace(t *testing.T) {
	for _, tc := range []struct {
		name string
		key  string
	}{
		{name: "qualified", key: "main.users"},
		{name: "unqualified", key: "users"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := newLiveWorkflowFixture(t)
			writeConfigNames(t, fixture, map[string]any{tc.key: map[string]any{"row_type": "Account"}})

			var output, diagnostics bytes.Buffer
			command := fixture.command(t, &output, &diagnostics)
			require.NoError(t, command.run([]string{"generate", "-config", fixture.configPath, "-scratch"}), diagnostics.String())

			files := fixture.generatedFiles(t)
			require.Contains(t, files, "users_gen.go")
			require.Contains(t, string(files["users_gen.go"]), "type AccountRow struct")
		})
	}
}

// TestGenerateRefusesTwoNameKeysForOneTable pins the refusal that replaces one key silently
// winning: on a connection using main, "main.users" and "users" address the same table, so a
// configuration stating both is a mistake the run names rather than resolves. Nothing is written.
func TestGenerateRefusesTwoNameKeysForOneTable(t *testing.T) {
	fixture := newLiveWorkflowFixture(t)
	writeConfigNames(t, fixture, map[string]any{
		"main.users": map[string]any{"row_type": "Account"},
		"users":      map[string]any{"row_type": "Person"},
	})

	var output, diagnostics bytes.Buffer
	command := fixture.command(t, &output, &diagnostics)
	err := command.run([]string{"generate", "-config", fixture.configPath, "-scratch"})
	require.Error(t, err)
	require.Contains(t, err.Error(), `"main.users"`)
	require.Contains(t, err.Error(), `"users"`)
	require.NoDirExists(t, filepath.Join(fixture.root, "internal", "store"))
}
