package rasqlmigrate

import (
	"bytes"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/stretchr/testify/require"
)

func TestRunSQLiteChangePlanFlow(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.sqlite")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	plan := emptySQLiteChangePlan(t, database)
	require.NoError(t, database.Close())
	planFile := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, changeplan.Write(planFile, plan))

	var output, diagnostics bytes.Buffer
	require.NoError(t, Run([]string{"plan", "-file", planFile}, &output, &diagnostics))
	require.Equal(t, "plan\t"+plan.ID().String()+"\n", output.String())
	require.Empty(t, diagnostics.String())

	output.Reset()
	require.NoError(t, RunLegacy([]string{"plan", "check", "-file", planFile, "-dialect", "sqlite", "-dsn", dsn}, &output))
	require.Equal(t, "plan\t"+plan.ID().String()+"\nnext-operation\t0\ncatalog-digest\t"+
		plan.Baseline().Catalog().CatalogDigest().String()+"\ncomplete\ttrue\n", output.String())

	output.Reset()
	require.NoError(t, Run([]string{"apply", "-plan", planFile, "-dialect", "sqlite", "-dsn", dsn}, &output, &diagnostics))
	require.Equal(t, "migration plan apply completed: 0 applied\n", output.String())

	output.Reset()
	require.NoError(t, RunLegacy([]string{"apply", "-plan", planFile, "-dialect", "sqlite", "-dsn", dsn}, &output))
	require.Equal(t, "migration plan apply completed: 0 applied\n", output.String())
}

func TestRunChangePlanRejectsDirectoryOnlyFlags(t *testing.T) {
	setCommandOutput(t)
	err := run([]string{"apply", "-plan", "plan.json", "-dialect", "sqlite", "-dsn", "unused", "-dry-run=false"})
	require.EqualError(t, err, "apply -dry-run is valid only with -dir")
}

func TestRunChangePlanReadsFileBeforeOpeningDatabase(t *testing.T) {
	setCommandOutput(t)
	originalOpen := openDatabase
	t.Cleanup(func() { openDatabase = originalOpen })
	opened := false
	openDatabase = func(string, string) (*sql.DB, error) {
		opened = true
		return nil, errors.New("database must not be opened")
	}
	err := run([]string{"plan", "check", "-file", filepath.Join(t.TempDir(), "missing.json"),
		"-dialect", "sqlite", "-dsn", "secret"})
	require.ErrorContains(t, err, "read migration plan")
	require.False(t, opened)
}

func emptySQLiteChangePlan(t *testing.T, database *sql.DB) changeplan.Plan {
	t.Helper()
	profile, err := engineprofile.Discover(t.Context(), database, engineprofile.SQLite, "sqlite-3.35")
	require.NoError(t, err)
	adapted := cliPlanProfile{profile}
	catalog, err := changeplan.NewCatalog(adapted, "cli-empty-plan", []changeplan.CatalogObject{})
	require.NoError(t, err)
	profileDigest, err := changeplan.ProfileDigest(adapted)
	require.NoError(t, err)
	catalogDigest, err := changeplan.CatalogDigest(catalog)
	require.NoError(t, err)
	identity, err := changeplan.NewCatalogIdentity(changeplan.SQLiteEngine, profileDigest, catalogDigest, changeplan.Digest{1})
	require.NoError(t, err)
	baseline, err := changeplan.NewBaselineIdentity(identity, catalog.SourceIdentity(), nil, nil)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "rasql_schema_migrations")
	require.NoError(t, err)
	plan, err := changeplan.NewPlan(adapted, baseline, history, nil, nil)
	require.NoError(t, err)
	return plan
}

type cliPlanProfile struct{ value engineprofile.Profile }

func (p cliPlanProfile) ID() string                                  { return p.value.ID }
func (p cliPlanProfile) Engine() changeplan.EngineID                 { return p.value.Engine }
func (p cliPlanProfile) Version() changeplan.EngineVersion           { return p.value.Version }
func (p cliPlanProfile) Capabilities() changeplan.EngineCapabilities { return p.value.Capabilities }
func (p cliPlanProfile) Limits() changeplan.EngineLimits             { return p.value.Limits }
