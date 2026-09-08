package migrate

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestIncompleteOperationScheduleMatrix(t *testing.T) {
	prepared := resultPreparedPlan(t)
	valid := []struct {
		name          string
		start, end    int
		operation     int
		stage         ChangePlanOperationStage
		statement     int
		affectedCount int
		certainty     ChangePlanOperationCertainty
	}{
		{"statement driver", 0, 2, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationRolledBack},
		{"statement later boundary", 0, 2, 1, ChangePlanStageStatement, 0, 1, ChangePlanOperationRolledBack},
		{"statement driver second", 0, 2, 1, ChangePlanStageStatement, 0, 2, ChangePlanOperationUnknown},
		{"statement later includes named", 0, 2, 1, ChangePlanStageStatement, 1, 2, ChangePlanOperationUnknown},
		{"preconditions", 0, 2, 1, ChangePlanStagePreconditions, -1, 1, ChangePlanOperationUnknown},
		{"postconditions", 0, 2, 1, ChangePlanStagePostconditions, -1, 2, ChangePlanOperationRolledBack},
		{"checkpoint", 0, 2, 1, ChangePlanStageCheckpoint, -1, 2, ChangePlanOperationUnknown},
		{"commit rolled back", 0, 2, 1, ChangePlanStageCommit, -1, 2, ChangePlanOperationRolledBack},
		{"commit unknown", 0, 2, 1, ChangePlanStageCommit, -1, 2, ChangePlanOperationUnknown},
		{"forbidden postconditions", 2, 3, 2, ChangePlanStagePostconditions, -1, 1, ChangePlanOperationUnknown},
		{"forbidden commit unknown", 2, 3, 2, ChangePlanStageCommit, -1, 1, ChangePlanOperationUnknown},
	}
	for _, item := range valid {
		t.Run(item.name, func(t *testing.T) {
			incomplete, err := newIncompleteOperation(prepared, item.start, item.end, item.operation, item.stage, item.statement, item.affectedCount, item.certainty)
			require.NoError(t, err)
			require.Equal(t, prepared.id, incomplete.PlanID())
			require.Equal(t, item.operation, incomplete.OperationIndex())
			require.Equal(t, prepared.operations[item.operation].operation.ID(), incomplete.Operation().ID())
			require.Equal(t, item.stage, incomplete.Stage())
			require.Equal(t, item.statement, incomplete.StatementIndex())
			require.Equal(t, item.certainty, incomplete.Certainty())
			affected := incomplete.AffectedOperations()
			require.Len(t, affected, item.affectedCount)
			for index := range affected {
				require.Equal(t, prepared.operations[item.start+index].operation.ID(), affected[index].ID())
			}
		})
	}

	invalid := []struct {
		name          string
		start, end    int
		operation     int
		stage         ChangePlanOperationStage
		statement     int
		affectedCount int
		certainty     ChangePlanOperationCertainty
	}{
		{"zero plan ID", 0, 2, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown},
		{"negative group start", -1, 2, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown},
		{"group end beyond schedule", 0, 5, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown},
		{"operation below group", 1, 3, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown},
		{"unknown group mode", 0, 1, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown},
		{"unknown stage", 0, 2, 0, ChangePlanOperationStage("unknown"), -1, 1, ChangePlanOperationUnknown},
		{"unknown certainty", 0, 2, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationCertainty("unknown-certainty")},
		{"empty group", 0, 0, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown},
		{"outside group", 0, 2, 2, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown},
		{"mixed group", 0, 3, 1, ChangePlanStagePostconditions, -1, 2, ChangePlanOperationUnknown},
		{"forbidden group has extra operation", 2, 4, 2, ChangePlanStagePostconditions, -1, 2, ChangePlanOperationUnknown},
		{"empty prefix", 0, 2, 0, ChangePlanStageStatement, 0, 0, ChangePlanOperationUnknown},
		{"prefix too large", 0, 2, 1, ChangePlanStagePostconditions, -1, 3, ChangePlanOperationUnknown},
		{"statement index negative", 0, 2, 0, ChangePlanStageStatement, -1, 1, ChangePlanOperationUnknown},
		{"statement index upper bound", 0, 2, 0, ChangePlanStageStatement, 2, 1, ChangePlanOperationUnknown},
		{"later statement excludes named operation", 0, 2, 1, ChangePlanStageStatement, 1, 1, ChangePlanOperationUnknown},
		{"nonstatement index", 0, 2, 1, ChangePlanStageCheckpoint, 0, 2, ChangePlanOperationUnknown},
		{"precondition names affected operation", 0, 2, 1, ChangePlanStagePreconditions, -1, 2, ChangePlanOperationUnknown},
		{"postcondition excludes named operation", 0, 2, 1, ChangePlanStagePostconditions, -1, 1, ChangePlanOperationUnknown},
		{"checkpoint excludes named operation", 0, 2, 1, ChangePlanStageCheckpoint, -1, 1, ChangePlanOperationUnknown},
		{"commit excludes group", 0, 2, 1, ChangePlanStageCommit, -1, 1, ChangePlanOperationUnknown},
		{"forbidden rolled back", 2, 3, 2, ChangePlanStagePostconditions, -1, 1, ChangePlanOperationRolledBack},
		{"forbidden group contains following operation", 2, 4, 2, ChangePlanStagePostconditions, -1, 2, ChangePlanOperationUnknown},
		{"commit names nonfinal operation", 0, 2, 0, ChangePlanStageCommit, -1, 2, ChangePlanOperationUnknown},
		{"commit has incomplete group", 0, 2, 1, ChangePlanStageCommit, -1, 1, ChangePlanOperationUnknown},
	}
	for _, item := range invalid {
		t.Run(item.name, func(t *testing.T) {
			candidate := prepared
			if item.name == "zero plan ID" {
				candidate.id = changeplan.PlanID{}
			}
			if item.name == "unknown group mode" {
				candidate.operations = append([]preparedPlanOperation(nil), prepared.operations...)
				candidate.operations[0].mode = resolvedChangePlanMode(99)
			}
			_, err := newIncompleteOperation(candidate, item.start, item.end, item.operation, item.stage, item.statement, item.affectedCount, item.certainty)
			require.Error(t, err)
		})
	}
}

func TestIncompleteChangePlanErrorOwnershipAndPrimarySelection(t *testing.T) {
	prepared := resultPreparedPlan(t)
	incomplete, err := newIncompleteOperation(prepared, 0, 2, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown)
	require.NoError(t, err)
	cause := errors.New("statement failed")
	typed := newIncompleteChangePlanError(incomplete, cause)
	var typedError *IncompleteChangePlanError
	require.ErrorAs(t, typed, &typedError)
	require.ErrorIs(t, typed, cause)
	owned := typedError.Incomplete.AffectedOperations()
	require.Len(t, owned, 1)
	owned[0] = prepared.operations[1].operation
	require.Equal(t, prepared.operations[0].operation.ID(), typedError.Incomplete.AffectedOperations()[0].ID())
	incomplete.affectedOperations[0] = prepared.operations[1].operation
	require.Equal(t, prepared.operations[0].operation.ID(), typedError.Incomplete.AffectedOperations()[0].ID())

	err = newIncompleteChangePlanError(IncompleteOperation{}, cause)
	require.Error(t, err)
	var invalidTyped *IncompleteChangePlanError
	require.NotErrorAs(t, err, &invalidTyped)
	err = newIncompleteChangePlanError(incomplete, nil)
	require.Error(t, err)

	cleanup := errors.New("cleanup failed")
	unrelatedIncomplete := newIncompleteChangePlanError(mustIncomplete(t, prepared, 0, 2, 1, ChangePlanStagePostconditions, -1, 2, ChangePlanOperationUnknown), errors.New("unrelated"))
	directResult, directReturned := changePlanExecutionResult(nil, typed)
	require.Same(t, typed, directReturned)
	require.Equal(t, typedError.Incomplete, *directResult.IncompleteOperation)
	joined := errors.Join(typed, cleanup)
	firstResult, returned := changePlanExecutionResult(nil, joined)
	require.NotNil(t, firstResult.IncompleteOperation)
	require.Same(t, joined, returned)
	require.Equal(t, typedError.Incomplete, *firstResult.IncompleteOperation)
	var joinedTyped *IncompleteChangePlanError
	require.ErrorAs(t, returned, &joinedTyped)
	require.ErrorIs(t, returned, cause)
	require.ErrorIs(t, returned, cleanup)
	secondResult, returned := changePlanExecutionResult(nil, errors.Join(errors.New("primary"), unrelatedIncomplete))
	require.Nil(t, secondResult.IncompleteOperation)
	require.Error(t, returned)
	ordered := errors.Join(unrelatedIncomplete, typed)
	orderedResult, orderedReturned := changePlanExecutionResult(nil, ordered)
	require.NotNil(t, orderedResult.IncompleteOperation)
	require.Same(t, ordered, orderedReturned)
	require.Equal(t, changeplan.OperationID("second"), orderedResult.IncompleteOperation.Operation().ID())
	wrapped := fmt.Errorf("wrapped: %w", typed)
	wrappedResult, wrappedReturned := changePlanExecutionResult(nil, wrapped)
	require.NotNil(t, wrappedResult.IncompleteOperation)
	require.Same(t, wrapped, wrappedReturned)
	require.ErrorIs(t, wrappedReturned, cause)
	require.ErrorAs(t, wrappedReturned, &joinedTyped)
	require.Equal(t, typedError.Incomplete, *wrappedResult.IncompleteOperation)
}

func TestIncompleteChangePlanResultDeepCopies(t *testing.T) {
	prepared := resultPreparedPlan(t)
	incomplete := mustIncomplete(t, prepared, 0, 2, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown)
	cause := errors.New("postcondition failed")
	typed := newIncompleteChangePlanError(incomplete, cause)
	inputCompleted := []changeplan.Operation{prepared.operations[0].operation}
	result, returned := changePlanExecutionResult(inputCompleted, typed)
	inputCompleted[0] = prepared.operations[2].operation
	require.Same(t, typed, returned)
	require.ErrorIs(t, returned, cause)
	require.Equal(t, prepared.id, result.IncompleteOperation.PlanID())
	require.Equal(t, 0, result.IncompleteOperation.OperationIndex())
	require.Equal(t, changeplan.OperationID("first"), result.IncompleteOperation.Operation().ID())
	require.Equal(t, ChangePlanStageStatement, result.IncompleteOperation.Stage())
	require.Equal(t, 0, result.IncompleteOperation.StatementIndex())
	require.Equal(t, ChangePlanOperationUnknown, result.IncompleteOperation.Certainty())
	require.Equal(t, `migrate: incomplete change plan operation 0 at statement: postcondition failed`, typed.Error())

	completed := result.CompletedOperations
	require.Len(t, completed, 1)
	completedStatements := completed[0].Statements()
	completedArgs := completedStatements[0].Args()
	completedArgs[0].([]byte)[0] = 'X'
	completedArgs[1] = time.Unix(0, 0)
	completedStatements[0] = stmt.New(sqltext.Text("changed"))
	require.Equal(t, changeplan.OperationID("first"), result.CompletedOperations[0].ID())
	assertResultOperation(t, result.CompletedOperations[0])
	require.Equal(t, "SELECT 1", result.CompletedOperations[0].Statements()[0].SQL())
	require.Equal(t, "SELECT 2", result.CompletedOperations[0].Statements()[1].SQL())
	require.Equal(t, []byte("payload"), result.CompletedOperations[0].Statements()[0].Args()[0])
	require.Equal(t, time.Unix(123, 456), result.CompletedOperations[0].Statements()[0].Args()[1])

	named := result.IncompleteOperation.Operation()
	namedStatements := named.Statements()
	namedArgs := namedStatements[0].Args()
	namedArgs[0].([]byte)[0] = 'Y'
	namedArgs[1] = time.Unix(1, 2)
	namedStatements[0] = stmt.New(sqltext.Text("changed"))
	require.Equal(t, changeplan.OperationID("first"), result.IncompleteOperation.Operation().ID())
	assertResultOperation(t, result.IncompleteOperation.Operation())
	require.Equal(t, "SELECT 1", result.IncompleteOperation.Operation().Statements()[0].SQL())
	require.Equal(t, "SELECT 2", result.IncompleteOperation.Operation().Statements()[1].SQL())
	require.Equal(t, []byte("payload"), result.IncompleteOperation.Operation().Statements()[0].Args()[0])
	require.Equal(t, time.Unix(123, 456), result.IncompleteOperation.Operation().Statements()[0].Args()[1])

	affected := result.IncompleteOperation.AffectedOperations()
	require.Len(t, affected, 1)
	affectedStatements := affected[0].Statements()
	affectedArgs := affectedStatements[0].Args()
	affectedArgs[0].([]byte)[0] = 'Z'
	affectedArgs[1] = time.Unix(9, 10)
	affectedStatements[0] = stmt.New(sqltext.Text("changed"))
	affected[0] = prepared.operations[2].operation
	require.Equal(t, changeplan.OperationID("first"), result.IncompleteOperation.AffectedOperations()[0].ID())
	assertResultOperation(t, result.IncompleteOperation.AffectedOperations()[0])
	require.Equal(t, []byte("payload"), result.IncompleteOperation.AffectedOperations()[0].Statements()[0].Args()[0])
	require.Equal(t, time.Unix(123, 456), result.IncompleteOperation.AffectedOperations()[0].Statements()[0].Args()[1])
	require.Equal(t, "SELECT 2", result.IncompleteOperation.AffectedOperations()[0].Statements()[1].SQL())
	for range 2 {
		require.Len(t, result.IncompleteOperation.AffectedOperations(), 1)
	}
}

func assertResultOperation(t *testing.T, operation changeplan.Operation) {
	t.Helper()
	require.Equal(t, changeplan.OperationID("first"), operation.ID())
	require.Equal(t, changeplan.OperationNativeSQL, operation.Kind())
	require.Equal(t, changeplan.Digest{5}, operation.ResultDigest())
	require.Equal(t, changeplan.TransactionRequired, operation.Transaction())
	require.False(t, operation.Reversible())
	require.Empty(t, operation.DependsOn())
	require.Equal(t, []changeplan.ObjectID{"object"}, operation.Objects())
	require.Len(t, operation.Statements(), 2)
}

func TestIncompleteChangePlanErrorInvalidValues(t *testing.T) {
	prepared := resultPreparedPlan(t)
	valid := mustIncomplete(t, prepared, 0, 2, 1, ChangePlanStagePostconditions, -1, 2, ChangePlanOperationUnknown)
	rows := []struct {
		name string
		make func() IncompleteOperation
	}{
		{"unknown stage", func() IncompleteOperation {
			value := valid
			value.stage = ChangePlanOperationStage("bad")
			return value
		}},
		{"unknown certainty", func() IncompleteOperation {
			value := valid
			value.certainty = ChangePlanOperationCertainty("bad")
			return value
		}},
		{"negative statement", func() IncompleteOperation {
			value := valid
			value.stage = ChangePlanStageStatement
			value.statementIndex = -1
			return value
		}},
		{"upper statement", func() IncompleteOperation {
			value := valid
			value.stage = ChangePlanStageStatement
			value.statementIndex = 2
			return value
		}},
		{"duplicate affected IDs", func() IncompleteOperation {
			value := valid
			value.affectedOperations = []changeplan.Operation{prepared.operations[0].operation, prepared.operations[0].operation}
			return value
		}},
		{"forbidden rolled back", func() IncompleteOperation {
			value := mustIncomplete(t, prepared, 2, 3, 2, ChangePlanStagePostconditions, -1, 1, ChangePlanOperationUnknown)
			value.certainty = ChangePlanOperationRolledBack
			return value
		}},
		{"preconditions contain named", func() IncompleteOperation { value := valid; value.stage = ChangePlanStagePreconditions; return value }},
		{"postconditions missing final", func() IncompleteOperation {
			value := valid
			value.affectedOperations = []changeplan.Operation{prepared.operations[0].operation}
			return value
		}},
		{"checkpoint missing final", func() IncompleteOperation {
			value := valid
			value.stage = ChangePlanStageCheckpoint
			value.affectedOperations = []changeplan.Operation{prepared.operations[0].operation}
			return value
		}},
		{"commit missing final", func() IncompleteOperation {
			value := valid
			value.stage = ChangePlanStageCommit
			value.affectedOperations = []changeplan.Operation{prepared.operations[0].operation}
			return value
		}},
	}
	for _, row := range rows {
		t.Run(row.name, func(t *testing.T) {
			value := row.make()
			err := newIncompleteChangePlanError(value, errors.New("cause"))
			require.Error(t, err)
			var typed *IncompleteChangePlanError
			require.NotErrorAs(t, err, &typed)
		})
	}
}

func TestChangePlanReconciliationErrorFields(t *testing.T) {
	cause := errors.New("progress mismatch")
	err := &ChangePlanReconciliationError{PlanID: changeplan.PlanID{4}, OperationID: "operation", ProgressID: "progress", Cause: cause}
	require.Equal(t, `migrate: change plan reconciliation failed for operation "operation" and progress "progress": progress mismatch`, err.Error())
	require.ErrorIs(t, err, cause)
	var typed *ChangePlanReconciliationError
	require.ErrorAs(t, err, &typed)
	require.Equal(t, changeplan.PlanID{4}, typed.PlanID)
	require.Equal(t, changeplan.OperationID("operation"), typed.OperationID)
	require.Equal(t, "progress", typed.ProgressID)
}

func TestDirectoryExecutionResultCompatibility(t *testing.T) {
	legacy := Migration{ID: "001", Statements: []Statement{{Source: "001.sql", SQL: sqltext.Text("SELECT 1")}}}
	result, err := executionResult([]Migration{legacy}, &IncompleteMigrationError{Incomplete: IncompleteMigration{ID: "002", Direction: DirectionUp, SourceIndex: 0}, Cause: errors.New("failed")})
	require.Len(t, result.Completed, 1)
	require.NotNil(t, result.Incomplete)
	require.Nil(t, result.CompletedOperations)
	require.Nil(t, result.IncompleteOperation)
	require.Error(t, err)
}

func TestPublicExecutionResultCompatibility(t *testing.T) {
	migration := recoveryMatrixMigration()
	successFixture := dbtest.NewRecovery()
	database, runner := openRecoveryRunner(t, successFixture)
	defer func() { _ = database.Close() }()
	applyResult, applyErr := runner.ApplyResult(t.Context(), AllPending(), migration)
	require.NoError(t, applyErr)
	require.Equal(t, []Migration{migration}, applyResult.Completed)
	require.Nil(t, applyResult.Incomplete)
	require.Nil(t, applyResult.CompletedOperations)
	require.Nil(t, applyResult.IncompleteOperation)
	revertResult, revertErr := runner.RevertResult(t.Context(), Steps(1), migration)
	require.NoError(t, revertErr)
	require.Equal(t, []Migration{migration}, revertResult.Completed)
	require.Nil(t, revertResult.Incomplete)
	require.Nil(t, revertResult.CompletedOperations)
	require.Nil(t, revertResult.IncompleteOperation)

	failureFixture := dbtest.NewRecovery()
	failureFixture.FailMigrationAt(1)
	failureDatabase, failureRunner := openRecoveryRunner(t, failureFixture)
	defer func() { _ = failureDatabase.Close() }()
	failureResult, failureErr := failureRunner.ApplyResult(t.Context(), AllPending(), migration)
	assertLegacyIncompleteResult(t, failureResult, failureErr, migration, DirectionUp, 1)

	revertFailureFixture := dbtest.NewRecovery()
	seedDatabase, seedRunner := openRecoveryRunner(t, revertFailureFixture)
	_, err := seedRunner.ApplyResult(t.Context(), AllPending(), migration)
	require.NoError(t, err)
	_ = seedDatabase.Close()
	revertFailureFixture.FailMigrationAt(1)
	revertFailureDatabase, revertFailureRunner := openRecoveryRunner(t, revertFailureFixture)
	defer func() { _ = revertFailureDatabase.Close() }()
	revertFailureResult, revertFailureErr := revertFailureRunner.RevertResult(t.Context(), Steps(1), migration)
	assertLegacyIncompleteResult(t, revertFailureResult, revertFailureErr, migration, DirectionDown, 1)

	recordingDatabase, calls := openRecordingDatabase(t)
	t.Cleanup(func() { _ = recordingDatabase.Close() })
	recordingRunner, err := New(recordingDatabase, dialect.SQLite())
	require.NoError(t, err)
	recordingMigration := Migration{
		ID:         "001",
		Statements: []Statement{{Source: "001.sql", SQL: sqltext.Text("SELECT 1")}},
		Down:       []Statement{{Source: "001.down.sql", SQL: sqltext.Text("SELECT 1")}},
	}
	recordingApplyResult, recordingApplyErr := recordingRunner.ApplyResult(t.Context(), AllPending(), recordingMigration)
	require.Equal(t, ExecutionResult{}, recordingApplyResult)
	require.EqualError(t, recordingApplyErr, "migrate: open database connection: driver: bad connection")
	require.Nil(t, recordingApplyResult.CompletedOperations)
	require.Nil(t, recordingApplyResult.IncompleteOperation)
	recordingRevertResult, recordingRevertErr := recordingRunner.RevertResult(t.Context(), Steps(1), recordingMigration)
	require.Equal(t, ExecutionResult{}, recordingRevertResult)
	require.EqualError(t, recordingRevertErr, "migrate: open database connection: driver: bad connection")
	require.Nil(t, recordingRevertResult.CompletedOperations)
	require.Nil(t, recordingRevertResult.IncompleteOperation)
	require.NotZero(t, *calls)
}

func assertLegacyIncompleteResult(t *testing.T, result ExecutionResult, err error, migration Migration, direction Direction, sourceIndex int) {
	t.Helper()
	require.Error(t, err)
	var incompleteErr *IncompleteMigrationError
	require.ErrorAs(t, err, &incompleteErr)
	require.NotNil(t, result.Incomplete)
	require.Equal(t, migration.ID, result.Incomplete.ID)
	require.Equal(t, recoveryMatrixChecksum(migration), result.Incomplete.Checksum)
	require.Equal(t, direction, result.Incomplete.Direction)
	require.Equal(t, sourceIndex, result.Incomplete.SourceIndex)
	sources := migration.Statements
	if direction == DirectionDown {
		sources = migration.Down
	}
	require.Equal(t, sources[sourceIndex].Source, result.Incomplete.Source)
	require.Equal(t, *result.Incomplete, incompleteErr.Incomplete)
	require.EqualError(t, incompleteErr.Cause, "execute source: migration SQL failure")
	require.ErrorContains(t, err, "migration SQL failure")
	require.Nil(t, result.CompletedOperations)
	require.Nil(t, result.IncompleteOperation)
}

func resultPreparedPlan(t *testing.T) preparedChangePlan {
	t.Helper()
	first := resultOperationWithArgs(t, "first", 2, changeplan.TransactionRequired, []any{[]byte("payload"), time.Unix(123, 456)})
	second := resultOperationWithStatements(t, "second", 2, changeplan.TransactionRequired)
	third := resultOperationWithStatements(t, "third", 1, changeplan.TransactionForbidden)
	fourth := resultOperationWithStatements(t, "fourth", 1, changeplan.TransactionRequired)
	return preparedChangePlan{
		id: changeplan.PlanID{1},
		operations: []preparedPlanOperation{
			{index: 0, operation: first, mode: planModeRequired, beforeDigest: changeplan.Digest{1}, afterDigest: changeplan.Digest{2}},
			{index: 1, operation: second, mode: planModeRequired, beforeDigest: changeplan.Digest{2}, afterDigest: changeplan.Digest{3}},
			{index: 2, operation: third, mode: planModeForbidden, beforeDigest: changeplan.Digest{3}, afterDigest: changeplan.Digest{4}},
			{index: 3, operation: fourth, mode: planModeRequired, beforeDigest: changeplan.Digest{4}, afterDigest: changeplan.Digest{5}},
		},
		hasForbidden: true,
	}
}

func mustIncomplete(t *testing.T, prepared preparedChangePlan, start, end, operation int, stage ChangePlanOperationStage, statement, affected int, certainty ChangePlanOperationCertainty) IncompleteOperation {
	t.Helper()
	incomplete, err := newIncompleteOperation(prepared, start, end, operation, stage, statement, affected, certainty)
	require.NoError(t, err)
	return incomplete
}

func resultOperationWithStatements(t *testing.T, id string, count int, mode changeplan.TransactionMode) changeplan.Operation {
	return resultOperationWithArgs(t, id, count, mode, nil)
}

func resultOperationWithArgs(t *testing.T, id string, count int, mode changeplan.TransactionMode, values []any) changeplan.Operation {
	t.Helper()
	statements := make([]stmt.Statement, count)
	for index := range statements {
		args := values
		if index > 0 {
			args = nil
		}
		statements[index] = stmt.New(sqltext.Text(fmt.Sprintf("SELECT %d", index+1)), args...)
	}
	operation, err := changeplan.NewOperation(changeplan.OperationID(id), changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"object"}, nil, nil, changeplan.Digest{byte(len(id))}, statements, mode, false, nil)
	require.NoError(t, err)
	return operation
}
