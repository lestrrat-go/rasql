package rasqlgen

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func baseSettingsFixture() config {
	trueValue := true
	return config{
		Dialect: "postgresql", Package: "store", Output: "internal/store", Emitter: "compact", Prune: &trueValue,
		Migrations: "db/migrations",
		Tables: configTables{
			Namespaces: []string{"public"}, IncludeViews: false, Include: []string{"users"}, Exclude: []string{"secrets"},
			HistoryTable: "rasql_schema_migrations", RowNames: map[string]string{"users": "User"},
		},
		Queries: []configQuery{{
			ID: "user_by_id", Engine: "postgresql", Operation: "select", Cardinality: "one",
			Input: "queries/user_by_id.sql", Function: "UserByID", Output: "user_by_id_gen.go",
		}},
		Mappings: json.RawMessage(`{"scalars":[{"name":"money"}]}`),
	}
}

// TestSettingsDigestChangesWithEveryRecordedSetting pins one mutation per field the settings
// digest is supposed to cover, so that a field added to config later without ever being read
// here fails this test rather than silently generating the same digest for two different
// configs.
func TestSettingsDigestChangesWithEveryRecordedSetting(t *testing.T) {
	base, err := settingsDigest(baseSettingsFixture())
	require.NoError(t, err)

	mutations := map[string]func(*config){
		"dialect":                func(c *config) { c.Dialect = "mysql" },
		"package":                func(c *config) { c.Package = "other" },
		"output":                 func(c *config) { c.Output = "internal/other" },
		"emitter":                func(c *config) { c.Emitter = "other" },
		"prune":                  func(c *config) { f := false; c.Prune = &f },
		"migrations":             func(c *config) { c.Migrations = "db/other-migrations" },
		"tables.namespaces":      func(c *config) { c.Tables.Namespaces = []string{"other"} },
		"tables.include":         func(c *config) { c.Tables.Include = []string{"other"} },
		"tables.exclude":         func(c *config) { c.Tables.Exclude = []string{"other"} },
		"tables.include_views":   func(c *config) { c.Tables.IncludeViews = true },
		"tables.history_table":   func(c *config) { c.Tables.HistoryTable = "other_migrations" },
		"tables.row_names":       func(c *config) { c.Tables.RowNames = map[string]string{"users": "Person"} },
		"tables.include_objects": func(c *config) { c.Tables.IncludeObjects = append(c.Tables.IncludeObjects, schemaObjectName("s", "t")) },
		"tables.exclude_objects": func(c *config) { c.Tables.ExcludeObjects = append(c.Tables.ExcludeObjects, schemaObjectName("s", "t")) },
		"queries (function)":     func(c *config) { c.Queries[0].Function = "Other" },
		"queries (added)":        func(c *config) { c.Queries = append(c.Queries, configQuery{ID: "second"}) },
		"mappings":               func(c *config) { c.Mappings = json.RawMessage(`{"scalars":[{"name":"other"}]}`) },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			cfg := baseSettingsFixture()
			mutate(&cfg)
			got, err := settingsDigest(cfg)
			require.NoError(t, err)
			require.NotEqual(t, base, got, "%s did not change the settings digest", name)
		})
	}
}

func TestSettingsDigestIgnoresJSONWhitespaceInMappings(t *testing.T) {
	a := baseSettingsFixture()
	a.Mappings = json.RawMessage(`{"scalars":[{"name":"money"}]}`)
	b := baseSettingsFixture()
	b.Mappings = json.RawMessage("{\n  \"scalars\" : [ { \"name\": \"money\" } ]\n}\n")

	digestA, err := settingsDigest(a)
	require.NoError(t, err)
	digestB, err := settingsDigest(b)
	require.NoError(t, err)
	require.Equal(t, digestA, digestB)
}

func TestSettingsDigestDefaultsEmitterAndPrune(t *testing.T) {
	explicit := baseSettingsFixture()
	implicit := baseSettingsFixture()
	implicit.Emitter = ""
	implicit.Prune = nil

	digestExplicit, err := settingsDigest(explicit)
	require.NoError(t, err)
	digestImplicit, err := settingsDigest(implicit)
	require.NoError(t, err)
	require.Equal(t, digestExplicit, digestImplicit)
}

func TestCanonicalDialectName(t *testing.T) {
	require.Equal(t, "postgresql", canonicalDialectName("postgres"))
	require.Equal(t, "postgresql", canonicalDialectName("postgresql"))
	require.Equal(t, "postgresql", canonicalDialectName("PostgreSQL"))
	require.Equal(t, "mysql", canonicalDialectName("mysql"))
	require.Equal(t, "sqlite", canonicalDialectName("sqlite"))
	require.Equal(t, "sqlite", canonicalDialectName("sqlite3"))
}
