package conformance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestConformanceSQLite(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, SeedDatabaseForEngine(t.Context(), database, "sqlite"))
	engine, ok := EngineByName("sqlite")
	require.True(t, ok)
	rawRoot, err := rasql.New(database, engine.Dialect)
	require.NoError(t, err)
	serverVersion, err := conformanceServerVersion(t.Context(), database, engine.Name)
	require.NoError(t, err)
	environment := EnvironmentSnapshot(CommitFromEnvironment(), serverVersion, ModuleVersion("modernc.org/sqlite"), "file::memory:")
	runCanonicalWorkloads(t, engine, database, rawRoot, nil, environment)
}

func conformanceServerVersion(ctx context.Context, database *sql.DB, engine string) (string, error) {
	query := map[string]string{"sqlite": "SELECT sqlite_version()", "postgresql": "SHOW server_version", "mysql": "SELECT VERSION()"}[engine]
	if query == "" {
		return "", fmt.Errorf("unknown conformance engine %q", engine)
	}
	var version string
	if err := database.QueryRowContext(ctx, query).Scan(&version); err != nil {
		return "", fmt.Errorf("read %s server version: %w", engine, err)
	}
	return version, nil
}

func TestConformanceSignatureSelectionRejectsUnknownWorkload(t *testing.T) {
	_, err := PortableSignatureForChecked("missing-workload")
	require.Error(t, err)
}

func TestConformanceEnvironmentRequiresObservedBuildData(t *testing.T) {
	if os.Getenv(ConformanceCommitEnvVar) != "" {
		t.Skip("archive metadata is supplied by the caller")
	}
	require.Empty(t, CommitFromEnvironment())
}

func TestGeneratedOverdueCardinalitySQLite(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, SeedDatabaseForEngine(t.Context(), database, "sqlite"))
	raw, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := rasql.DiscoverEngineProfile(t.Context(), raw, "sqlite-3.35")
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(raw, profile)
	require.NoError(t, err)
	oneQuery, err := generatedOverdueQuery("sqlite", rasql.ExactlyOne, "one")
	require.NoError(t, err)
	one, err := rasql.One(t.Context(), executor, oneQuery)
	require.NoError(t, err)
	require.Equal(t, int64(1), one.ID)
	maybeQuery, err := generatedOverdueQuery("sqlite", rasql.AtMostOne, "maybe")
	require.NoError(t, err)
	_, found, err := rasql.Maybe(t.Context(), executor, maybeQuery)
	require.NoError(t, err)
	require.False(t, found)
	manyQuery, err := generatedOverdueQuery("sqlite", rasql.Many, "many")
	require.NoError(t, err)
	many, err := rasql.All(t.Context(), executor, manyQuery)
	require.NoError(t, err)
	require.Len(t, many, 2)
	wrongOne, err := generatedOverdueQuery("sqlite", rasql.ExactlyOne, "many")
	require.NoError(t, err)
	_, err = rasql.One(t.Context(), executor, wrongOne)
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	wrongMaybe, err := generatedOverdueQuery("sqlite", rasql.AtMostOne, "many")
	require.NoError(t, err)
	_, _, err = rasql.Maybe(t.Context(), executor, wrongMaybe)
	require.ErrorIs(t, err, rasql.ErrMultipleRows)
	wrongEmpty, err := generatedOverdueQuery("sqlite", rasql.ExactlyOne, "maybe")
	require.NoError(t, err)
	_, err = rasql.One(t.Context(), executor, wrongEmpty)
	require.True(t, errors.Is(err, rasql.ErrNoRows))
}
