package rasqlgen

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func writeConfig(t *testing.T, data []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "rasql.json")
	require.NoError(t, os.WriteFile(path, data, 0o600))
	return path
}

func TestLoadConfigCanonicalSettings(t *testing.T) {
	path := writeConfig(t, []byte(`{
  "engine": {"dialect": "sqlite", "profile": "sqlite-3.35"},
  "schema": {"kind": "migrations", "identity": "app", "paths": ["migrations/*.sql"]},
  "package": "store",
  "output": "internal/store",
  "emitter": "compact",
  "prune": false,
  "tables": {"exclude": ["audit_log"], "row_names": {"users": "User"}}
}`))

	settings, err := loadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "sqlite", settings.Engine.Dialect)
	require.Equal(t, "sqlite-3.35", settings.Engine.Profile)
	require.Equal(t, "migrations", settings.Schema.Kind)
	require.Equal(t, []string{"migrations/*.sql"}, settings.Schema.Paths)
	require.Equal(t, "store", settings.Package)
	require.Equal(t, "internal/store", settings.Output)
	require.Equal(t, "compact", settings.Emitter)
	require.NotNil(t, settings.Prune)
	require.False(t, *settings.Prune)
	require.Equal(t, []string{"audit_log"}, settings.Tables.Exclude)
	require.Equal(t, "User", settings.Tables.RowNames["users"])
}

func TestLoadConfigReadLimit(t *testing.T) {
	const limit = 1 << 20
	atLimit := append([]byte("{}"), bytes.Repeat([]byte{' '}, limit-2)...)
	_, err := loadConfig(writeConfig(t, atLimit))
	require.NoError(t, err)

	pastLimit := append(atLimit, ' ')
	_, err = loadConfig(writeConfig(t, pastLimit))
	require.Error(t, err)
	require.Contains(t, err.Error(), "1048576-byte limit")
}

func TestLoadConfigRefusals(t *testing.T) {
	testCases := []struct {
		name     string
		settings string
		expected string
	}{
		{name: "unknown key", settings: `{"package":"store","rowname":{}}`, expected: `unknown field "rowname"`},
		{name: "malformed JSON", settings: `{"package":"store",}`, expected: "parse config"},
		{name: "trailing value", settings: `{}` + " " + `{}`, expected: "unexpected value after the settings object"},
		{name: "checked-in DSN", settings: `{"dsn":"postgres://user:secret@example.test/db"}`, expected: `unknown field "dsn"`},
		{name: "retired emitter", settings: `{"emitter":"legacy"}`, expected: `emitter "legacy" must be compact`},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := loadConfig(writeConfig(t, []byte(testCase.settings)))
			require.Error(t, err)
			require.Contains(t, err.Error(), testCase.expected)
			if testCase.name == "checked-in DSN" {
				require.NotContains(t, err.Error(), "secret")
			}
		})
	}
}

func TestLoadConfigExplicitPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "build", "codegen.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700))
	require.NoError(t, os.WriteFile(path, []byte(`{"package":"records"}`), 0o600))

	settings, err := loadConfig(path)
	require.NoError(t, err)
	require.Equal(t, "records", settings.Package)

	_, err = loadConfig(filepath.Join(dir, "absent.json"))
	require.Error(t, err)
	require.Contains(t, err.Error(), "read config")
}
