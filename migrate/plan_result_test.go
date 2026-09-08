package migrate

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestChangePlanResultCopies(t *testing.T) {
	first := resultOperation(t, "first")
	second := resultOperation(t, "second")
	planID := changeplan.PlanID{1}
	cases := []struct {
		name      string
		operation changeplan.Operation
		stage     ChangePlanOperationStage
		statement int
		affected  []changeplan.Operation
		certainty ChangePlanOperationCertainty
	}{
		{"statement driver", first, ChangePlanStageStatement, 0, []changeplan.Operation{first}, ChangePlanOperationRolledBack},
		{"statement cancellation boundary", second, ChangePlanStageStatement, 0, []changeplan.Operation{first}, ChangePlanOperationRolledBack},
		{"preconditions", second, ChangePlanStagePreconditions, -1, []changeplan.Operation{first}, ChangePlanOperationRolledBack},
		{"postconditions", second, ChangePlanStagePostconditions, -1, []changeplan.Operation{second}, ChangePlanOperationRolledBack},
		{"checkpoint", second, ChangePlanStageCheckpoint, -1, []changeplan.Operation{second}, ChangePlanOperationRolledBack},
		{"commit", second, ChangePlanStageCommit, -1, []changeplan.Operation{first, second}, ChangePlanOperationUnknown},
	}
	for _, item := range cases {
		t.Run(item.name, func(t *testing.T) {
			incomplete, err := newIncompleteOperation(planID, 1, item.operation, item.stage, item.statement, item.affected, item.certainty)
			require.NoError(t, err)
			cause := errors.New("cause")
			planErr := newIncompleteChangePlanError(incomplete, cause)
			result, returned := changePlanExecutionResult([]changeplan.Operation{first}, planErr)
			require.ErrorIs(t, returned, cause)
			var typed *IncompleteChangePlanError
			require.ErrorAs(t, returned, &typed)
			require.NotNil(t, result.IncompleteOperation)
			require.Equal(t, item.stage, result.IncompleteOperation.Stage())
			completed := result.CompletedOperations
			completed[0] = second
			affected := result.IncompleteOperation.AffectedOperations()
			affected[0] = second
			require.Equal(t, item.affected[0].ID(), result.IncompleteOperation.AffectedOperations()[0].ID())
			require.Equal(t, item.operation.ID(), typed.Incomplete.Operation().ID())
		})
	}
}

func TestIncompleteOperationValidationMatrix(t *testing.T) {
	first := resultOperation(t, "first")
	second := resultOperation(t, "second")
	planID := changeplan.PlanID{2}
	_, err := newIncompleteOperation(changeplan.PlanID{}, 0, first, ChangePlanStageStatement, 0, []changeplan.Operation{first}, ChangePlanOperationRolledBack)
	require.Error(t, err)
	_, err = newIncompleteOperation(planID, -1, first, ChangePlanStageStatement, 0, []changeplan.Operation{first}, ChangePlanOperationRolledBack)
	require.Error(t, err)
	_, err = newIncompleteOperation(planID, 0, first, ChangePlanOperationStage("bad"), -1, []changeplan.Operation{first}, ChangePlanOperationRolledBack)
	require.Error(t, err)
	_, err = newIncompleteOperation(planID, 0, first, ChangePlanStagePostconditions, 0, []changeplan.Operation{first}, ChangePlanOperationRolledBack)
	require.Error(t, err)
	_, err = newIncompleteOperation(planID, 1, second, ChangePlanStagePreconditions, -1, []changeplan.Operation{second}, ChangePlanOperationRolledBack)
	require.Error(t, err)
	_, err = newIncompleteOperation(planID, 1, second, ChangePlanStagePostconditions, -1, []changeplan.Operation{first}, ChangePlanOperationRolledBack)
	require.Error(t, err)
	_, err = newIncompleteOperation(planID, 1, second, ChangePlanStageStatement, 0, []changeplan.Operation{}, ChangePlanOperationRolledBack)
	require.Error(t, err)
	for _, certainty := range []ChangePlanOperationCertainty{"", "certain"} {
		_, err = newIncompleteOperation(planID, 0, first, ChangePlanStageStatement, 0, []changeplan.Operation{first}, certainty)
		require.Error(t, err)
	}
	_, err = newIncompleteOperation(planID, 0, first, ChangePlanStageStatement, -1, []changeplan.Operation{first}, ChangePlanOperationRolledBack)
	require.Error(t, err)
	_, err = newIncompleteOperation(planID, 0, first, ChangePlanStageCommit, -1, []changeplan.Operation{first, first}, ChangePlanOperationUnknown)
	require.Error(t, err)
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

func resultOperation(t *testing.T, id string) changeplan.Operation {
	t.Helper()
	operation, err := changeplan.NewOperation(changeplan.OperationID(id), changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"object"}, nil, nil, changeplan.Digest{byte(len(id))}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionRequired, false, nil)
	require.NoError(t, err)
	return operation
}
