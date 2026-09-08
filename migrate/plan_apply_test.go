package migrate

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteChangePlanCheckApplyAndReapply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "plan.sqlite")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, createPlanFixtureTable(t, database, false))
	plan := sqliteAddColumnPlan(t, database, changeplan.TransactionRequired)
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)

	check, err := runner.CheckChangePlan(t.Context(), plan)
	require.NoError(t, err)
	require.Equal(t, plan.ID(), check.PlanID())
	require.Equal(t, 0, check.NextOperationIndex())
	require.False(t, check.Complete())
	require.False(t, sqliteTableExists(t, database, defaultHistoryTable+"_plan_progress"))

	result, err := runner.ApplyChangePlan(t.Context(), plan)
	require.NoError(t, err)
	require.Len(t, result.CompletedOperations, 1)
	require.Equal(t, changeplan.OperationID("add-name"), result.CompletedOperations[0].ID())
	require.Nil(t, result.IncompleteOperation)
	require.Equal(t, 2, sqliteColumnCount(t, database, "users"))
	require.False(t, sqliteTableExists(t, database, defaultHistoryTable))

	check, err = runner.CheckChangePlan(t.Context(), plan)
	require.NoError(t, err)
	require.Equal(t, 1, check.NextOperationIndex())
	require.True(t, check.Complete())

	result, err = runner.ApplyChangePlan(t.Context(), plan)
	require.NoError(t, err)
	require.Empty(t, result.CompletedOperations)
	require.Equal(t, 2, sqliteColumnCount(t, database, "users"))
}

func TestSQLiteForbiddenChangePlanUsesDurableJournal(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "plan.sqlite")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, createPlanFixtureTable(t, database, false))
	plan := sqliteAddColumnPlan(t, database, changeplan.TransactionForbidden)
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)

	result, err := runner.ApplyChangePlan(t.Context(), plan)
	require.NoError(t, err)
	require.Len(t, result.CompletedOperations, 1)
	require.Equal(t, 2, sqliteColumnCount(t, database, "users"))
	require.True(t, sqliteTableExists(t, database, defaultHistoryTable+"_progress"))
	var progressRows int
	require.NoError(t, database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM "+defaultHistoryTable+"_progress").Scan(&progressRows))
	require.Zero(t, progressRows)
	matches, err := filepath.Glob(filepath.Join(directory, ".rasql-plan-lock-*"))
	require.NoError(t, err)
	require.Len(t, matches, 1)
}

func TestSQLiteForbiddenChangePlanRecoversExecutedStatement(t *testing.T) {
	path := filepath.Join(t.TempDir(), "recovery.sqlite")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, createPlanFixtureTable(t, database, false))
	plan := sqliteAddColumnPlan(t, database, changeplan.TransactionForbidden)
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)

	checkpointFailure := errors.New("checkpoint interrupted")
	originalHook := journalWriteHook
	journalWriteHook = func(stage string) error {
		if stage == "checkpoint" {
			return checkpointFailure
		}
		return nil
	}
	t.Cleanup(func() { journalWriteHook = originalHook })
	result, err := runner.ApplyChangePlan(t.Context(), plan)
	require.ErrorIs(t, err, checkpointFailure)
	require.NotNil(t, result.IncompleteOperation)
	require.Equal(t, ChangePlanOperationUnknown, result.IncompleteOperation.Certainty())
	require.Equal(t, 2, sqliteColumnCount(t, database, "users"))

	journalWriteHook = originalHook
	result, err = runner.ApplyChangePlan(t.Context(), plan)
	require.NoError(t, err)
	require.Len(t, result.CompletedOperations, 1)
	require.Equal(t, changeplan.OperationID("add-name"), result.CompletedOperations[0].ID())
	require.Equal(t, 2, sqliteColumnCount(t, database, "users"))
	var progressRows int
	require.NoError(t, database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM "+defaultHistoryTable+"_progress").Scan(&progressRows))
	require.Zero(t, progressRows)
}

func TestSQLiteForbiddenChangePlanResumesAfterDurableStatementCheckpoint(t *testing.T) {
	path := filepath.Join(t.TempDir(), "checkpoint.sqlite")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, createPlanFixtureTable(t, database, false))
	plan := sqliteTwoStatementForbiddenPlan(t, database)
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)

	intentFailure := errors.New("stop before second intent")
	originalHook := journalWriteHook
	intentCalls := 0
	journalWriteHook = func(stage string) error {
		if stage == "intent" {
			intentCalls++
			if intentCalls == 2 {
				return intentFailure
			}
		}
		return nil
	}
	t.Cleanup(func() { journalWriteHook = originalHook })

	result, err := runner.ApplyChangePlan(t.Context(), plan)
	require.ErrorIs(t, err, intentFailure)
	require.NotNil(t, result.IncompleteOperation)
	require.Equal(t, 2, sqliteColumnCount(t, database, "users"))

	journalWriteHook = originalHook
	result, err = runner.ApplyChangePlan(t.Context(), plan)
	require.NoError(t, err)
	require.Len(t, result.CompletedOperations, 1)
	require.Equal(t, 3, sqliteColumnCount(t, database, "users"))
}

func TestSQLiteRequiredChangePlanRollsBackWholeGroup(t *testing.T) {
	path := filepath.Join(t.TempDir(), "required-rollback.sqlite")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, createPlanFixtureTable(t, database, false))
	plan := sqliteRequiredRollbackPlan(t, database)
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)

	result, err := runner.ApplyChangePlan(t.Context(), plan)
	require.Error(t, err)
	require.Empty(t, result.CompletedOperations)
	require.NotNil(t, result.IncompleteOperation)
	require.Equal(t, changeplan.OperationID("add-email"), result.IncompleteOperation.Operation().ID())
	require.Equal(t, ChangePlanStageStatement, result.IncompleteOperation.Stage())
	require.Equal(t, ChangePlanOperationRolledBack, result.IncompleteOperation.Certainty())
	require.Len(t, result.IncompleteOperation.AffectedOperations(), 2)
	require.Equal(t, 1, sqliteColumnCount(t, database, "users"))
	require.False(t, sqliteTableExists(t, database, defaultHistoryTable+"_plan_progress"))
}

func TestSQLiteChangePlanDriftRunsNoOperationSQL(t *testing.T) {
	path := filepath.Join(t.TempDir(), "drift.sqlite")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, createPlanFixtureTable(t, database, false))
	plan := sqliteAddColumnPlan(t, database, changeplan.TransactionRequired)
	_, err = database.ExecContext(t.Context(), "CREATE TABLE rogue (id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)

	_, err = runner.CheckChangePlan(t.Context(), plan)
	require.Error(t, err)
	result, err := runner.ApplyChangePlan(t.Context(), plan)
	require.Error(t, err)
	require.Empty(t, result.CompletedOperations)
	require.Nil(t, result.IncompleteOperation)
	require.Equal(t, 1, sqliteColumnCount(t, database, "users"))
	require.False(t, sqliteTableExists(t, database, defaultHistoryTable+"_plan_progress"))
}

func TestSQLiteForbiddenChangePlanSerializesConcurrentApply(t *testing.T) {
	path := filepath.Join(t.TempDir(), "concurrent.sqlite")
	database, err := sql.Open("sqlite", path)
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	require.NoError(t, createPlanFixtureTable(t, database, false))
	plan := sqliteAddColumnPlan(t, database, changeplan.TransactionForbidden)
	runner, err := New(database, dialect.SQLite())
	require.NoError(t, err)

	type applyResult struct {
		result ExecutionResult
		err    error
	}
	start := make(chan struct{})
	results := make(chan applyResult, 2)
	var ready sync.WaitGroup
	ready.Add(2)
	for range 2 {
		go func() {
			ready.Done()
			<-start
			result, err := runner.ApplyChangePlan(t.Context(), plan)
			results <- applyResult{result: result, err: err}
		}()
	}
	ready.Wait()
	close(start)
	first, second := <-results, <-results
	require.NoError(t, first.err)
	require.NoError(t, second.err)
	require.Equal(t, 1, len(first.result.CompletedOperations)+len(second.result.CompletedOperations))
	require.Equal(t, 2, sqliteColumnCount(t, database, "users"))
}

func sqliteAddColumnPlan(t *testing.T, database *sql.DB, mode changeplan.TransactionMode) changeplan.Plan {
	t.Helper()
	profile, err := engineprofile.Discover(t.Context(), database, engineprofile.SQLite, "sqlite-3.35")
	require.NoError(t, err)
	baselineResult, err := catalogread.Read(t.Context(), database, profile, catalogread.Scope{})
	require.NoError(t, err)
	baseline := catalogFromPlanRead(t, profile, baselineResult)

	afterDatabase, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "after.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, afterDatabase.Close()) })
	require.NoError(t, createPlanFixtureTable(t, afterDatabase, true))
	afterResult, err := catalogread.Read(t.Context(), afterDatabase, profile, catalogread.Scope{})
	require.NoError(t, err)
	after := catalogFromPlanRead(t, profile, afterResult)

	profileDigest, err := changeplan.ProfileDigest(planProfileAdapter{profile})
	require.NoError(t, err)
	baselineDigest, err := changeplan.CatalogDigest(baseline)
	require.NoError(t, err)
	afterDigest, err := changeplan.CatalogDigest(after)
	require.NoError(t, err)
	identity, err := changeplan.NewCatalogIdentity(changeplan.SQLiteEngine, profileDigest, baselineDigest, changeplan.Digest{1})
	require.NoError(t, err)
	object, err := changeplan.NewBaselineObject("users", "table", "main", "users")
	require.NoError(t, err)
	baselineIdentity, err := changeplan.NewBaselineIdentity(identity, "sqlite-plan-source", []changeplan.BaselineObject{object}, nil)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", defaultHistoryTable)
	require.NoError(t, err)
	operation, err := changeplan.NewOperation("add-name", changeplan.OperationAddColumn, nil,
		[]changeplan.ObjectID{"users"}, nil, nil, afterDigest,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users ADD COLUMN name TEXT"))},
		mode, false, nil)
	require.NoError(t, err)
	plan, err := changeplan.NewPlan(planProfileAdapter{profile}, baselineIdentity, history, nil, []changeplan.Operation{operation})
	require.NoError(t, err)
	return plan
}

func sqliteTwoStatementForbiddenPlan(t *testing.T, database *sql.DB) changeplan.Plan {
	t.Helper()
	profile, err := engineprofile.Discover(t.Context(), database, engineprofile.SQLite, "sqlite-3.35")
	require.NoError(t, err)
	baselineResult, err := catalogread.Read(t.Context(), database, profile, catalogread.Scope{})
	require.NoError(t, err)
	baseline := catalogFromPlanRead(t, profile, baselineResult)
	after := sqlitePlanCatalogForSchema(t, profile,
		"CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY, name TEXT, email TEXT)")

	profileDigest, err := changeplan.ProfileDigest(planProfileAdapter{profile})
	require.NoError(t, err)
	baselineDigest, err := changeplan.CatalogDigest(baseline)
	require.NoError(t, err)
	afterDigest, err := changeplan.CatalogDigest(after)
	require.NoError(t, err)
	identity, err := changeplan.NewCatalogIdentity(
		changeplan.SQLiteEngine, profileDigest, baselineDigest, changeplan.Digest{1},
	)
	require.NoError(t, err)
	object, err := changeplan.NewBaselineObject("users", "table", "main", "users")
	require.NoError(t, err)
	baselineIdentity, err := changeplan.NewBaselineIdentity(
		identity, "sqlite-plan-source", []changeplan.BaselineObject{object}, nil,
	)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", defaultHistoryTable)
	require.NoError(t, err)
	operation, err := changeplan.NewOperation(
		"add-fields",
		changeplan.OperationAddColumn,
		nil,
		[]changeplan.ObjectID{"users"},
		nil,
		nil,
		afterDigest,
		[]stmt.Statement{
			stmt.New(sqltext.Text("ALTER TABLE users ADD COLUMN name TEXT")),
			stmt.New(sqltext.Text("ALTER TABLE users ADD COLUMN email TEXT")),
		},
		changeplan.TransactionForbidden,
		false,
		nil,
	)
	require.NoError(t, err)
	plan, err := changeplan.NewPlan(
		planProfileAdapter{profile}, baselineIdentity, history, nil, []changeplan.Operation{operation},
	)
	require.NoError(t, err)
	return plan
}

func sqliteRequiredRollbackPlan(t *testing.T, database *sql.DB) changeplan.Plan {
	t.Helper()
	profile, err := engineprofile.Discover(t.Context(), database, engineprofile.SQLite, "sqlite-3.35")
	require.NoError(t, err)
	baselineResult, err := catalogread.Read(t.Context(), database, profile, catalogread.Scope{})
	require.NoError(t, err)
	baseline := catalogFromPlanRead(t, profile, baselineResult)
	afterName := sqlitePlanCatalogForSchema(t, profile, "CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY, name TEXT)")
	afterEmail := sqlitePlanCatalogForSchema(t, profile,
		"CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY, name TEXT, email TEXT)")

	profileDigest, err := changeplan.ProfileDigest(planProfileAdapter{profile})
	require.NoError(t, err)
	baselineDigest, err := changeplan.CatalogDigest(baseline)
	require.NoError(t, err)
	nameDigest, err := changeplan.CatalogDigest(afterName)
	require.NoError(t, err)
	emailDigest, err := changeplan.CatalogDigest(afterEmail)
	require.NoError(t, err)
	identity, err := changeplan.NewCatalogIdentity(changeplan.SQLiteEngine, profileDigest, baselineDigest, changeplan.Digest{1})
	require.NoError(t, err)
	object, err := changeplan.NewBaselineObject("users", "table", "main", "users")
	require.NoError(t, err)
	baselineIdentity, err := changeplan.NewBaselineIdentity(identity, "sqlite-plan-source", []changeplan.BaselineObject{object}, nil)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", defaultHistoryTable)
	require.NoError(t, err)
	addName, err := changeplan.NewOperation("add-name", changeplan.OperationAddColumn, nil,
		[]changeplan.ObjectID{"users"}, nil, nil, nameDigest,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users ADD COLUMN name TEXT"))},
		changeplan.TransactionRequired, false, nil)
	require.NoError(t, err)
	addEmail, err := changeplan.NewOperation("add-email", changeplan.OperationAddColumn,
		[]changeplan.OperationID{addName.ID()}, []changeplan.ObjectID{"users"}, nil, nil, emailDigest,
		[]stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users ADD COLUMN"))},
		changeplan.TransactionRequired, false, nil)
	require.NoError(t, err)
	plan, err := changeplan.NewPlan(planProfileAdapter{profile}, baselineIdentity, history, nil,
		[]changeplan.Operation{addName, addEmail})
	require.NoError(t, err)
	return plan
}

func sqlitePlanCatalogForSchema(t *testing.T, profile engineprofile.Profile, statement string) changeplan.Catalog {
	t.Helper()
	database, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "catalog.sqlite"))
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	_, err = database.ExecContext(t.Context(), statement)
	require.NoError(t, err)
	result, err := catalogread.Read(t.Context(), database, profile, catalogread.Scope{})
	require.NoError(t, err)
	return catalogFromPlanRead(t, profile, result)
}

func catalogFromPlanRead(t *testing.T, profile engineprofile.Profile, result catalogread.Result) changeplan.Catalog {
	t.Helper()
	require.Len(t, result.Tables, 1)
	object, err := changeplan.NewCatalogObject("users", result.Tables[0])
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalog(planProfileAdapter{profile}, "sqlite-plan-source", []changeplan.CatalogObject{object})
	require.NoError(t, err)
	return catalog
}

func createPlanFixtureTable(t *testing.T, database *sql.DB, withName bool) error {
	t.Helper()
	statement := "CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY)"
	if withName {
		statement = "CREATE TABLE users (id INTEGER NOT NULL PRIMARY KEY, name TEXT)"
	}
	_, err := database.ExecContext(t.Context(), statement)
	return err
}

func sqliteTableExists(t *testing.T, database *sql.DB, name string) bool {
	t.Helper()
	var count int
	require.NoError(t, database.QueryRowContext(t.Context(),
		"SELECT COUNT(*) FROM sqlite_master WHERE type = 'table' AND name = ?", name).Scan(&count))
	return count == 1
}

func sqliteColumnCount(t *testing.T, database *sql.DB, table string) int {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), "PRAGMA table_info("+table+")")
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	count := 0
	for rows.Next() {
		count++
	}
	require.NoError(t, rows.Err())
	return count
}
