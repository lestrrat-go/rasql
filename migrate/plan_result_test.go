package migrate

import (
	"errors"
	"fmt"
	"testing"

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
		{"empty group", 0, 0, 0, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown},
		{"outside group", 0, 2, 2, ChangePlanStageStatement, 0, 1, ChangePlanOperationUnknown},
		{"mixed group", 0, 3, 1, ChangePlanStagePostconditions, -1, 2, ChangePlanOperationUnknown},
		{"forbidden group has extra operation", 2, 3, 2, ChangePlanStagePostconditions, -1, 2, ChangePlanOperationUnknown},
		{"empty prefix", 0, 2, 0, ChangePlanStageStatement, 0, 0, ChangePlanOperationUnknown},
		{"prefix too large", 0, 2, 1, ChangePlanStagePostconditions, -1, 3, ChangePlanOperationUnknown},
		{"statement index negative", 0, 2, 0, ChangePlanStageStatement, -1, 1, ChangePlanOperationUnknown},
		{"statement index upper bound", 0, 2, 0, ChangePlanStageStatement, 2, 1, ChangePlanOperationUnknown},
		{"nonstatement index", 0, 2, 1, ChangePlanStageCheckpoint, 0, 2, ChangePlanOperationUnknown},
		{"precondition names affected operation", 0, 2, 1, ChangePlanStagePreconditions, -1, 2, ChangePlanOperationUnknown},
		{"postcondition excludes named operation", 0, 2, 1, ChangePlanStagePostconditions, -1, 1, ChangePlanOperationUnknown},
		{"checkpoint excludes named operation", 0, 2, 1, ChangePlanStageCheckpoint, -1, 1, ChangePlanOperationUnknown},
		{"commit excludes group", 0, 2, 1, ChangePlanStageCommit, -1, 1, ChangePlanOperationUnknown},
		{"forbidden rolled back", 2, 3, 2, ChangePlanStagePostconditions, -1, 1, ChangePlanOperationRolledBack},
	}
	for _, item := range invalid {
		t.Run(item.name, func(t *testing.T) {
			_, err := newIncompleteOperation(prepared, item.start, item.end, item.operation, item.stage, item.statement, item.affectedCount, item.certainty)
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
	firstResult, returned := changePlanExecutionResult(nil, errors.Join(typed, cleanup))
	require.NotNil(t, firstResult.IncompleteOperation)
	require.ErrorIs(t, returned, cleanup)
	secondResult, returned := changePlanExecutionResult(nil, errors.Join(errors.New("primary"), unrelatedIncomplete))
	require.Nil(t, secondResult.IncompleteOperation)
	require.Error(t, returned)
	orderedResult, _ := changePlanExecutionResult(nil, errors.Join(unrelatedIncomplete, typed))
	require.NotNil(t, orderedResult.IncompleteOperation)
	require.Equal(t, changeplan.OperationID("second"), orderedResult.IncompleteOperation.Operation().ID())
	wrappedResult, _ := changePlanExecutionResult(nil, fmt.Errorf("wrapped: %w", typed))
	require.NotNil(t, wrappedResult.IncompleteOperation)
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

func resultPreparedPlan(t *testing.T) preparedChangePlan {
	t.Helper()
	first := resultOperationWithStatements(t, "first", 2, changeplan.TransactionRequired)
	second := resultOperationWithStatements(t, "second", 1, changeplan.TransactionRequired)
	third := resultOperationWithStatements(t, "third", 1, changeplan.TransactionForbidden)
	return preparedChangePlan{
		id: changeplan.PlanID{1},
		operations: []preparedPlanOperation{
			{index: 0, operation: first, mode: planModeRequired, beforeDigest: changeplan.Digest{1}, afterDigest: changeplan.Digest{2}},
			{index: 1, operation: second, mode: planModeRequired, beforeDigest: changeplan.Digest{2}, afterDigest: changeplan.Digest{3}},
			{index: 2, operation: third, mode: planModeForbidden, beforeDigest: changeplan.Digest{3}, afterDigest: changeplan.Digest{4}},
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
	t.Helper()
	statements := make([]stmt.Statement, count)
	for index := range statements {
		statements[index] = stmt.New(sqltext.Text(fmt.Sprintf("SELECT %d", index+1)))
	}
	operation, err := changeplan.NewOperation(changeplan.OperationID(id), changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"object"}, nil, nil, changeplan.Digest{byte(len(id))}, statements, mode, false, nil)
	require.NoError(t, err)
	return operation
}
