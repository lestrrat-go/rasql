package migrate

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/stretchr/testify/require"
)

func recoveryMatrixMigration() Migration {
	return Migration{
		ID: "001_recovery",
		Statements: []Statement{
			{Source: "001_create.up.sql", SQL: sqltext.Text("CREATE TABLE recovery_one")},
			{Source: "002_index.up.sql", SQL: sqltext.Text("CREATE INDEX recovery_two")},
			{Source: "003_seed.up.sql", SQL: sqltext.Text("INSERT INTO recovery_three")},
		},
		Down: []Statement{
			{Source: "001_create.down.sql", SQL: sqltext.Text("DROP TABLE recovery_one")},
			{Source: "002_index.down.sql", SQL: sqltext.Text("DROP INDEX recovery_two")},
			{Source: "003_seed.down.sql", SQL: sqltext.Text("DELETE FROM recovery_three")},
		},
	}
}

func openRecoveryRunner(t *testing.T, fixture *dbtest.Recovery) (*sql.DB, Runner) {
	t.Helper()
	database, err := fixture.Open()
	require.NoError(t, err)
	runner, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	return database, runner
}

func TestRecoveryPublicFiveWindows(t *testing.T) {
	migration := recoveryMatrixMigration()
	tests := []struct {
		name                string
		failure             dbtest.Failure
		configure           func(*dbtest.Recovery)
		wantProgress        bool
		wantIndex, wantNext int
	}{
		{name: "intent before SQL", failure: dbtest.FailIntent, wantProgress: false},
		{name: "migration SQL", configure: func(f *dbtest.Recovery) { f.FailMigrationAt(1) }, wantProgress: true, wantIndex: 1, wantNext: 1},
		{name: "post SQL before checkpoint", configure: func(f *dbtest.Recovery) { f.FailCheckpointAt(1) }, wantProgress: true, wantIndex: 1, wantNext: 1},
		{name: "history mutation", failure: dbtest.FailHistory, wantProgress: true, wantIndex: 2, wantNext: 3},
		{name: "progress cleanup", failure: dbtest.FailProgressCleanup, wantProgress: true, wantIndex: 2, wantNext: 3},
	}
	for _, direction := range []Direction{DirectionUp, DirectionDown} {
		for _, test := range tests {
			t.Run(string(direction)+"/"+test.name, func(t *testing.T) {
				fixture := dbtest.NewRecovery()
				if test.configure != nil {
					test.configure(fixture)
				} else if test.failure != 0 {
					fixture.FailNext(test.failure)
				}
				database, runner := openRecoveryRunner(t, fixture)
				if direction == DirectionDown {
					fixture.SetHistory(migration.ID, checksum(migration.Statements))
				}
				var result ExecutionResult
				var err error
				if direction == DirectionUp {
					result, err = runner.ApplyResult(t.Context(), AllPending(), migration)
				} else {
					result, err = runner.RevertResult(t.Context(), Steps(1), migration)
				}
				require.Error(t, err)
				if test.wantProgress {
					require.NotNil(t, result.Incomplete)
				} else if !test.wantProgress || test.wantNext != 3 {
					require.NotNil(t, result.Incomplete)
				}
				_ = database.Close()
				reopened, restarted := openRecoveryRunner(t, fixture)
				defer func() { _ = reopened.Close() }()
				snapshot := fixture.Snapshot()
				if test.wantProgress {
					require.NotNil(t, snapshot.Progress)
					require.Equal(t, test.wantIndex, snapshot.Progress.SourceIndex)
					require.Equal(t, test.wantNext, snapshot.Progress.NextIndex)
				} else {
					require.Nil(t, snapshot.Progress)
				}
				executedSources := migration.Statements
				if direction == DirectionDown {
					executedSources = migration.Down
				}
				for sourceIndex, sql := range executedSources {
					want := 1
					if test.name == "intent before SQL" || (test.name == "migration SQL" && sourceIndex > 0) || (test.name == "post SQL before checkpoint" && sourceIndex == 2) {
						want = 0
					}
					require.Equal(t, want, snapshot.Executions[string(sql.SQL)])
				}
				status, statusErr := restarted.Status(t.Context(), migration)
				require.NoError(t, statusErr)
				if direction == DirectionUp {
					_, planErr := restarted.ApplyPlan(t.Context(), AllPending(), migration)
					if test.wantProgress && test.wantNext == test.wantIndex {
						require.Error(t, planErr)
					} else {
						require.NoError(t, planErr)
					}
				} else if !test.wantProgress || test.wantNext != 3 {
					_, planErr := restarted.RevertPlan(t.Context(), Steps(1), migration)
					if test.wantProgress && test.wantNext == test.wantIndex {
						require.Error(t, planErr)
					} else {
						require.NoError(t, planErr)
					}
				}
				if test.wantProgress && test.wantNext == test.wantIndex {
					require.Equal(t, StatusIncomplete, status[0].State)
				}
				if test.wantProgress && test.wantNext == 3 {
					if direction == DirectionUp {
						require.Equal(t, StatusApplied, status[0].State)
					} else {
						require.Equal(t, StatusPending, status[0].State)
					}
				}
				if test.name == "migration SQL" || test.name == "post SQL before checkpoint" {
					check := &matrixCheck{decision: ReconcileNotExecuted}
					if test.name == "post SQL before checkpoint" {
						check.decision = ReconcileExecuted
					}
					require.NoError(t, restarted.Reconcile(t.Context(), check, migration))
					if direction == DirectionUp {
						_, err = restarted.ApplyResult(t.Context(), AllPending(), migration)
					} else {
						_, err = restarted.RevertResult(t.Context(), Steps(1), migration)
					}
					require.NoError(t, err)
				}
			})
		}
	}
}

func TestRecoveryPublicKnownCheckpointPlansAndRetry(t *testing.T) {
	migration := recoveryMatrixMigration()
	for _, direction := range []Direction{DirectionUp, DirectionDown} {
		t.Run(string(direction), func(t *testing.T) {
			fixture := dbtest.NewRecovery()
			if direction == DirectionDown {
				fixture.SetHistory(migration.ID, checksum(migration.Statements))
			}
			statements := migration.Statements
			source := statements[0].Source
			if direction == DirectionDown {
				source = migration.Down[0].Source
			}
			fixture.SetProgress(dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(direction), SourceIndex: 0, Source: source, NextIndex: 1})
			database, runner := openRecoveryRunner(t, fixture)
			defer func() { _ = database.Close() }()
			var planned []Migration
			var err error
			if direction == DirectionUp {
				planned, err = runner.ApplyPlan(t.Context(), AllPending(), migration)
			} else {
				planned, err = runner.RevertPlan(t.Context(), Steps(1), migration)
			}
			require.NoError(t, err)
			require.Len(t, planned, 1)
			if direction == DirectionUp {
				require.Equal(t, []string{"002_index.up.sql", "003_seed.up.sql"}, sources(planned[0].Statements))
			} else {
				require.Equal(t, []string{"002_index.down.sql", "003_seed.down.sql"}, sources(planned[0].Down))
			}
			planned[0].Statements[0].Source = "mutated"
			planned[0].Down[0].Source = "mutated"
			require.Equal(t, source, fixture.Snapshot().Progress.Source)
		})
	}
}

type matrixCheck struct {
	decision     ReconcileDecision
	calls        int
	connectionID int64
	value        IncompleteMigration
}

func (c *matrixCheck) Check(ctx context.Context, connection *sql.Conn, value IncompleteMigration) (ReconcileDecision, error) {
	c.calls++
	c.value = value
	if err := connection.QueryRowContext(ctx, "SELECT CONNECTION_ID()").Scan(&c.connectionID); err != nil {
		return "", err
	}
	return c.decision, nil
}

func TestRecoveryPublicReconcileAllSourcesAndDecisions(t *testing.T) {
	migration := recoveryMatrixMigration()
	for _, direction := range []Direction{DirectionUp, DirectionDown} {
		for _, decision := range []ReconcileDecision{ReconcileExecuted, ReconcileNotExecuted} {
			for _, sourceIndex := range []int{0, 1, 2} {
				t.Run(string(direction)+"/"+string(decision)+"/"+string(rune('0'+sourceIndex)), func(t *testing.T) {
					fixture := dbtest.NewRecovery()
					if direction == DirectionDown {
						fixture.SetHistory(migration.ID, checksum(migration.Statements))
					}
					statements := migration.Statements
					if direction == DirectionDown {
						statements = migration.Down
					}
					fixture.SetProgress(dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(direction), SourceIndex: sourceIndex, Source: statements[sourceIndex].Source, NextIndex: sourceIndex})
					database, runner := openRecoveryRunner(t, fixture)
					check := &matrixCheck{decision: decision}
					require.NoError(t, runner.Reconcile(t.Context(), check, migration))
					require.Equal(t, 1, check.calls)
					require.Equal(t, int64(1), check.connectionID)
					require.Equal(t, migration.ID, check.value.ID)
					require.Equal(t, sourceIndex, check.value.SourceIndex)
					snapshot := fixture.Snapshot()
					require.Zero(t, snapshot.LockOwner)
					require.Equal(t, snapshot.LockAcquires, snapshot.LockReleases)
					if decision == ReconcileNotExecuted && sourceIndex > 0 {
						require.Equal(t, sourceIndex-1, snapshot.Progress.SourceIndex)
						require.Equal(t, sourceIndex, snapshot.Progress.NextIndex)
					}
					if decision == ReconcileNotExecuted && sourceIndex == 0 {
						require.Nil(t, snapshot.Progress)
					}
					if decision == ReconcileExecuted && sourceIndex < 2 {
						require.Equal(t, sourceIndex+1, snapshot.Progress.NextIndex)
					}
					_ = database.Close()
				})
			}
		}
	}
}

func TestRecoveryPublicMismatchRefusalAndResultCopies(t *testing.T) {
	migration := recoveryMatrixMigration()
	for _, test := range []struct {
		name     string
		progress dbtest.Progress
	}{
		{name: "checksum", progress: dbtest.Progress{ID: migration.ID, Checksum: "wrong", Direction: string(DirectionUp), SourceIndex: 0, Source: migration.Statements[0].Source, NextIndex: 0}},
		{name: "source", progress: dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(DirectionUp), SourceIndex: 0, Source: "wrong.sql", NextIndex: 0}},
		{name: "direction", progress: dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: "sideways", SourceIndex: 0, Source: migration.Statements[0].Source, NextIndex: 0}},
		{name: "source index", progress: dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(DirectionUp), SourceIndex: 3, Source: migration.Statements[0].Source, NextIndex: 3}},
		{name: "next index", progress: dbtest.Progress{ID: migration.ID, Checksum: checksum(migration.Statements), Direction: string(DirectionUp), SourceIndex: 0, Source: migration.Statements[0].Source, NextIndex: 2}},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := dbtest.NewRecovery()
			fixture.SetProgress(test.progress)
			database, runner := openRecoveryRunner(t, fixture)
			defer func() { _ = database.Close() }()
			before := fixture.Snapshot()
			check := &matrixCheck{decision: ReconcileExecuted}
			err := runner.Reconcile(t.Context(), check, migration)
			require.Error(t, err)
			require.Zero(t, check.calls)
			after := fixture.Snapshot()
			require.Equal(t, before.History, after.History)
			require.Equal(t, before.Progress, after.Progress)
			require.Equal(t, before.Effects, after.Effects)
			require.Equal(t, before.Executions, after.Executions)
		})
	}

	fixture := dbtest.NewRecovery()
	database, runner := openRecoveryRunner(t, fixture)
	result, err := runner.ApplyResult(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Len(t, result.Completed, 1)
	result.Completed[0].Statements[0].Source = "changed"
	require.Equal(t, 1, fixture.Snapshot().Executions[string(migration.Statements[0].SQL)])
	completed, err := runner.Apply(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Empty(t, completed)
	completed = append(completed, migration)
	completed[0].Statements[0].Source = "changed again"
	require.Equal(t, 1, fixture.Snapshot().Executions[string(migration.Statements[0].SQL)])
	_ = database.Close()
}

func TestRecoveryPublicRevertAndCompatibilityResultCopies(t *testing.T) {
	migration := recoveryMatrixMigration()
	fixture := dbtest.NewRecovery()
	database, runner := openRecoveryRunner(t, fixture)
	defer func() { _ = database.Close() }()
	result, err := runner.ApplyResult(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Len(t, result.Completed, 1)
	result.Completed[0].Down[0].Source = "changed"
	require.Equal(t, migration.Down[0].Source, recoveryMatrixMigration().Down[0].Source)
	reverted, err := runner.RevertResult(t.Context(), Steps(1), migration)
	require.NoError(t, err)
	require.Len(t, reverted.Completed, 1)
	reverted.Completed[0].Statements[0].Source = "changed"
	require.Equal(t, migration.Statements[0].Source, recoveryMatrixMigration().Statements[0].Source)
	completed, err := runner.Apply(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	require.Len(t, completed, 1)
	completed[0].Statements[0].Source = "changed"
	legacyReverted, err := runner.Revert(t.Context(), Steps(1), migration)
	require.NoError(t, err)
	require.Len(t, legacyReverted, 1)
	legacyReverted[0].Down[0].Source = "changed"
	require.Equal(t, 2, fixture.Snapshot().Executions[string(migration.Statements[0].SQL)])
}

func sources(statements []Statement) []string {
	result := make([]string, len(statements))
	for index, statement := range statements {
		result[index] = statement.Source
	}
	return result
}
