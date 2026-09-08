package migrate

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/stretchr/testify/require"
)

func TestResolvePlanHistory(t *testing.T) {
	tests := []struct {
		name       string
		engine     engineprofile.EngineID
		planSchema string
		observed   any
		wantSchema string
		wantError  bool
		errorText  string
	}{
		{name: "sqlite empty", engine: engineprofile.SQLite, wantSchema: "main"},
		{name: "sqlite main", engine: engineprofile.SQLite, planSchema: "main", wantSchema: "main"},
		{name: "sqlite other", engine: engineprofile.SQLite, planSchema: "other", wantError: true, errorText: "not main"},
		{name: "postgres empty", engine: engineprofile.PostgreSQL, observed: "tenant", wantSchema: "tenant"},
		{name: "postgres exact", engine: engineprofile.PostgreSQL, planSchema: "tenant", observed: "tenant", wantSchema: "tenant"},
		{name: "postgres mismatch", engine: engineprofile.PostgreSQL, planSchema: "other", observed: "tenant", wantError: true, errorText: "does not match"},
		{name: "postgres null", engine: engineprofile.PostgreSQL, observed: nil, wantError: true, errorText: "empty plan schema"},
		{name: "postgres empty result", engine: engineprofile.PostgreSQL, observed: "", wantError: true, errorText: "empty plan schema"},
		{name: "mysql empty", engine: engineprofile.MySQL, observed: "tenant", wantSchema: "tenant"},
		{name: "mysql exact", engine: engineprofile.MySQL, planSchema: "tenant", observed: "tenant", wantSchema: "tenant"},
		{name: "mysql mismatch", engine: engineprofile.MySQL, planSchema: "other", observed: "tenant", wantError: true, errorText: "does not match"},
		{name: "mysql null", engine: engineprofile.MySQL, observed: nil, wantError: true, errorText: "empty plan schema"},
		{name: "mysql empty result", engine: engineprofile.MySQL, observed: "", wantError: true, errorText: "empty plan schema"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() { _ = database.Close() })
			databaseRunner := runnerFor(t, database, dialectForEngine(test.engine))
			plan := runnerPlan(t, databaseRunner.historyTable, test.engine, changeplan.TransactionForbidden)
			prepared, err := prepareChangePlan(databaseRunner, plan)
			require.NoError(t, err)
			history, historyErr := changeplan.NewHistoryIdentity(test.planSchema, databaseRunner.historyTable)
			require.NoError(t, historyErr)
			prepared.history = history
			if test.engine != engineprofile.SQLite {
				query := "SELECT current_schema()"
				if test.engine == engineprofile.MySQL {
					query = "SELECT DATABASE()"
				}
				if test.observed == nil {
					mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"schema"}).AddRow(nil))
				} else {
					mock.ExpectQuery(query).WillReturnRows(sqlmock.NewRows([]string{"schema"}).AddRow(test.observed))
				}
			}
			connection, err := database.Conn(context.Background())
			require.NoError(t, err)
			resolved, resolveErr := resolvePlanHistory(context.Background(), connection, prepared)
			require.NoError(t, connection.Close())
			if test.wantError {
				require.Error(t, resolveErr)
				require.ErrorContains(t, resolveErr, test.errorText)
			} else {
				require.NoError(t, resolveErr)
				require.Equal(t, test.wantSchema, resolved.schema)
				wantQualified := `"main"."rasql_schema_migrations_plan_progress"`
				switch test.engine {
				case engineprofile.PostgreSQL:
					wantQualified = `"tenant"."rasql_schema_migrations_plan_progress"`
				case engineprofile.MySQL:
					wantQualified = "`tenant`.`rasql_schema_migrations_plan_progress`"
				}
				require.Equal(t, wantQualified, resolved.qualifiedPlanSQL)
				if test.engine == engineprofile.SQLite {
					require.Equal(t, "main", resolved.historyTable.Schema)
					require.Equal(t, "main", resolved.legacyProgressTable.Schema)
					require.Equal(t, "main", resolved.planProgressTable.Schema)
				} else {
					require.Empty(t, resolved.historyTable.Schema)
					require.Empty(t, resolved.legacyProgressTable.Schema)
					require.Empty(t, resolved.planProgressTable.Schema)
				}
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestNewPlanProgressStoreUsesResolvedDialect(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	runner := runnerFor(t, database, dialect.SQLite())
	prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
	require.NoError(t, err)
	history := resolvedHistoryFor(prepared, "sqlite")
	store, err := newPlanProgressStore(prepared, history)
	require.NoError(t, err)
	require.Equal(t, "sqlite", store.dialect.Name())

	missing := history
	missing.dialectName = ""
	_, err = newPlanProgressStore(prepared, missing)
	require.ErrorContains(t, err, "dialect is missing")

	mismatched := history
	mismatched.dialectName = "postgresql"
	_, err = newPlanProgressStore(prepared, mismatched)
	require.ErrorContains(t, err, "does not match plan engine")
}

func TestResolvePlanHistoryRejectsQueryAndScanFailures(t *testing.T) {
	for _, engine := range []engineprofile.EngineID{engineprofile.PostgreSQL, engineprofile.MySQL} {
		for _, test := range []struct {
			name string
			rows *sqlmock.Rows
			err  error
		}{
			{name: "query", err: sql.ErrNoRows},
			{name: "scan", rows: sqlmock.NewRows([]string{"schema", "extra"}).AddRow("tenant", "unexpected")},
		} {
			test := test
			t.Run(test.name, func(t *testing.T) {
				database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
				require.NoError(t, err)
				t.Cleanup(func() { _ = database.Close() })
				dialectValue := dialectForEngine(engine)
				runner := runnerFor(t, database, dialectValue)
				prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engine, changeplan.TransactionForbidden))
				require.NoError(t, err)
				expectedQuery := "SELECT current_schema()"
				if engine == engineprofile.MySQL {
					expectedQuery = "SELECT DATABASE()"
				}
				expected := mock.ExpectQuery(expectedQuery)
				if test.err != nil {
					expected.WillReturnError(test.err)
				} else {
					expected.WillReturnRows(test.rows)
				}
				connection, err := database.Conn(context.Background())
				require.NoError(t, err)
				_, err = resolvePlanHistory(context.Background(), connection, prepared)
				require.Error(t, err)
				require.NoError(t, connection.Close())
				require.NoError(t, mock.ExpectationsWereMet())
			})
		}
	}
}

func TestPlanMetadataReadOnlyHelpers(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	runner := runnerFor(t, database, dialect.SQLite())
	prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
	require.NoError(t, err)
	history := resolvedHistoryFor(prepared, "sqlite")
	catalogQuery := `SELECT 1 FROM "main".sqlite_master WHERE type = 'table' AND name = ?`
	legacyQuery := "SELECT " + runner.idSQL + ", " + runner.checksumSQL + ", " + runner.progressColumn("direction") + ", " + runner.progressColumn("source_index") + ", " + runner.progressColumn("source") + ", " + runner.progressColumn("next_index") + " FROM " + runner.progressSQL + " ORDER BY " + runner.idSQL

	mock.ExpectQuery(catalogQuery).WithArgs(runner.progressTable).WillReturnRows(sqlmock.NewRows([]string{"exists"}))
	entry, err := readLegacyProgressIfExists(context.Background(), runner, database, history)
	require.NoError(t, err)
	require.Nil(t, entry)

	mock.ExpectQuery(catalogQuery).WithArgs(runner.progressTable).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(legacyQuery).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"}))
	entry, err = readLegacyProgressIfExists(context.Background(), runner, database, history)
	require.NoError(t, err)
	require.Nil(t, entry)

	mock.ExpectQuery(catalogQuery).WithArgs(runner.progressTable).WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(1))
	mock.ExpectQuery(legacyQuery).WillReturnRows(sqlmock.NewRows([]string{"id", "checksum", "direction", "source_index", "source", "next_index"}).AddRow("migration", "checksum", DirectionUp, 0, "source", 1))
	entry, err = readLegacyProgressIfExists(context.Background(), runner, database, history)
	require.NoError(t, err)
	require.NotNil(t, entry)
	require.Equal(t, "migration", entry.id)

	mock.ExpectQuery(catalogQuery).WithArgs(runner.progressTable).WillReturnError(errors.New("catalog failed"))
	_, err = readLegacyProgressIfExists(context.Background(), runner, database, history)
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPlanMetadataReadOnlyHelpersAllEngines(t *testing.T) {
	for _, engine := range []engineprofile.EngineID{engineprofile.PostgreSQL, engineprofile.MySQL} {
		t.Run(dialectForEngine(engine).Name(), func(t *testing.T) {
			runnerDatabase, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
			require.NoError(t, err)
			t.Cleanup(func() { _ = runnerDatabase.Close() })
			runner := runnerFor(t, runnerDatabase, dialectForEngine(engine))
			prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engine, changeplan.TransactionForbidden))
			require.NoError(t, err)
			history := resolvedHistoryFor(prepared, dialectForEngine(engine).Name())
			catalogQuery := "SELECT 1 FROM information_schema.tables WHERE table_schema = $1 AND table_name = $2"
			if engine == engineprofile.MySQL {
				catalogQuery = "SELECT 1 FROM information_schema.tables WHERE table_schema = ? AND table_name = ?"
			}
			legacyQuery := "SELECT " + runner.idSQL + ", " + runner.checksumSQL + ", " + runner.progressColumn("direction") + ", " + runner.progressColumn("source_index") + ", " + runner.progressColumn("source") + ", " + runner.progressColumn("next_index") + " FROM " + runner.progressSQL + " ORDER BY " + runner.idSQL
			require.Equal(t, legacyProgressSelectLiteral(engine), legacyQuery)
			progressColumns := []string{"id", "checksum", "direction", "source_index", "source", "next_index"}
			for _, test := range []struct {
				name         string
				catalogRows  *sqlmock.Rows
				catalogErr   error
				legacyRows   *sqlmock.Rows
				wantNil      bool
				wantError    bool
				wantLegacyID string
			}{
				{name: "absent", catalogRows: sqlmock.NewRows([]string{"exists"}), wantNil: true},
				{name: "empty progress", catalogRows: sqlmock.NewRows([]string{"exists"}).AddRow(1), legacyRows: sqlmock.NewRows(progressColumns), wantNil: true},
				{name: "one progress", catalogRows: sqlmock.NewRows([]string{"exists"}).AddRow(1), legacyRows: sqlmock.NewRows(progressColumns).AddRow("migration", "checksum", DirectionUp, 0, "source", 1), wantLegacyID: "migration"},
				{name: "unrelated progress", catalogRows: sqlmock.NewRows([]string{"exists"}).AddRow(1), legacyRows: sqlmock.NewRows(progressColumns).AddRow("unrelated", "checksum", DirectionDown, 4, "source", 2), wantLegacyID: "unrelated"},
				{name: "multiple progress", catalogRows: sqlmock.NewRows([]string{"exists"}).AddRow(1), legacyRows: sqlmock.NewRows(progressColumns).AddRow("a", "checksum", DirectionUp, 0, "source", 1).AddRow("b", "checksum", DirectionUp, 1, "source", 2), wantError: true},
				{name: "catalog query failure", catalogErr: errors.New("catalog failed"), wantError: true},
				{name: "catalog scan failure", catalogRows: sqlmock.NewRows([]string{"exists", "extra"}).AddRow(1, "unexpected"), wantError: true},
				{name: "catalog rows failure", catalogRows: sqlmock.NewRows([]string{"exists"}).AddRow(1).RowError(0, errors.New("catalog row failed")), wantError: true},
				{name: "catalog close failure", catalogRows: sqlmock.NewRows([]string{"exists"}).AddRow(1).CloseError(errors.New("catalog close failed")), wantError: true},
				{name: "legacy scan failure", catalogRows: sqlmock.NewRows([]string{"exists"}).AddRow(1), legacyRows: sqlmock.NewRows(progressColumns).AddRow("migration", "checksum", "bad-direction", "bad-index", "source", "bad-next"), wantError: true},
				{name: "legacy rows failure", catalogRows: sqlmock.NewRows([]string{"exists"}).AddRow(1), legacyRows: sqlmock.NewRows(progressColumns).AddRow("migration", "checksum", DirectionUp, 0, "source", 1).RowError(0, errors.New("legacy row failed")), wantError: true},
			} {
				t.Run(test.name, func(t *testing.T) {
					expectation := mock.ExpectQuery(catalogQuery).WithArgs(history.schema, history.legacyProgressTable.Name)
					if test.catalogErr != nil {
						expectation.WillReturnError(test.catalogErr)
					} else {
						expectation.WillReturnRows(test.catalogRows)
					}
					if test.legacyRows != nil {
						mock.ExpectQuery(legacyQuery).WillReturnRows(test.legacyRows)
					}
					entry, helperErr := readLegacyProgressIfExists(t.Context(), runner, runnerDatabase, history)
					if test.wantError {
						require.Error(t, helperErr)
						return
					}
					require.NoError(t, helperErr)
					if test.wantNil {
						require.Nil(t, entry)
						return
					}
					require.NotNil(t, entry)
					require.Equal(t, test.wantLegacyID, entry.id)
				})
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func legacyProgressSelectLiteral(engine engineprofile.EngineID) string {
	switch engine {
	case engineprofile.PostgreSQL:
		return `SELECT "id", "checksum", "direction", "source_index", "source", "next_index" FROM "rasql_schema_migrations_progress" ORDER BY "id"`
	case engineprofile.MySQL:
		return "SELECT `id`, `checksum`, `direction`, `source_index`, `source`, `next_index` FROM `rasql_schema_migrations_progress` ORDER BY `id`"
	default:
		return `SELECT "id", "checksum", "direction", "source_index", "source", "next_index" FROM "rasql_schema_migrations_progress" ORDER BY "id"`
	}
}

func TestCustomHistoryKeepsLegacyObjectsAndIsolatesPlanProgress(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	database.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	firstRunner := runnerFor(t, database, dialect.SQLite())
	secondRunner, err := NewWithHistoryTable(database, dialect.SQLite(), "other_schema_migrations")
	require.NoError(t, err)
	firstHistory := prepareCustomHistoryFixture(t, firstRunner)
	secondHistory := prepareCustomHistoryFixture(t, secondRunner)
	firstDDL := snapshotSQLiteObjects(t, database, firstRunner.historyTable, firstRunner.progressTable)
	secondDDL := snapshotSQLiteObjects(t, database, secondRunner.historyTable, secondRunner.progressTable)
	firstRows := snapshotLegacyRows(t, database, firstRunner)
	secondRows := snapshotLegacyRows(t, database, secondRunner)

	firstStore := resolveSQLiteStore(t, database, firstRunner, firstHistory)
	secondStore := resolveSQLiteStore(t, database, secondRunner, secondHistory)
	require.NotEqual(t, firstStore.tableSQL, secondStore.tableSQL)
	require.NoError(t, firstStore.ensure(t.Context(), database))
	require.NoError(t, secondStore.ensure(t.Context(), database))
	require.Equal(t, firstDDL, snapshotSQLiteObjects(t, database, firstRunner.historyTable, firstRunner.progressTable))
	require.Equal(t, secondDDL, snapshotSQLiteObjects(t, database, secondRunner.historyTable, secondRunner.progressTable))
	require.Equal(t, firstRows, snapshotLegacyRows(t, database, firstRunner))
	require.Equal(t, secondRows, snapshotLegacyRows(t, database, secondRunner))

	entry := validPlanProgressEntry(t, 0, 1)
	require.NoError(t, firstStore.insert(t.Context(), database, entry))
	require.NoError(t, secondStore.insert(t.Context(), database, entry))
	assertPlanProgressRow(t, database, firstStore, entry)
	assertPlanProgressRow(t, database, secondStore, entry)
	var firstCount, secondCount int
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+firstStore.tableSQL).Scan(&firstCount))
	require.NoError(t, database.QueryRowContext(t.Context(), "SELECT COUNT(*) FROM "+secondStore.tableSQL).Scan(&secondCount))
	require.Equal(t, 1, firstCount)
	require.Equal(t, 1, secondCount)
}

func prepareCustomHistoryFixture(t *testing.T, runner Runner) resolvedPlanHistory {
	t.Helper()
	require.NoError(t, runner.ensureHistory(t.Context(), runner.database))
	require.NoError(t, runner.ensureProgress(t.Context(), runner.database))
	_, err := runner.database.ExecContext(t.Context(), "INSERT INTO "+runner.historySQL+" ("+runner.idSQL+", "+runner.checksumSQL+", "+runner.appliedAtSQL+") VALUES (?, ?, ?)", "migration", "checksum", "2024-01-01 00:00:00")
	require.NoError(t, err)
	_, err = runner.database.ExecContext(t.Context(), "INSERT INTO "+runner.progressSQL+" ("+runner.idSQL+", "+runner.checksumSQL+", "+runner.progressColumn("direction")+", "+runner.progressColumn("source_index")+", "+runner.progressColumn("source")+", "+runner.progressColumn("next_index")+", "+runner.progressColumn("started_at")+") VALUES (?, ?, ?, ?, ?, ?, ?)", "migration", "checksum", DirectionUp, 0, "source", 1, "2024-01-01 00:00:00")
	require.NoError(t, err)
	prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
	require.NoError(t, err)
	return resolvedHistoryFor(prepared, "sqlite")
}

func resolveSQLiteStore(t *testing.T, database *sql.DB, runner Runner, history resolvedPlanHistory) planProgressStore {
	t.Helper()
	prepared, err := prepareChangePlan(runner, runnerPlan(t, runner.historyTable, engineprofile.SQLite, changeplan.TransactionForbidden))
	require.NoError(t, err)
	store, err := newPlanProgressStore(prepared, history)
	require.NoError(t, err)
	return store
}

func snapshotSQLiteObjects(t *testing.T, database *sql.DB, names ...string) []string {
	t.Helper()
	rows, err := database.QueryContext(t.Context(), "SELECT name, sql FROM main.sqlite_master WHERE name IN (?, ?) ORDER BY name", names[0], names[1])
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	var snapshot []string
	for rows.Next() {
		var name, sqlText string
		require.NoError(t, rows.Scan(&name, &sqlText))
		snapshot = append(snapshot, name+"\x00"+sqlText)
	}
	require.NoError(t, rows.Err())
	return snapshot
}

func snapshotLegacyRows(t *testing.T, database *sql.DB, runner Runner) []string {
	t.Helper()
	queries := []string{
		"SELECT quote(" + runner.idSQL + "), quote(" + runner.checksumSQL + "), quote(" + runner.appliedAtSQL + ") FROM " + runner.historySQL + " ORDER BY " + runner.idSQL,
		"SELECT quote(" + runner.idSQL + "), quote(" + runner.checksumSQL + "), quote(" + runner.progressColumn("direction") + "), quote(" + runner.progressColumn("source_index") + "), quote(" + runner.progressColumn("source") + "), quote(" + runner.progressColumn("next_index") + "), quote(" + runner.progressColumn("started_at") + ") FROM " + runner.progressSQL + " ORDER BY " + runner.idSQL,
	}
	var snapshot []string
	for queryIndex, query := range queries {
		rows, err := database.QueryContext(t.Context(), query)
		require.NoError(t, err)
		for rows.Next() {
			values := make([]string, 3)
			if queryIndex == 1 {
				values = make([]string, 7)
			}
			arguments := make([]any, len(values))
			for index := range values {
				arguments[index] = &values[index]
			}
			require.NoError(t, rows.Scan(arguments...))
			snapshot = append(snapshot, values...)
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	return snapshot
}

func dialectForEngine(engine engineprofile.EngineID) dialect.Dialect {
	switch engine {
	case engineprofile.PostgreSQL:
		return dialect.PostgreSQL()
	case engineprofile.MySQL:
		return dialect.MySQL()
	default:
		return dialect.SQLite()
	}
}
