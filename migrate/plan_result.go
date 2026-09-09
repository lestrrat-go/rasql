package migrate

import (
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

func newIncompleteOperation(prepared preparedChangePlan, groupStart, groupEnd, operationIndex int, stage ChangePlanOperationStage, statementIndex, affectedCount int, certainty ChangePlanOperationCertainty) (IncompleteOperation, error) {
	if prepared.id == (changeplan.PlanID{}) {
		return IncompleteOperation{}, fmt.Errorf("migrate: incomplete operation requires a plan ID")
	}
	if groupStart < 0 || groupEnd <= groupStart || groupEnd > len(prepared.operations) {
		return IncompleteOperation{}, fmt.Errorf("migrate: incomplete operation group is outside the prepared schedule")
	}
	if operationIndex < groupStart || operationIndex >= groupEnd {
		return IncompleteOperation{}, fmt.Errorf("migrate: incomplete operation index is outside its group")
	}
	if !validChangePlanStage(stage) || !validChangePlanCertainty(certainty) {
		return IncompleteOperation{}, fmt.Errorf("migrate: incomplete operation stage or certainty is invalid")
	}
	groupMode := prepared.operations[groupStart].mode
	switch groupMode {
	case planModeRequired:
		for index := groupStart; index < groupEnd; index++ {
			if prepared.operations[index].mode != planModeRequired {
				return IncompleteOperation{}, fmt.Errorf("migrate: required operation group contains a forbidden operation")
			}
		}
	case planModeForbidden:
		if groupEnd != groupStart+1 || prepared.operations[groupEnd-1].mode != planModeForbidden {
			return IncompleteOperation{}, fmt.Errorf("migrate: forbidden operation group must contain one operation")
		}
	default:
		return IncompleteOperation{}, fmt.Errorf("migrate: incomplete operation group has unknown mode")
	}
	if affectedCount <= 0 || affectedCount > groupEnd-groupStart {
		return IncompleteOperation{}, fmt.Errorf("migrate: affected operation count is outside its group")
	}
	if stage == ChangePlanStageStatement {
		statements := prepared.operations[operationIndex].operation.Statements()
		if statementIndex < 0 || statementIndex >= len(statements) {
			return IncompleteOperation{}, fmt.Errorf("migrate: statement index is outside the named operation")
		}
	} else if statementIndex != -1 {
		return IncompleteOperation{}, fmt.Errorf("migrate: non-statement stage requires statement index -1")
	}
	namedIsFinal := operationIndex == groupStart+affectedCount-1
	if stage == ChangePlanStagePreconditions {
		if affectedCount == groupEnd-groupStart || operationIndex != groupStart+affectedCount {
			return IncompleteOperation{}, fmt.Errorf("migrate: preconditions must name the immediate next operation")
		}
	} else if stage == ChangePlanStageStatement && statementIndex == 0 && operationIndex == groupStart+affectedCount {
		// A cancellation before statement zero leaves the immediate next operation untouched.
	} else if !namedIsFinal {
		return IncompleteOperation{}, fmt.Errorf("migrate: %s failure must affect the named operation", stage)
	}
	if stage == ChangePlanStageCommit {
		if operationIndex != groupEnd-1 || affectedCount != groupEnd-groupStart {
			return IncompleteOperation{}, fmt.Errorf("migrate: commit must include the complete operation group")
		}
		// A canceled commit may be rolled back successfully, while a driver or rollback failure stays unknown.
	}
	if groupMode == planModeForbidden && certainty == ChangePlanOperationRolledBack {
		return IncompleteOperation{}, fmt.Errorf("migrate: forbidden operation cannot be rolled back")
	}
	operation := prepared.operations[operationIndex].operation
	affected := make([]changeplan.Operation, affectedCount)
	for index := range affected {
		affected[index] = prepared.operations[groupStart+index].operation
	}
	return IncompleteOperation{
		planID:             prepared.id,
		operationIndex:     operationIndex,
		operation:          operation,
		stage:              stage,
		statementIndex:     statementIndex,
		affectedOperations: affected,
		certainty:          certainty,
	}, nil
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
	// incomplete is always produced by newIncompleteOperation, which already validates every
	// invariant below except this one: a zero-value plan ID only occurs when a caller
	// constructs an IncompleteOperation by hand rather than through that constructor.
	if incomplete.planID == (changeplan.PlanID{}) {
		return fmt.Errorf("migrate: incomplete operation requires a plan ID")
	}
	incomplete.affectedOperations = append([]changeplan.Operation(nil), incomplete.affectedOperations...)
	return &IncompleteChangePlanError{Incomplete: incomplete, Cause: cause}
}

func changePlanExecutionResult(completed []changeplan.Operation, err error) (ExecutionResult, error) {
	result := ExecutionResult{CompletedOperations: append([]changeplan.Operation(nil), completed...)}
	if incomplete := primaryIncompleteChangePlanError(err); incomplete != nil {
		value := incomplete.Incomplete
		value.affectedOperations = append([]changeplan.Operation(nil), value.affectedOperations...)
		result.IncompleteOperation = &value
	}
	return result, err
}

func primaryIncompleteChangePlanError(err error) *IncompleteChangePlanError {
	if err == nil {
		return nil
	}
	if incomplete, ok := err.(*IncompleteChangePlanError); ok {
		return incomplete
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return nil
		}
		return primaryIncompleteChangePlanError(causes[0])
	}
	if wrapped, ok := err.(interface{ Unwrap() error }); ok {
		return primaryIncompleteChangePlanError(wrapped.Unwrap())
	}
	return nil
}
