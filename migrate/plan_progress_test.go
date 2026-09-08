package migrate

import (
	"context"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestPlanProgressDDLExact(t *testing.T) {
	for _, engine := range []engineprofile.EngineID{engineprofile.SQLite, engineprofile.PostgreSQL, engineprofile.MySQL} {
		t.Run(dialectForEngine(engine).Name(), func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			runner := runnerFor(t, database, dialectForEngine(engine))
			plan := runnerPlan(t, runner.historyTable, engine, changeplan.TransactionForbidden)
			prepared, err := prepareChangePlan(runner, plan)
			require.NoError(t, err)
			resolved := resolvedHistoryFor(prepared, dialectForEngine(engine).Name())
			store, err := newPlanProgressStore(prepared, resolved)
			require.NoError(t, err)
			legacyDDL := runner.progressTableDDL()
			historyDDL, err := runner.historyTableDDL()
			require.NoError(t, err)
			expected := "CREATE TABLE IF NOT EXISTS " + store.tableSQL + " (" + store.columnSQL.quotedPlanID + " VARCHAR(64) NOT NULL PRIMARY KEY, " + store.columnSQL.quotedNextIndex + " INTEGER NOT NULL, " + store.columnSQL.quotedCount + " INTEGER NOT NULL, " + store.columnSQL.quotedCatalogHash + " CHAR(64) NOT NULL)"
			mock.ExpectExec(expected).WillReturnResult(sqlmock.NewResult(0, 0))
			require.NoError(t, store.ensure(context.Background(), database))
			catalogQuery, catalogArgs, err := store.catalogQuery()
			require.NoError(t, err)
			catalogValues := make([]driver.Value, len(catalogArgs))
			for index, argument := range catalogArgs {
				catalogValues[index] = argument
			}
			mock.ExpectQuery(catalogQuery).WithArgs(catalogValues...).WillReturnRows(sqlmock.NewRows([]string{"exists"}))
			exists, err := store.exists(context.Background(), database)
			require.NoError(t, err)
			require.False(t, exists)
			currentLegacyDDL := runner.progressTableDDL()
			currentHistoryDDL, err := runner.historyTableDDL()
			require.NoError(t, err)
			require.Equal(t, legacyDDL, currentLegacyDDL)
			require.Equal(t, historyDDL, currentHistoryDDL)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPlanProgressReadMatrix(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	runner := runnerFor(t, database, dialect.SQLite())
	prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
	require.NoError(t, err)
	store, err := newPlanProgressStore(prepared, resolvedHistoryFor(prepared, "sqlite"))
	require.NoError(t, err)
	query := "SELECT " + store.columnSQL.quotedPlanID + ", " + store.columnSQL.quotedNextIndex + ", " + store.columnSQL.quotedCount + ", " + store.columnSQL.quotedCatalogHash + " FROM " + store.tableSQL
	validPlan := changeplan.Digest{1}.String()
	validCatalog := changeplan.Digest{2}.String()
	tests := []struct {
		name      string
		rows      *sqlmock.Rows
		err       error
		want      bool
		wantError bool
	}{
		{name: "empty", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"})},
		{name: "valid fresh", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, 0, 2, validCatalog), want: true},
		{name: "valid partial", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, 1, 2, validCatalog), want: true},
		{name: "valid terminal", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, 2, 2, validCatalog), want: true},
		{name: "multiple", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, 0, 2, validCatalog).AddRow(validPlan, 1, 2, validCatalog), wantError: true},
		{name: "zero plan", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(changeplan.Digest{}.String(), 0, 2, validCatalog), wantError: true},
		{name: "uppercase catalog", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, 0, 2, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), wantError: true},
		{name: "negative count", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, 0, -1, validCatalog), wantError: true},
		{name: "negative index", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, -1, 2, validCatalog), wantError: true},
		{name: "index too large", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, 3, 2, validCatalog), wantError: true},
		{name: "null", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(nil, 0, 2, validCatalog), wantError: true},
		{name: "scan failure", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, "bad", 2, validCatalog), wantError: true},
		{name: "rows failure", rows: sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}).AddRow(validPlan, 0, 2, validCatalog).RowError(0, errors.New("row failure")), wantError: true},
		{name: "query failure", err: errors.New("query failure"), wantError: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			expectation := mock.ExpectQuery(query)
			if test.err != nil {
				expectation.WillReturnError(test.err)
			} else {
				expectation.WillReturnRows(test.rows)
			}
			entry, readErr := store.read(context.Background(), database)
			if test.want {
				require.NoError(t, readErr)
				require.NotNil(t, entry)
			} else if test.wantError {
				require.Error(t, readErr)
			} else {
				require.NoError(t, readErr)
				require.Nil(t, entry)
			}
		})
	}
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlanProgressWrites(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	runner := runnerFor(t, database, dialect.SQLite())
	prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
	require.NoError(t, err)
	store, err := newPlanProgressStore(prepared, resolvedHistoryFor(prepared, "sqlite"))
	require.NoError(t, err)
	entry := validPlanProgressEntry(t, 1, 2)
	oldPlan := entry.checkpoint.PlanID()
	newCheckpoint, err := changeplan.NewCheckpoint(changeplan.PlanID{3}, 0, changeplan.Digest{4})
	require.NoError(t, err)
	replacement := planProgressEntry{checkpoint: newCheckpoint, operationCount: 3}
	insertSQL := "INSERT INTO " + store.tableSQL + " (" + store.columnSQL.quotedPlanID + ", " + store.columnSQL.quotedNextIndex + ", " + store.columnSQL.quotedCount + ", " + store.columnSQL.quotedCatalogHash + ") VALUES (?, ?, ?, ?)"
	updateSQL := "UPDATE " + store.tableSQL + " SET " + store.columnSQL.quotedNextIndex + " = ?, " + store.columnSQL.quotedCount + " = ?, " + store.columnSQL.quotedCatalogHash + " = ? WHERE " + store.columnSQL.quotedPlanID + " = ?"
	replaceSQL := "UPDATE " + store.tableSQL + " SET " + store.columnSQL.quotedPlanID + " = ?, " + store.columnSQL.quotedNextIndex + " = ?, " + store.columnSQL.quotedCount + " = ?, " + store.columnSQL.quotedCatalogHash + " = ? WHERE " + store.columnSQL.quotedPlanID + " = ?"
	mock.ExpectExec(insertSQL).WithArgs(oldPlan.String(), 1, 2, changeplan.Digest{2}.String()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(updateSQL).WithArgs(1, 2, changeplan.Digest{2}.String(), oldPlan.String()).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(replaceSQL).WithArgs(newCheckpoint.PlanID().String(), 0, 3, changeplan.Digest{4}.String(), oldPlan.String()).WillReturnResult(sqlmock.NewResult(0, 1))
	require.NoError(t, store.insert(context.Background(), database, entry))
	require.NoError(t, store.update(context.Background(), database, entry))
	require.NoError(t, store.replace(context.Background(), database, oldPlan, replacement))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlanProgressWriteHookStopsWrites(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	runner := runnerFor(t, database, dialect.SQLite())
	prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
	require.NoError(t, err)
	store, err := newPlanProgressStore(prepared, resolvedHistoryFor(prepared, "sqlite"))
	require.NoError(t, err)
	previous := planCheckpointWriteHook
	planCheckpointWriteHook = func(operation string) error { return errors.New(operation + " blocked") }
	t.Cleanup(func() { planCheckpointWriteHook = previous })
	entry := validPlanProgressEntry(t, 0, 1)
	require.Error(t, store.insert(context.Background(), database, entry))
	require.Error(t, store.update(context.Background(), database, entry))
	require.Error(t, store.replace(context.Background(), database, entry.checkpoint.PlanID(), entry))
	require.NoError(t, mock.ExpectationsWereMet())
}

func validPlanProgressEntry(t *testing.T, next, count int) planProgressEntry {
	t.Helper()
	checkpoint, err := changeplan.NewCheckpoint(changeplan.PlanID{1}, next, changeplan.Digest{2})
	require.NoError(t, err)
	return planProgressEntry{checkpoint: checkpoint, operationCount: count}
}

func resolvedHistoryFor(prepared preparedChangePlan, name string) resolvedPlanHistory {
	schemaName := "main"
	if name != "sqlite" {
		schemaName = "tenant"
	}
	qualifiedSchema, _ := dialectForEngine(prepared.profile.Engine).QuoteIdentifier(schemaName)
	qualifiedTable, _ := dialectForEngine(prepared.profile.Engine).QuoteIdentifier(prepared.progressName.bareName)
	exclusionSchema := schemaName
	if name != "sqlite" {
		exclusionSchema = ""
	}
	return resolvedPlanHistory{
		schema:              schemaName,
		historyTable:        schema.ObjectName{Schema: exclusionSchema, Name: prepared.history.Table()},
		legacyProgressTable: schema.ObjectName{Schema: exclusionSchema, Name: prepared.history.Table() + "_progress"},
		planProgressTable:   schema.ObjectName{Schema: schemaName, Name: prepared.progressName.bareName},
		qualifiedPlanSQL:    qualifiedSchema + "." + qualifiedTable,
		dialectName:         name,
	}
}
