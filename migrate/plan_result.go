package migrate

import (
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
)

type ChangePlanOperationCertainty string

const (
	ChangePlanOperationRolledBack ChangePlanOperationCertainty = "rolled_back"
	ChangePlanOperationUnknown    ChangePlanOperationCertainty = "unknown"
)

type ChangePlanOperationStage string

const (
	ChangePlanStageStatement      ChangePlanOperationStage = "statement"
	ChangePlanStagePreconditions  ChangePlanOperationStage = "preconditions"
	ChangePlanStagePostconditions ChangePlanOperationStage = "postconditions"
	ChangePlanStageCheckpoint     ChangePlanOperationStage = "checkpoint"
	ChangePlanStageCommit         ChangePlanOperationStage = "commit"
)

type IncompleteOperation struct {
	planID             changeplan.PlanID
	operationIndex     int
	operation          changeplan.Operation
	stage              ChangePlanOperationStage
	statementIndex     int
	affectedOperations []changeplan.Operation
	certainty          ChangePlanOperationCertainty
}

func (o IncompleteOperation) PlanID() changeplan.PlanID       { return o.planID }
func (o IncompleteOperation) OperationIndex() int             { return o.operationIndex }
func (o IncompleteOperation) Operation() changeplan.Operation { return o.operation }
func (o IncompleteOperation) Stage() ChangePlanOperationStage { return o.stage }
func (o IncompleteOperation) StatementIndex() int             { return o.statementIndex }
func (o IncompleteOperation) AffectedOperations() []changeplan.Operation {
	return append([]changeplan.Operation(nil), o.affectedOperations...)
}
func (o IncompleteOperation) Certainty() ChangePlanOperationCertainty { return o.certainty }

type IncompleteChangePlanError struct {
	Incomplete IncompleteOperation
	Cause      error
}

func (e *IncompleteChangePlanError) Error() string {
	if e == nil {
		return "migrate: incomplete change plan"
	}
	return fmt.Sprintf("migrate: incomplete change plan operation %d at %s: %v", e.Incomplete.operationIndex, e.Incomplete.stage, e.Cause)
}
func (e *IncompleteChangePlanError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

type ChangePlanReconciliationError struct {
	PlanID      changeplan.PlanID
	OperationID changeplan.OperationID
	ProgressID  string
	Cause       error
}

func (e *ChangePlanReconciliationError) Error() string {
	if e == nil {
		return "migrate: change plan reconciliation failed"
	}
	return fmt.Sprintf("migrate: change plan reconciliation failed for operation %q and progress %q: %v", e.OperationID, e.ProgressID, e.Cause)
}
func (e *ChangePlanReconciliationError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Cause
}

func newIncompleteOperation(planID changeplan.PlanID, operationIndex int, operation changeplan.Operation, stage ChangePlanOperationStage, statementIndex int, affected []changeplan.Operation, certainty ChangePlanOperationCertainty) (IncompleteOperation, error) {
	if planID == (changeplan.PlanID{}) {
		return IncompleteOperation{}, fmt.Errorf("migrate: incomplete operation requires a plan ID")
	}
	if operationIndex < 0 || operation.ID() == "" {
		return IncompleteOperation{}, fmt.Errorf("migrate: incomplete operation identity is invalid")
	}
	if !validChangePlanStage(stage) || !validChangePlanCertainty(certainty) {
		return IncompleteOperation{}, fmt.Errorf("migrate: incomplete operation stage or certainty is invalid")
	}
	if stage == ChangePlanStageStatement {
		if statementIndex < 0 {
			return IncompleteOperation{}, fmt.Errorf("migrate: statement stage requires a statement index")
		}
	} else if statementIndex != -1 {
		return IncompleteOperation{}, fmt.Errorf("migrate: non-statement stage requires statement index -1")
	}
	if len(affected) == 0 {
		return IncompleteOperation{}, fmt.Errorf("migrate: incomplete operation requires affected operations")
	}
	seen := make(map[changeplan.OperationID]struct{}, len(affected))
	for index, item := range affected {
		if item.ID() == "" {
			return IncompleteOperation{}, fmt.Errorf("migrate: affected operation %d has no ID", index)
		}
		if _, exists := seen[item.ID()]; exists {
			return IncompleteOperation{}, fmt.Errorf("migrate: affected operations contain duplicate %q", item.ID())
		}
		seen[item.ID()] = struct{}{}
	}
	if certainty == ChangePlanOperationRolledBack && operation.Transaction() == changeplan.TransactionForbidden {
		return IncompleteOperation{}, fmt.Errorf("migrate: forbidden operation cannot be rolled back")
	}
	if stage == ChangePlanStagePreconditions {
		if affected[len(affected)-1].ID() == operation.ID() {
			return IncompleteOperation{}, fmt.Errorf("migrate: precondition operation cannot be affected")
		}
	} else if stage != ChangePlanStageStatement && affected[len(affected)-1].ID() != operation.ID() {
		return IncompleteOperation{}, fmt.Errorf("migrate: %s failure must affect the named operation", stage)
	}
	return IncompleteOperation{planID: planID, operationIndex: operationIndex, operation: operation, stage: stage, statementIndex: statementIndex, affectedOperations: append([]changeplan.Operation(nil), affected...), certainty: certainty}, nil
}

func validChangePlanStage(stage ChangePlanOperationStage) bool {
	switch stage {
	case ChangePlanStageStatement, ChangePlanStagePreconditions, ChangePlanStagePostconditions, ChangePlanStageCheckpoint, ChangePlanStageCommit:
		return true
	default:
		return false
	}
}
func validChangePlanCertainty(certainty ChangePlanOperationCertainty) bool {
	return certainty == ChangePlanOperationRolledBack || certainty == ChangePlanOperationUnknown
}

func newIncompleteChangePlanError(incomplete IncompleteOperation, cause error) error {
	if cause == nil {
		return fmt.Errorf("migrate: incomplete change plan requires a cause")
	}
	return &IncompleteChangePlanError{Incomplete: incomplete, Cause: cause}
}

func changePlanExecutionResult(completed []changeplan.Operation, err error) (ExecutionResult, error) {
	result := ExecutionResult{CompletedOperations: append([]changeplan.Operation(nil), completed...)}
	var incomplete *IncompleteChangePlanError
	if errors.As(err, &incomplete) && incomplete != nil {
		value := incomplete.Incomplete
		value.affectedOperations = append([]changeplan.Operation(nil), value.affectedOperations...)
		result.IncompleteOperation = &value
	}
	return result, err
}
