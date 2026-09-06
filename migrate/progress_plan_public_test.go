package migrate

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
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

func openPlanProgressRunner(t *testing.T, state *recoveryState) (*sql.DB, Runner) {
	t.Helper()
	recoveryCurrent.Store(state)
	database, err := sql.Open(recoveryDriverName, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	return database, runner
}

func TestApplyPlanReturnsProgressSourceSuffix(t *testing.T) {
	migration := planProgressMigration()
	state := &recoveryState{
		history: map[string]string{}, effects: map[string]bool{}, executions: map[string]int{},
		progress: &recoveryProgress{id: migration.ID, checksum: checksum(migration.Statements), direction: string(DirectionUp), source: migration.Statements[0].Source, sourceIndex: 0, nextIndex: 1},
	}
	_, runner := openPlanProgressRunner(t, state)
	planned, err := runner.ApplyPlan(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Len(t, planned, 1)
	require.Equal(t, []string{"002_index.sql", "003_seed.sql"}, statementSources(planned[0].Statements))
	require.Equal(t, []string{"001_users.down.sql", "002_index.down.sql", "003_seed.down.sql"}, statementSources(planned[0].Down))
}

func TestRevertPlanReturnsProgressSourceSuffix(t *testing.T) {
	migration := planProgressMigration()
	state := &recoveryState{
		history: map[string]string{migration.ID: checksum(migration.Statements)}, effects: map[string]bool{}, executions: map[string]int{},
		progress: &recoveryProgress{id: migration.ID, checksum: checksum(migration.Statements), direction: string(DirectionDown), source: migration.Down[0].Source, sourceIndex: 0, nextIndex: 1},
	}
	_, runner := openPlanProgressRunner(t, state)
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
			history := map[string]string{}
			if direction == DirectionDown {
				history[migration.ID] = checksum(migration.Statements)
			}
			source := migration.Statements[0].Source
			if direction == DirectionDown {
				source = migration.Down[0].Source
			}
			state := &recoveryState{
				history: history, effects: map[string]bool{}, executions: map[string]int{},
				progress: &recoveryProgress{id: migration.ID, checksum: checksum(migration.Statements), direction: string(direction), source: source, sourceIndex: 0, nextIndex: 0},
			}
			_, runner := openPlanProgressRunner(t, state)
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
	state := &recoveryState{
		history: map[string]string{}, effects: map[string]bool{}, executions: map[string]int{},
		progress: &recoveryProgress{id: migration.ID, checksum: checksum(migration.Statements), direction: string(DirectionUp), source: migration.Statements[2].Source, sourceIndex: 2, nextIndex: 3},
	}
	_, runner := openPlanProgressRunner(t, state)
	planned, err := runner.ApplyPlan(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Empty(t, planned)
	status, err := runner.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, StatusApplied, status[0].State)
	state.mu.Lock()
	require.Nil(t, state.progress)
	state.mu.Unlock()
}

func TestRevertPlanFinalizesTerminalProgressAfterHistoryMutation(t *testing.T) {
	migration := planProgressMigration()
	state := &recoveryState{
		history: map[string]string{}, effects: map[string]bool{}, executions: map[string]int{},
		progress: &recoveryProgress{id: migration.ID, checksum: checksum(migration.Statements), direction: string(DirectionDown), source: migration.Down[2].Source, sourceIndex: 2, nextIndex: 3},
	}
	_, runner := openPlanProgressRunner(t, state)
	planned, err := runner.RevertPlan(t.Context(), Steps(1), migration)
	require.NoError(t, err)
	require.Empty(t, planned)
	status, err := runner.Status(t.Context(), migration)
	require.NoError(t, err)
	require.Equal(t, StatusPending, status[0].State)
	state.mu.Lock()
	require.Nil(t, state.progress)
	state.mu.Unlock()
}

func statementSources(statements []Statement) []string {
	result := make([]string, len(statements))
	for index, statement := range statements {
		result[index] = statement.Source
	}
	return result
}
