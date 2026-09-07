package sqlite_test

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/stretchr/testify/require"
)

func TestSchemaEvolutionSQLiteRefusesTemporaryNameExhaustion(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT NOT NULL);")
	names := make([]string, 1000)
	for index := range names {
		if index == 0 {
			names[index] = "tasks"
		} else {
			name := "tasks__rasql_rebuild"
			if index > 1 {
				name = fmt.Sprintf("tasks__rasql_rebuild_%d", index)
			}
			names[index] = name
		}
	}
	var err error
	baseline, err = analyzer.AttachLiveCatalog(baseline, sqlite.LiveCatalogFacts{ObjectNames: names})
	require.NoError(t, err)
	_, err = analyzer.Diff(baseline, target)
	require.ErrorContains(t, err, "no available rebuild temporary name")
}

func TestSchemaEvolutionSQLiteOfflineUnknownFactsRefusePublication(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, owner_label TEXT NOT NULL);")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Operations, 1)
	require.Len(t, plan.Decisions, 1)
	require.Empty(t, plan.Statements)
	require.False(t, plan.Executable())
	resolved, err := plan.Resolve(diff.Resolution{DecisionID: plan.Decisions[0].ID, BackfillSQL: "UPDATE tasks SET owner_label = 'owner';"})
	require.ErrorContains(t, err, "live trigger and view dependency inspection")
	require.Empty(t, resolved.Statements)
	root := t.TempDir()
	output := filepath.Join(root, "missing", "001_offline")
	require.ErrorContains(t, diff.WriteMigration(output, plan), "unresolved decisions")
	_, statErr := os.Stat(filepath.Dir(output))
	require.Error(t, statErr)
}

func TestSchemaEvolutionSQLiteOfflineUnknownFactsWithoutDecisionsRefusePublication(t *testing.T) {
	analyzer := sqlite.New()
	baseline := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, label TEXT);")
	target := parseSnapshot(t, analyzer, "CREATE TABLE tasks (id INTEGER PRIMARY KEY, name TEXT, label TEXT UNIQUE);")
	plan, err := analyzer.Diff(baseline, target)
	require.NoError(t, err)
	require.Len(t, plan.Operations, 1)
	require.Empty(t, plan.Decisions)
	require.Empty(t, plan.Statements)
	require.False(t, plan.Executable())
	root := t.TempDir()
	output := filepath.Join(root, "missing", "001_offline")
	require.ErrorContains(t, diff.WriteMigration(output, plan), "plan is not executable")
	_, statErr := os.Stat(filepath.Dir(output))
	require.Error(t, statErr)
}
