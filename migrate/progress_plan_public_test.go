package migrate

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func planProgressMigration() Migration {
	return Migration{
		ID: "001_users",
		Statements: []Statement{
			{Source: "001_users.sql", SQL: sqltext.Text("CREATE TABLE users")},
			{Source: "002_index.sql", SQL: sqltext.Text("CREATE INDEX users_id")},
			{Source: "003_seed.sql", SQL: sqltext.Text("INSERT INTO users")},
		},
		Down: []Statement{
			{Source: "001_users.down.sql", SQL: sqltext.Text("DROP TABLE users")},
			{Source: "002_index.down.sql", SQL: sqltext.Text("DROP INDEX users_id")},
			{Source: "003_seed.down.sql", SQL: sqltext.Text("DELETE FROM users")},
		},
	}
}

func openPlanProgressRunner(t *testing.T, fixture *dbtest.Recovery) (*sql.DB, Runner) {
	t.Helper()
	database, err := fixture.Open()
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	return database, runner
}

func TestApplyPlanReturnsProgressSourceSuffix(t *testing.T) {
	migration := planProgressMigration()
	fixture := dbtest.NewRecovery()
	fixture.SetProgress(dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(DirectionUp), Source: migration.Statements[0].Source, SourceIndex: 0, NextIndex: 1})
	_, runner := openPlanProgressRunner(t, fixture)
	planned, err := runner.ApplyPlan(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Len(t, planned, 1)
	require.Equal(t, []string{"002_index.sql", "003_seed.sql"}, statementSources(planned[0].Statements))
	require.Equal(t, []string{"001_users.down.sql", "002_index.down.sql", "003_seed.down.sql"}, statementSources(planned[0].Down))
}

func TestRevertPlanReturnsProgressSourceSuffix(t *testing.T) {
	migration := planProgressMigration()
	fixture := dbtest.NewRecovery()
	fixture.SetHistory(migration.ID, checksum(migration.Statements))
	fixture.SetProgress(dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(DirectionDown), Source: migration.Down[0].Source, SourceIndex: 0, NextIndex: 1})
	_, runner := openPlanProgressRunner(t, fixture)
	planned, err := runner.RevertPlan(t.Context(), Steps(1), migration)
	require.NoError(t, err)
	require.Len(t, planned, 1)
	require.Equal(t, []string{"002_index.down.sql", "003_seed.down.sql"}, statementSources(planned[0].Down))
	require.Equal(t, []string{"001_users.sql", "002_index.sql", "003_seed.sql"}, statementSources(planned[0].Statements))
}

func TestProgressPlansRefuseUncertainRows(t *testing.T) {
	for _, direction := range []Direction{DirectionUp, DirectionDown} {
		t.Run(string(direction), func(t *testing.T) {
			migration := planProgressMigration()
			fixture := dbtest.NewRecovery()
			if direction == DirectionDown {
				fixture.SetHistory(migration.ID, checksum(migration.Statements))
			}
			source := migration.Statements[0].Source
			if direction == DirectionDown {
				source = migration.Down[0].Source
			}
			fixture.SetProgress(dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(direction), Source: source, SourceIndex: 0, NextIndex: 0})
			_, runner := openPlanProgressRunner(t, fixture)
			var err error
			if direction == DirectionUp {
				_, err = runner.ApplyPlan(t.Context(), AllPending(), migration)
			} else {
				_, err = runner.RevertPlan(t.Context(), Steps(1), migration)
			}
			var incomplete *IncompleteMigrationError
			require.ErrorAs(t, err, &incomplete)
			require.Equal(t, direction, incomplete.Incomplete.Direction)
		})
	}
}

func TestApplyPlanFinalizesTerminalProgress(t *testing.T) {
	migration := planProgressMigration()
	fixture := dbtest.NewRecovery()
	fixture.SetProgress(dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(DirectionUp), Source: migration.Statements[2].Source, SourceIndex: 2, NextIndex: 3})
	_, runner := openPlanProgressRunner(t, fixture)
	planned, err := runner.ApplyPlan(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Empty(t, planned)
	status, err := runner.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, StatusApplied, status[0].State)
	require.Nil(t, fixture.Snapshot().Progress)
}

func TestRevertPlanFinalizesTerminalProgressAfterHistoryMutation(t *testing.T) {
	migration := planProgressMigration()
	fixture := dbtest.NewRecovery()
	fixture.SetProgress(dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(DirectionDown), Source: migration.Down[2].Source, SourceIndex: 2, NextIndex: 3})
	_, runner := openPlanProgressRunner(t, fixture)
	planned, err := runner.RevertPlan(t.Context(), Steps(1), migration)
	require.NoError(t, err)
	require.Empty(t, planned)
	status, err := runner.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, StatusPending, status[0].State)
	require.Nil(t, fixture.Snapshot().Progress)
}

func statementSources(statements []Statement) []string {
	result := make([]string, len(statements))
	for index, statement := range statements {
		result[index] = statement.Source
	}
	return result
}
