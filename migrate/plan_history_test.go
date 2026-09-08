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
					require.Equal(t, "main", resolved.planProgressTable.Schema)
				} else {
					require.Empty(t, resolved.historyTable.Schema)
					require.Empty(t, resolved.planProgressTable.Schema)
				}
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
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
