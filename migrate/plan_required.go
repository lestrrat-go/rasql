package migrate

import (
	"context"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
)

type planRunState struct {
	prepared preparedChangePlan
	history  resolvedPlanHistory
	store    planProgressStore
}

type planGroupResult struct {
	completed []changeplan.Operation
}

type planApplyBoundary interface {
	executor
	queryer
	readCatalog(context.Context, preparedChangePlan, resolvedPlanHistory, int) (changeplan.Catalog, changeplan.Digest, error)
	readCatalogCandidates(context.Context, preparedChangePlan, resolvedPlanHistory, int) (planCatalogCandidates, error)
	Commit(context.Context) error
	Rollback(context.Context) error
}

func runRequiredPlanGroup(
	ctx context.Context,
	boundary planApplyBoundary,
	run planRunState,
	verified verifiedPlanState,
	start, end int,
) (planGroupResult, error) {
	if boundary == nil || start < 0 || end <= start || end > len(run.prepared.operations) || verified.nextIndex != start {
		return planGroupResult{}, rollbackRequiredBoundary(boundary, errors.New("migrate: invalid required plan group"), nil)
	}
	for index := start; index < end; index++ {
		if run.prepared.operations[index].mode != planModeRequired {
			return planGroupResult{}, rollbackRequiredBoundary(boundary, errors.New("migrate: required group contains forbidden work"), nil)
		}
	}
	switch verified.progressAction {
	case planProgressInsert:
		if err := run.store.ensure(ctx, boundary); err != nil {
			return planGroupResult{}, rollbackRequiredBoundary(boundary, err, nil)
		}
		if err := run.store.insert(ctx, boundary, newPlanProgressEntry(run.prepared, 0)); err != nil {
			return planGroupResult{}, rollbackRequiredBoundary(boundary, err, nil)
		}
	case planProgressReplace:
		if err := run.store.replace(ctx, boundary, verified.replacedPlanID, newPlanProgressEntry(run.prepared, 0)); err != nil {
			return planGroupResult{}, rollbackRequiredBoundary(boundary, err, nil)
		}
	}
	affected := 0
	for index := start; index < end; index++ {
		operation := run.prepared.operations[index].operation
		if err := ctx.Err(); err != nil {
			return planGroupResult{}, requiredBoundaryFailure(boundary, run.prepared, start, end, index,
				ChangePlanStageStatement, 0, affected, err)
		}
		before, digest, err := boundary.readCatalog(ctx, run.prepared, run.history, index)
		if err == nil && digest != run.prepared.operations[index].beforeDigest {
			err = errors.New("migrate: live catalog differs from required operation prefix")
		}
		if err == nil {
			err = changeplan.EvaluateFacts(before, operation.Preconditions())
		}
		if err != nil {
			if affected == 0 {
				return planGroupResult{}, rollbackRequiredBoundary(boundary, err, nil)
			}
			return planGroupResult{}, requiredBoundaryFailure(boundary, run.prepared, start, end, index,
				ChangePlanStagePreconditions, -1, affected, err)
		}
		for statementIndex, statement := range operation.Statements() {
			if err := ctx.Err(); err != nil {
				return planGroupResult{}, requiredBoundaryFailure(boundary, run.prepared, start, end, index,
					ChangePlanStageStatement, statementIndex, affected, err)
			}
			if affected == index-start {
				affected++
			}
			if _, err := boundary.ExecContext(ctx, statement.SQL(), statement.BoundArgs()...); err != nil {
				return planGroupResult{}, requiredBoundaryFailure(boundary, run.prepared, start, end, index,
					ChangePlanStageStatement, statementIndex, affected, err)
			}
		}
		if err := ctx.Err(); err != nil {
			return planGroupResult{}, requiredBoundaryFailure(boundary, run.prepared, start, end, index,
				ChangePlanStagePostconditions, -1, affected, err)
		}
		after, digest, err := boundary.readCatalog(ctx, run.prepared, run.history, index+1)
		if err == nil {
			err = changeplan.EvaluateFacts(after, operation.Postconditions())
		}
		if err == nil && digest != operation.ResultDigest() {
			err = errors.New("migrate: required operation result digest differs from plan")
		}
		if err != nil {
			return planGroupResult{}, requiredBoundaryFailure(boundary, run.prepared, start, end, index,
				ChangePlanStagePostconditions, -1, affected, err)
		}
		if err := ctx.Err(); err != nil {
			return planGroupResult{}, requiredBoundaryFailure(boundary, run.prepared, start, end, index,
				ChangePlanStageCheckpoint, -1, affected, err)
		}
		if err := run.store.update(ctx, boundary, newPlanProgressEntry(run.prepared, index+1)); err != nil {
			return planGroupResult{}, requiredBoundaryFailure(boundary, run.prepared, start, end, index,
				ChangePlanStageCheckpoint, -1, affected, err)
		}
	}
	if err := ctx.Err(); err != nil {
		return planGroupResult{}, requiredBoundaryFailure(boundary, run.prepared, start, end, end-1,
			ChangePlanStageCommit, -1, affected, err)
	}
	if err := boundary.Commit(ctx); err != nil {
		incomplete, buildErr := newIncompleteOperation(run.prepared, start, end, end-1,
			ChangePlanStageCommit, -1, affected, ChangePlanOperationUnknown)
		if buildErr != nil {
			return planGroupResult{}, errors.Join(err, buildErr)
		}
		return planGroupResult{}, newIncompleteChangePlanError(incomplete, err)
	}
	completed := make([]changeplan.Operation, end-start)
	for index := range completed {
		completed[index] = run.prepared.operations[start+index].operation
	}
	return planGroupResult{completed: completed}, nil
}

func newPlanProgressEntry(prepared preparedChangePlan, index int) planProgressEntry {
	digest, _ := prepared.expectedPrefixDigest(index)
	return newPlanProgressEntryWithDigest(prepared, index, digest)
}

func newPlanProgressEntryWithDigest(
	prepared preparedChangePlan,
	index int,
	digest changeplan.Digest,
) planProgressEntry {
	checkpoint, _ := changeplan.NewCheckpoint(prepared.id, index, digest)
	return planProgressEntry{checkpoint: checkpoint, operationCount: len(prepared.operations)}
}

func requiredBoundaryFailure(
	boundary planApplyBoundary,
	prepared preparedChangePlan,
	start, end, operationIndex int,
	stage ChangePlanOperationStage,
	statementIndex, affected int,
	cause error,
) error {
	if affected == 0 {
		return rollbackRequiredBoundary(boundary, cause, nil)
	}
	cleanupCtx, cancel := planCleanupContext()
	rollbackErr := boundary.Rollback(cleanupCtx)
	cancel()
	certainty := ChangePlanOperationRolledBack
	if rollbackErr != nil {
		certainty = ChangePlanOperationUnknown
	}
	incomplete, err := newIncompleteOperation(prepared, start, end, operationIndex, stage, statementIndex, affected, certainty)
	if err != nil {
		return errors.Join(cause, rollbackErr, err)
	}
	return newIncompleteChangePlanError(incomplete, errors.Join(cause, rollbackErr))
}

func rollbackRequiredBoundary(boundary planApplyBoundary, cause, extra error) error {
	if boundary == nil {
		return errors.Join(cause, extra)
	}
	cleanupCtx, cancel := planCleanupContext()
	rollbackErr := boundary.Rollback(cleanupCtx)
	cancel()
	return errors.Join(cause, extra, rollbackErr)
}

func planCleanupContext() (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(context.Background()), changePlanLockReleaseTimeout)
}

func maximalRequiredPlanEnd(prepared preparedChangePlan, start int) (int, error) {
	if start < 0 || start >= len(prepared.operations) || prepared.operations[start].mode != planModeRequired {
		return 0, fmt.Errorf("migrate: operation %d does not start a required group", start)
	}
	end := start + 1
	for end < len(prepared.operations) && prepared.operations[end].mode == planModeRequired {
		end++
	}
	return end, nil
}
