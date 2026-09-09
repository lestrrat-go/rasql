package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
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
			expected := literalPlanProgressDDL(engine)
			mock.ExpectExec(expected).WillReturnResult(sqlmock.NewResult(0, 0))
			require.NoError(t, store.ensure(context.Background(), database))
			catalogQuery, catalogArgs, err := store.catalogQuery()
			require.NoError(t, err)
			wantCatalogQuery := `SELECT 1 FROM "main".sqlite_master WHERE type = 'table' AND name = ?`
			switch engine {
			case engineprofile.PostgreSQL:
				wantCatalogQuery = "SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2"
			case engineprofile.MySQL:
				wantCatalogQuery = "SELECT 1 FROM information_schema.tables WHERE table_schema = ? AND table_name = ?"
			}
			require.Equal(t, wantCatalogQuery, catalogQuery)
			catalogValues := make([]driver.Value, len(catalogArgs))
			for index, argument := range catalogArgs {
				catalogValues[index] = argument
			}
			mock.ExpectQuery(catalogQuery).WithArgs(catalogValues...).WillReturnRows(sqlmock.NewRows([]string{"exists"}))
			exists, err := store.exists(context.Background(), database)
			require.NoError(t, err)
			require.False(t, exists)
			mock.ExpectQuery(expectedPlanProgressRead(engine)).WillReturnRows(sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest"}))
			entry, err := store.read(context.Background(), database)
			require.NoError(t, err)
			require.Nil(t, entry)
			currentLegacyDDL := runner.progressTableDDL()
			currentHistoryDDL, err := runner.historyTableDDL()
			require.NoError(t, err)
			require.Equal(t, legacyDDL, currentLegacyDDL)
			require.Equal(t, historyDDL, currentHistoryDDL)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func literalPlanProgressDDL(engine engineprofile.EngineID) string {
	switch engine {
	case engineprofile.PostgreSQL:
		return `CREATE TABLE IF NOT EXISTS "tenant"."rasql_schema_migrations_plan_progress" ("plan_id" VARCHAR(64) NOT NULL PRIMARY KEY, "next_index" INTEGER NOT NULL, "operation_count" INTEGER NOT NULL, "catalog_digest" CHAR(64) NOT NULL)`
	case engineprofile.MySQL:
		return "CREATE TABLE IF NOT EXISTS `tenant`.`rasql_schema_migrations_plan_progress` (`plan_id` VARCHAR(64) NOT NULL PRIMARY KEY, `next_index` INTEGER NOT NULL, `operation_count` INTEGER NOT NULL, `catalog_digest` CHAR(64) NOT NULL)"
	default:
		return `CREATE TABLE IF NOT EXISTS "main"."rasql_schema_migrations_plan_progress" ("plan_id" VARCHAR(64) NOT NULL PRIMARY KEY, "next_index" INTEGER NOT NULL, "operation_count" INTEGER NOT NULL, "catalog_digest" CHAR(64) NOT NULL)`
	}
}

func expectedPlanProgressRead(engine engineprofile.EngineID) string {
	switch engine {
	case engineprofile.PostgreSQL:
		return `SELECT "plan_id", "next_index", "operation_count", "catalog_digest" FROM "tenant"."rasql_schema_migrations_plan_progress"`
	case engineprofile.MySQL:
		return "SELECT `plan_id`, `next_index`, `operation_count`, `catalog_digest` FROM `tenant`.`rasql_schema_migrations_plan_progress`"
	default:
		return `SELECT "plan_id", "next_index", "operation_count", "catalog_digest" FROM "main"."rasql_schema_migrations_plan_progress"`
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

func TestPlanProgressReadMatrixAllEngines(t *testing.T) {
	engines := []engineprofile.EngineID{engineprofile.SQLite, engineprofile.PostgreSQL, engineprofile.MySQL}
	for _, engine := range engines {
		t.Run(dialectForEngine(engine).Name(), func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			runner := runnerFor(t, database, dialectForEngine(engine))
			prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engine, changeplan.TransactionForbidden))
			require.NoError(t, err)
			store, err := newPlanProgressStore(prepared, resolvedHistoryFor(prepared, dialectForEngine(engine).Name()))
			require.NoError(t, err)
			query := expectedPlanProgressRead(engine)
			planID := changeplan.Digest{0xab}.String()
			catalog := changeplan.Digest{0xcd}.String()
			uppercaseCatalog := strings.ToUpper(catalog)
			tests := []struct {
				name      string
				rows      func() *sqlmock.Rows
				err       error
				want      bool
				wantIndex int
				wantCount int
				wantError bool
			}{
				{name: "absent", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()) }},
				{name: "fresh", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, 0, 2, catalog) }, want: true, wantCount: 2},
				{name: "partial", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, 1, 2, catalog) }, want: true, wantIndex: 1, wantCount: 2},
				{name: "terminal", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, 2, 2, catalog) }, want: true, wantIndex: 2, wantCount: 2},
				{name: "multiple", rows: func() *sqlmock.Rows {
					return sqlmock.NewRows(progressColumns()).AddRow(planID, 0, 2, catalog).AddRow(planID, 1, 2, catalog)
				}, wantError: true},
				{name: "malformed plan", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow("bad", 0, 2, catalog) }, wantError: true},
				{name: "uppercase plan", rows: func() *sqlmock.Rows {
					return sqlmock.NewRows(progressColumns()).AddRow(strings.ToUpper(planID), 0, 2, catalog)
				}, wantError: true},
				{name: "zero plan", rows: func() *sqlmock.Rows {
					return sqlmock.NewRows(progressColumns()).AddRow(changeplan.Digest{}.String(), 0, 2, catalog)
				}, wantError: true},
				{name: "malformed catalog", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, 0, 2, "bad") }, wantError: true},
				{name: "uppercase catalog", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, 0, 2, uppercaseCatalog) }, wantError: true},
				{name: "negative count", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, 0, -1, catalog) }, wantError: true},
				{name: "negative index", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, -1, 2, catalog) }, wantError: true},
				{name: "index greater than count", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, 3, 2, catalog) }, wantError: true},
				{name: "null plan", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(nil, 0, 2, catalog) }, wantError: true},
				{name: "null index", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, nil, 2, catalog) }, wantError: true},
				{name: "null count", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, 0, nil, catalog) }, wantError: true},
				{name: "null catalog", rows: func() *sqlmock.Rows { return sqlmock.NewRows(progressColumns()).AddRow(planID, 0, 2, nil) }, wantError: true},
				{name: "index overflow", rows: func() *sqlmock.Rows {
					return sqlmock.NewRows(progressColumns()).AddRow(planID, "999999999999999999999999", 2, catalog)
				}, wantError: true},
				{name: "count overflow", rows: func() *sqlmock.Rows {
					return sqlmock.NewRows(progressColumns()).AddRow(planID, 0, "999999999999999999999999", catalog)
				}, wantError: true},
				{name: "scan failure", rows: func() *sqlmock.Rows {
					return sqlmock.NewRows(progressColumns()).AddRow(planID, "not-an-integer", 2, catalog)
				}, wantError: true},
				{name: "three fields", rows: func() *sqlmock.Rows {
					return sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count"}).AddRow(planID, 0, 2)
				}, wantError: true},
				{name: "five fields", rows: func() *sqlmock.Rows {
					return sqlmock.NewRows([]string{"plan_id", "next_index", "operation_count", "catalog_digest", "extra"}).AddRow(planID, 0, 2, catalog, "extra")
				}, wantError: true},
				{name: "rows failure", rows: func() *sqlmock.Rows {
					return sqlmock.NewRows(progressColumns()).AddRow(planID, 0, 2, catalog).RowError(0, errors.New("row failure"))
				}, wantError: true},
				{name: "query failure", err: errors.New("query failure"), wantError: true},
			}
			for _, test := range tests {
				t.Run(test.name, func(t *testing.T) {
					expectation := mock.ExpectQuery(query)
					if test.err != nil {
						expectation.WillReturnError(test.err)
					} else {
						expectation.WillReturnRows(test.rows())
					}
					entry, readErr := store.read(t.Context(), database)
					if test.want {
						require.NoError(t, readErr)
						require.NotNil(t, entry)
						require.Equal(t, changeplan.PlanID(changeplan.Digest{0xab}), entry.checkpoint.PlanID())
						require.Equal(t, test.wantIndex, entry.checkpoint.NextIndex())
						require.Equal(t, test.wantCount, entry.operationCount)
						require.Equal(t, changeplan.Digest{0xcd}, entry.checkpoint.CatalogDigest())
						return
					}
					if test.wantError {
						require.Error(t, readErr)
						return
					}
					require.NoError(t, readErr)
					require.Nil(t, entry)
				})
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func progressColumns() []string {
	return []string{"plan_id", "next_index", "operation_count", "catalog_digest"}
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

func TestPlanProgressWritesAllEnginesUseLiteralSQLAndArguments(t *testing.T) {
	for _, engine := range []engineprofile.EngineID{engineprofile.SQLite, engineprofile.PostgreSQL, engineprofile.MySQL} {
		t.Run(dialectForEngine(engine).Name(), func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			runner := runnerFor(t, database, dialectForEngine(engine))
			prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engine, changeplan.TransactionForbidden))
			require.NoError(t, err)
			store, err := newPlanProgressStore(prepared, resolvedHistoryFor(prepared, dialectForEngine(engine).Name()))
			require.NoError(t, err)
			entry := validPlanProgressEntry(t, 1, 2)
			oldPlan := entry.checkpoint.PlanID()
			newCheckpoint, err := changeplan.NewCheckpoint(changeplan.PlanID{3}, 0, changeplan.Digest{4})
			require.NoError(t, err)
			replacement := planProgressEntry{checkpoint: newCheckpoint, operationCount: 3}
			insertSQL, updateSQL, replaceSQL := literalProgressWriteSQL(engine)
			mock.ExpectExec(insertSQL).WithArgs(oldPlan.String(), 1, 2, changeplan.Digest{2}.String()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(updateSQL).WithArgs(1, 2, changeplan.Digest{2}.String(), oldPlan.String()).WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec(replaceSQL).WithArgs(newCheckpoint.PlanID().String(), 0, 3, changeplan.Digest{4}.String(), oldPlan.String()).WillReturnResult(sqlmock.NewResult(0, 1))
			require.NoError(t, store.insert(t.Context(), database, entry))
			require.NoError(t, store.update(t.Context(), database, entry))
			require.NoError(t, store.replace(t.Context(), database, oldPlan, replacement))
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func literalProgressWriteSQL(engine engineprofile.EngineID) (string, string, string) {
	switch engine {
	case engineprofile.PostgreSQL:
		return `INSERT INTO "tenant"."rasql_schema_migrations_plan_progress" ("plan_id", "next_index", "operation_count", "catalog_digest") VALUES ($1, $2, $3, $4)`,
			`UPDATE "tenant"."rasql_schema_migrations_plan_progress" SET "next_index" = $1, "operation_count" = $2, "catalog_digest" = $3 WHERE "plan_id" = $4`,
			`UPDATE "tenant"."rasql_schema_migrations_plan_progress" SET "plan_id" = $1, "next_index" = $2, "operation_count" = $3, "catalog_digest" = $4 WHERE "plan_id" = $5`
	case engineprofile.MySQL:
		return "INSERT INTO `tenant`.`rasql_schema_migrations_plan_progress` (`plan_id`, `next_index`, `operation_count`, `catalog_digest`) VALUES (?, ?, ?, ?)",
			"UPDATE `tenant`.`rasql_schema_migrations_plan_progress` SET `next_index` = ?, `operation_count` = ?, `catalog_digest` = ? WHERE `plan_id` = ?",
			"UPDATE `tenant`.`rasql_schema_migrations_plan_progress` SET `plan_id` = ?, `next_index` = ?, `operation_count` = ?, `catalog_digest` = ? WHERE `plan_id` = ?"
	default:
		return `INSERT INTO "main"."rasql_schema_migrations_plan_progress" ("plan_id", "next_index", "operation_count", "catalog_digest") VALUES (?, ?, ?, ?)`,
			`UPDATE "main"."rasql_schema_migrations_plan_progress" SET "next_index" = ?, "operation_count" = ?, "catalog_digest" = ? WHERE "plan_id" = ?`,
			`UPDATE "main"."rasql_schema_migrations_plan_progress" SET "plan_id" = ?, "next_index" = ?, "operation_count" = ?, "catalog_digest" = ? WHERE "plan_id" = ?`
	}
}

func TestPlanProgressWritesRejectValidationAndHookWithoutExec(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	runner := runnerFor(t, database, dialect.SQLite())
	prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
	require.NoError(t, err)
	store, err := newPlanProgressStore(prepared, resolvedHistoryFor(prepared, "sqlite"))
	require.NoError(t, err)
	entry := validPlanProgressEntry(t, 0, 1)
	invalid := planProgressEntry{}
	oldPlan := entry.checkpoint.PlanID()
	for _, test := range []struct {
		name string
		call func(executor) error
	}{
		{name: "insert validation", call: func(executor executor) error { return store.insert(t.Context(), executor, invalid) }},
		{name: "update validation", call: func(executor executor) error { return store.update(t.Context(), executor, invalid) }},
		{name: "replace validation", call: func(executor executor) error { return store.replace(t.Context(), executor, oldPlan, invalid) }},
		{name: "replace zero old ID", call: func(executor executor) error { return store.replace(t.Context(), executor, changeplan.PlanID{}, entry) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec := &countingExecutor{}
			require.Error(t, test.call(exec))
			require.Zero(t, exec.calls)
		})
	}
	previous := planCheckpointWriteHook
	planCheckpointWriteHook = func(string) error { return errors.New("blocked") }
	t.Cleanup(func() { planCheckpointWriteHook = previous })
	for _, test := range []struct {
		name string
		call func(executor) error
	}{
		{name: "insert hook", call: func(executor executor) error { return store.insert(t.Context(), executor, entry) }},
		{name: "update hook", call: func(executor executor) error { return store.update(t.Context(), executor, entry) }},
		{name: "replace hook", call: func(executor executor) error { return store.replace(t.Context(), executor, oldPlan, entry) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			exec := &countingExecutor{}
			require.Error(t, test.call(exec))
			require.Zero(t, exec.calls)
		})
	}
}

type countingExecutor struct {
	calls  int
	result sql.Result
	err    error
}

func (e *countingExecutor) ExecContext(context.Context, string, ...any) (sql.Result, error) {
	e.calls++
	return e.result, e.err
}

func TestPlanProgressWritesReportExecAndAffectedRowFailures(t *testing.T) {
	for _, operation := range []string{"insert", "update", "replace"} {
		for _, failure := range []struct {
			name   string
			result sql.Result
			err    error
		}{
			{name: "exec", err: errors.New("exec failed")},
			{name: "nil result"},
			{name: "rows affected", result: sqlmock.NewErrorResult(errors.New("rows affected failed"))},
			{name: "zero rows", result: sqlmock.NewResult(0, 0)},
			{name: "two rows", result: sqlmock.NewResult(0, 2)},
		} {
			if operation == "insert" && failure.name != "exec" {
				continue
			}
			t.Run(fmt.Sprintf("%s/%s", operation, failure.name), func(t *testing.T) {
				database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
				require.NoError(t, err)
				t.Cleanup(func() { _ = database.Close() })
				runner := runnerFor(t, database, dialect.SQLite())
				prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
				require.NoError(t, err)
				store, err := newPlanProgressStore(prepared, resolvedHistoryFor(prepared, "sqlite"))
				require.NoError(t, err)
				entry := validPlanProgressEntry(t, 1, 2)
				newCheckpoint, err := changeplan.NewCheckpoint(changeplan.PlanID{3}, 0, changeplan.Digest{4})
				require.NoError(t, err)
				replacement := planProgressEntry{checkpoint: newCheckpoint, operationCount: 3}
				insertSQL, updateSQL, replaceSQL := literalProgressWriteSQL(engineprofile.SQLite)
				query := map[string]string{"insert": insertSQL, "update": updateSQL, "replace": replaceSQL}[operation]
				expectation := mock.ExpectExec(query)
				if failure.err != nil {
					expectation.WillReturnError(failure.err)
				} else {
					expectation.WillReturnResult(failure.result)
				}
				var callErr error
				switch operation {
				case "insert":
					callErr = store.insert(t.Context(), database, entry)
				case "update":
					callErr = store.update(t.Context(), database, entry)
				default:
					callErr = store.replace(t.Context(), database, entry.checkpoint.PlanID(), replacement)
				}
				require.Error(t, callErr)
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

func TestPlanProgressSQLiteCRUDSchemaAndTransactions(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	runner := runnerFor(t, database, dialect.SQLite())
	prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
	require.NoError(t, err)
	preparedBefore := prepared
	connection, err := database.Conn(t.Context())
	require.NoError(t, err)
	history, err := resolvePlanHistory(t.Context(), connection, prepared)
	require.NoError(t, err)
	require.NoError(t, connection.Close())
	require.Equal(t, preparedBefore, prepared)
	store, err := newPlanProgressStore(prepared, history)
	require.NoError(t, err)

	var tableCount int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM main.sqlite_master WHERE type = 'table'").Scan(&tableCount))
	require.Zero(t, tableCount)
	exists, err := store.exists(t.Context(), database)
	require.NoError(t, err)
	require.False(t, exists)
	legacyEntry, err := readLegacyProgressIfExists(t.Context(), runner, database, history)
	require.NoError(t, err)
	require.Nil(t, legacyEntry)
	require.NoError(t, store.ensure(t.Context(), database))

	var createSQL string
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT sql FROM main.sqlite_master WHERE name = ?", store.table).Scan(&createSQL))
	require.Equal(t, `CREATE TABLE "rasql_schema_migrations_plan_progress" ("plan_id" VARCHAR(64) NOT NULL PRIMARY KEY, "next_index" INTEGER NOT NULL, "operation_count" INTEGER NOT NULL, "catalog_digest" CHAR(64) NOT NULL)`, createSQL)
	rows, err := database.QueryContext(t.Context(), `PRAGMA table_info("rasql_schema_migrations_plan_progress")`)
	require.NoError(t, err)
	type column struct {
		cid     int
		name    string
		typeSQL string
		notNull int
		primary int
	}
	var columns []column
	for rows.Next() {
		var value column
		var defaultValue any
		require.NoError(t, rows.Scan(&value.cid, &value.name, &value.typeSQL, &value.notNull, &defaultValue, &value.primary))
		columns = append(columns, value)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, []column{
		{cid: 0, name: "plan_id", typeSQL: "VARCHAR(64)", notNull: 1, primary: 1},
		{cid: 1, name: "next_index", typeSQL: "INTEGER", notNull: 1},
		{cid: 2, name: "operation_count", typeSQL: "INTEGER", notNull: 1},
		{cid: 3, name: "catalog_digest", typeSQL: "CHAR(64)", notNull: 1},
	}, columns)

	oldCheckpoint, err := changeplan.NewCheckpoint(changeplan.PlanID(changeplan.Digest{0xab}), 0, changeplan.Digest{0xcd})
	require.NoError(t, err)
	old := planProgressEntry{checkpoint: oldCheckpoint, operationCount: 2}
	require.NoError(t, store.insert(t.Context(), database, old))
	assertPlanProgressRow(t, database, store, old)
	updatedCheckpoint, err := changeplan.NewCheckpoint(oldCheckpoint.PlanID(), 1, changeplan.Digest{0xef})
	require.NoError(t, err)
	updated := planProgressEntry{checkpoint: updatedCheckpoint, operationCount: 2}
	require.NoError(t, store.update(t.Context(), database, updated))
	assertPlanProgressRow(t, database, store, updated)

	newCheckpoint, err := changeplan.NewCheckpoint(changeplan.PlanID(changeplan.Digest{0x12}), 0, changeplan.Digest{0x34})
	require.NoError(t, err)
	replacement := planProgressEntry{checkpoint: newCheckpoint, operationCount: 3}
	transaction, err := database.BeginTx(t.Context(), nil)
	require.NoError(t, err)
	require.NoError(t, store.replace(t.Context(), transaction, oldCheckpoint.PlanID(), replacement))
	require.NoError(t, transaction.Rollback())
	assertPlanProgressRow(t, database, store, updated)
	require.NoError(t, store.replace(t.Context(), database, updatedCheckpoint.PlanID(), replacement))
	assertPlanProgressRow(t, database, store, replacement)

	conflictCheckpoint, err := changeplan.NewCheckpoint(changeplan.PlanID(changeplan.Digest{0x56}), 2, changeplan.Digest{0x78})
	require.NoError(t, err)
	conflict := planProgressEntry{checkpoint: conflictCheckpoint, operationCount: 4}
	require.NoError(t, store.insert(t.Context(), database, conflict))
	require.Error(t, store.replace(t.Context(), database, replacement.checkpoint.PlanID(), conflict))
	assertPlanProgressRow(t, database, store, replacement)
	assertPlanProgressRow(t, database, store, conflict)
}

func assertPlanProgressRow(t *testing.T, database *sql.DB, store planProgressStore, want planProgressEntry) {
	t.Helper()
	query := "SELECT " + store.columnSQL.quotedPlanID + ", " + store.columnSQL.quotedNextIndex + ", " + store.columnSQL.quotedCount + ", " + store.columnSQL.quotedCatalogHash + " FROM " + store.tableSQL + " WHERE " + store.columnSQL.quotedPlanID + " = ?"
	var planID, catalog string
	var nextIndex, operationCount int
	require.NoError(t, database.QueryRowContext(t.Context(), query, want.checkpoint.PlanID().String()).Scan(&planID, &nextIndex, &operationCount, &catalog))
	require.Equal(t, want.checkpoint.PlanID().String(), planID)
	require.Equal(t, want.checkpoint.NextIndex(), nextIndex)
	require.Equal(t, want.operationCount, operationCount)
	require.Equal(t, want.checkpoint.CatalogDigest().String(), catalog)
}

func TestCheckpointCodecRemainsThreeFields(t *testing.T) {
	checkpoint, err := changeplan.NewCheckpoint(changeplan.PlanID(changeplan.Digest{0xab}), 1, changeplan.Digest{0xcd})
	require.NoError(t, err)
	const literal = "{\"plan_id\":\"ab00000000000000000000000000000000000000000000000000000000000000\",\"next_index\":1,\"catalog_digest\":\"cd00000000000000000000000000000000000000000000000000000000000000\"}\n"
	encoded, err := changeplan.EncodeCheckpoint(checkpoint)
	require.NoError(t, err)
	require.Equal(t, literal, string(encoded))
	decoded, err := changeplan.DecodeCheckpoint([]byte(literal))
	require.NoError(t, err)
	require.Equal(t, checkpoint, decoded)
	encodedAfter, err := changeplan.EncodeCheckpoint(decoded)
	require.NoError(t, err)
	require.Equal(t, literal, string(encodedAfter))
	require.NotContains(t, string(encodedAfter), "operation_count")
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
