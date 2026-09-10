package rasqlmigrate

import (
	"bytes"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
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

func TestRunSQLiteNonemptyChangePlanFlow(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.sqlite")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	plan := sqliteCreateTableChangePlan(t, database, dsn)
	require.NoError(t, database.Close())
	planFile := filepath.Join(t.TempDir(), "plan.json")
	require.NoError(t, changeplan.Write(planFile, plan))

	var output, diagnostics bytes.Buffer
	require.NoError(t, Run([]string{"plan", "-file", planFile}, &output, &diagnostics))
	require.Equal(t, "plan\t"+plan.ID().String()+"\noperation\t0\tcreate-users\tcreate_table\tengine_default\n",
		output.String())
	require.Empty(t, diagnostics.String())

	output.Reset()
	require.NoError(t, Run([]string{"plan", "check", "-file", planFile, "-dialect", "sqlite", "-dsn", dsn},
		&output, &diagnostics))
	require.Equal(t, "plan\t"+plan.ID().String()+"\nnext-operation\t0\ncatalog-digest\t"+
		plan.Baseline().Catalog().CatalogDigest().String()+"\ncomplete\tfalse\n", output.String())

	output.Reset()
	require.NoError(t, Run([]string{"apply", "-plan", planFile, "-dialect", "sqlite", "-dsn", dsn},
		&output, &diagnostics))
	require.Equal(t, "applied-operation\t0\tcreate-users\nmigration plan apply completed: 1 applied\n", output.String())

	database, err = sql.Open("sqlite", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	var tables int
	require.NoError(t, database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'users'").Scan(&tables))
	require.Equal(t, 1, tables)
	require.NoError(t, database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = 'rasql_schema_migrations'").Scan(&tables))
	require.Zero(t, tables)

	output.Reset()
	require.NoError(t, RunLegacy([]string{"apply", "-plan", planFile, "-dialect", "sqlite", "-dsn", dsn}, &output))
	require.Equal(t, "migration plan apply completed: 0 applied\n", output.String())
}

func TestRunChangePlanRejectsDirectoryOnlyFlags(t *testing.T) {
	setCommandOutput(t)
	err := run([]string{"apply", "-plan", "plan.json", "-dialect", "sqlite", "-dsn", "unused", "-dry-run=false"})
	require.EqualError(t, err, "apply -dry-run is valid only with -dir")
}

func TestRunChangePlanSelectorFlags(t *testing.T) {
	setCommandOutput(t)
	tests := []struct {
		name string
		args []string
		want string
	}{
		{name: "plan missing", args: []string{"plan"}, want: "plan requires exactly one of -dir and -file"},
		{name: "plan conflict", args: []string{"plan", "-dir", "migrations", "-file", "plan.json"}, want: "plan requires exactly one of -dir and -file"},
		{name: "plan duplicate file", args: []string{"plan", "-file", "first.json", "-file", "second.json"}, want: "-file provided more than once"},
		{name: "plan empty file", args: []string{"plan", "-file="}, want: "plan -file must not be empty"},
		{name: "plan positional", args: []string{"plan", "-dir", "migrations", "extra"}, want: "plan accepts no positional arguments"},
		{name: "apply missing", args: []string{"apply"}, want: "apply requires exactly one of -dir and -plan"},
		{name: "apply conflict", args: []string{"apply", "-dir", "migrations", "-plan", "plan.json"}, want: "apply requires exactly one of -dir and -plan"},
		{name: "apply duplicate plan", args: []string{"apply", "-plan", "first.json", "-plan", "second.json"}, want: "-plan provided more than once"},
		{name: "apply empty plan", args: []string{"apply", "-plan="}, want: "apply -plan must not be empty"},
		{name: "apply positional", args: []string{"apply", "-dir", "migrations", "extra"}, want: "apply accepts no positional arguments"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			require.ErrorContains(t, run(test.args), test.want)
		})
	}
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
	missing := filepath.Join(t.TempDir(), "missing.json")
	for _, args := range [][]string{
		{"plan", "check", "-file", missing, "-dialect", "sqlite", "-dsn", "secret"},
		{"plan", "check", "-dialect", "sqlite", "-dsn", "secret", "-file", missing},
	} {
		err := run(args)
		require.ErrorContains(t, err, "read migration plan")
		require.False(t, opened)
	}
}

func emptySQLiteChangePlan(t *testing.T, database *sql.DB) changeplan.Plan {
	t.Helper()
	profile, err := engineprofile.Discover(t.Context(), database, engineprofile.SQLite, "sqlite-3.35")
	require.NoError(t, err)
	adapted := changePlanProfile{profile}
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

func sqliteCreateTableChangePlan(t *testing.T, database *sql.DB, dsn string) changeplan.Plan {
	t.Helper()
	profile, err := engineprofile.Discover(t.Context(), database, engineprofile.SQLite, "sqlite-3.35")
	require.NoError(t, err)
	adapted := changePlanProfile{profile}
	sourceIdentity := "cli-create-table"

	// The database is empty at this point, so the baseline catalog built
	// from it is empty too; it is still built the way plan create builds a
	// live baseline, through compilerir.AssignObjectIDs and
	// changeplan.NewCatalogFromPhysical, rather than from a compiler lock.
	beforeRead, err := catalogread.Read(t.Context(), database, profile, catalogread.Scope{})
	require.NoError(t, err)
	require.Empty(t, beforeRead.Tables)
	beforePhysical, diagnostics := compilerir.PhysicalFromTableDefs(changePlanEngineIdentity(profile), beforeRead.Tables)
	require.Empty(t, diagnostics)
	assignedBefore, diagnostics := compilerir.AssignObjectIDs(beforePhysical, compilerir.IdentityInput{SourceIdentity: sourceIdentity})
	require.Empty(t, diagnostics)
	baseline, err := changeplan.NewCatalogFromPhysical(assignedBefore, sourceIdentity)
	require.NoError(t, err)

	const createSQL = "CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY)"
	_, err = database.ExecContext(t.Context(), createSQL)
	require.NoError(t, err)
	afterRead, err := catalogread.Read(t.Context(), database, profile, catalogread.Scope{})
	require.NoError(t, err)
	require.Len(t, afterRead.Tables, 1)
	operationID := changeplan.OperationID("create-users")
	introduced, err := changeplan.NewIntroducedBaselineObject(sourceIdentity, operationID, afterRead.Tables[0])
	require.NoError(t, err)
	afterObject, err := changeplan.NewCatalogObject(introduced.ID(), afterRead.Tables[0])
	require.NoError(t, err)
	after, err := changeplan.NewCatalogLike(baseline, []changeplan.CatalogObject{afterObject})
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), "DROP TABLE users")
	require.NoError(t, err)

	afterDigest, err := changeplan.CatalogDigest(after)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "rasql_schema_migrations")
	require.NoError(t, err)
	operation, err := changeplan.NewOperation(
		operationID,
		changeplan.OperationCreateTable,
		nil,
		[]changeplan.ObjectID{introduced.ID()},
		nil,
		nil,
		afterDigest,
		[]stmt.Statement{stmt.New(sqltext.Text(createSQL))},
		changeplan.TransactionEngineDefault,
		false,
		nil,
	)
	require.NoError(t, err)
	step, err := changeplan.NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	resolved, err := changeplan.NewResolvedChanges(
		baseline,
		[]changeplan.ResolvedCatalogStep{step},
		[]changeplan.Decision{},
		[]changeplan.Operation{operation},
		[]changeplan.BaselineObject{introduced},
		[]changeplan.BaselineRename{},
	)
	require.NoError(t, err)
	plan, err := changeplan.FromBaseline(baseline, adapted, history, resolved)
	require.NoError(t, err)
	return plan
}
