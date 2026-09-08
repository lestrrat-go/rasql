package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
)

type forbiddenPlanResult struct {
	completed *changeplan.Operation
}

func runForbiddenPlanOperation(
	ctx context.Context,
	connection *sql.Conn,
	run planRunState,
	boundary planApplyBoundary,
) (forbiddenPlanResult, error) {
	queries := queryer(connection)
	if boundary != nil {
		queries = boundary
	}
	entry, legacy, _, err := readClassifiedPlanRows(ctx, planJournalRunner(run),
		queries, run.prepared, run.history, run.store)
	if err != nil {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary, err)
	}
	if legacy != nil {
		return recoverForbiddenPlanOperation(ctx, connection, run, boundary, entry, legacy)
	}
	classified, err := classifyPlanProgress(run.prepared, entry)
	if err != nil {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary, err)
	}
	prefix := 0
	if classified.classification == planProgressSame {
		prefix = classified.entry.checkpoint.NextIndex()
	}
	catalog, digest, err := readForbiddenCatalog(ctx, connection, boundary, run, prefix)
	if err != nil {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary, err)
	}
	state, err := verifyPlanState(run.prepared, entry, nil, catalog, digest)
	if err != nil {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary, err)
	}
	if state.complete || run.prepared.operations[state.nextIndex].mode != planModeForbidden {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary, errors.New("migrate: selected operation is not forbidden"))
	}
	operation := run.prepared.operations[state.nextIndex]
	if err := changeplan.EvaluateFacts(catalog, operation.operation.Preconditions()); err != nil {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary, err)
	}
	if err := installForbiddenPrefix(ctx, connection, run, boundary, state); err != nil {
		return forbiddenPlanResult{}, err
	}
	return executeForbiddenStatements(ctx, connection, run, operation, 0)
}

func recoverForbiddenPlanOperation(
	ctx context.Context,
	connection *sql.Conn,
	run planRunState,
	boundary planApplyBoundary,
	entry *planProgressEntry,
	legacy *progressEntry,
) (forbiddenPlanResult, error) {
	if err := validateJournalPlanProgress(run.prepared, entry); err != nil {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary,
			planReconciliation(run.prepared, "", legacy.id, err))
	}
	operationIndex, migration, err := matchingPlanJournal(run.prepared, legacy)
	if err != nil {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary,
			planReconciliation(run.prepared, "", legacy.id, err))
	}
	operation := run.prepared.operations[operationIndex]
	next := entry.checkpoint.NextIndex()
	if next != operationIndex && next != operationIndex+1 {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary,
			planReconciliation(run.prepared, operation.operation.ID(), legacy.id, errors.New("journal operation differs from checkpoint")))
	}
	if next == operationIndex+1 {
		if entry.checkpoint.CatalogDigest() != operation.afterDigest {
			return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary,
				planReconciliation(run.prepared, operation.operation.ID(), legacy.id,
					errors.New("stored digest differs from completed operation")))
		}
		catalog, digest, err := readForbiddenCatalog(ctx, connection, boundary, run, next)
		if err == nil {
			err = operationFactsMatch(catalog, operation.operation.Postconditions(), digest, operation.afterDigest)
		}
		if err != nil {
			return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary,
				planReconciliation(run.prepared, operation.operation.ID(), legacy.id, err))
		}
		if err := deleteMatchingJournal(ctx, connection, boundary, run, migration.id); err != nil {
			return forbiddenPlanResult{}, err
		}
		return forbiddenPlanResult{}, nil
	}
	if legacy.nextIndex > legacy.sourceIndex {
		candidates, err := readForbiddenCandidates(ctx, connection, boundary, run, operationIndex)
		if err == nil {
			_, digest, digestErr := currentForbiddenCatalog(candidates)
			err = digestErr
			if err == nil && digest != entry.checkpoint.CatalogDigest() {
				err = errors.New("live catalog differs from durable statement checkpoint")
			}
		}
		if err != nil {
			return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary,
				planReconciliation(run.prepared, operation.operation.ID(), legacy.id, err))
		}
		if err := commitForbiddenDecision(ctx, boundary); err != nil {
			return forbiddenPlanResult{}, err
		}
		return executeForbiddenStatements(ctx, connection, run, operation, legacy.nextIndex)
	}
	candidates, err := readForbiddenCandidates(ctx, connection, boundary, run, operationIndex)
	if err != nil {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary,
			planReconciliation(run.prepared, operation.operation.ID(), legacy.id, err))
	}
	before, beforeDigest, beforeErr := currentForbiddenCatalog(candidates)
	if beforeErr == nil && beforeDigest != entry.checkpoint.CatalogDigest() {
		beforeErr = errors.New("live catalog differs from durable statement checkpoint")
	}
	if beforeErr == nil && legacy.sourceIndex == 0 {
		beforeErr = changeplan.EvaluateFacts(before, operation.operation.Preconditions())
	}
	afterErr := errors.New("nonterminal statement outcome is not provable")
	if legacy.sourceIndex+1 == len(migration.statements) {
		afterErr = candidates.afterErr
	}
	if legacy.sourceIndex+1 == len(migration.statements) && afterErr == nil {
		afterErr = operationFactsMatch(candidates.after, operation.operation.Postconditions(), candidates.afterDigest, operation.afterDigest)
	}
	beforeOK := beforeErr == nil
	afterOK := afterErr == nil
	if beforeOK == afterOK {
		return forbiddenPlanResult{}, rollbackForbiddenDecision(boundary, planReconciliation(run.prepared,
			operation.operation.ID(), legacy.id, errors.Join(fmt.Errorf("before state: %w", beforeErr), fmt.Errorf("after state: %w", afterErr))))
	}
	if afterOK {
		if err := finishRecoveredForbidden(ctx, connection, boundary, run, operationIndex, migration.id); err != nil {
			return forbiddenPlanResult{}, err
		}
		completed := operation.operation
		return forbiddenPlanResult{completed: &completed}, nil
	}
	if err := restorePlanJournal(ctx, connection, boundary, run, migration, legacy.nextIndex); err != nil {
		return forbiddenPlanResult{}, err
	}
	return executeForbiddenStatements(ctx, connection, run, operation, legacy.sourceIndex)
}

func validateJournalPlanProgress(prepared preparedChangePlan, entry *planProgressEntry) error {
	if entry == nil {
		return errors.New("journal lacks plan checkpoint")
	}
	if entry.checkpoint.PlanID() != prepared.id {
		return errors.New("journal lacks same-plan checkpoint")
	}
	if entry.operationCount != len(prepared.operations) {
		return errors.New("journal operation count differs from plan")
	}
	next := entry.checkpoint.NextIndex()
	if next < 0 || next > entry.operationCount {
		return errors.New("journal plan checkpoint is outside operation range")
	}
	return nil
}

func currentForbiddenCatalog(
	candidates planCatalogCandidates,
) (changeplan.Catalog, changeplan.Digest, error) {
	if candidates.beforeErr == nil && candidates.afterErr == nil {
		if candidates.beforeDigest != candidates.afterDigest {
			return changeplan.Catalog{}, changeplan.Digest{},
				errors.New("live catalog has ambiguous operation identity")
		}
		return candidates.before, candidates.beforeDigest, nil
	}
	if candidates.beforeErr == nil {
		return candidates.before, candidates.beforeDigest, nil
	}
	if candidates.afterErr == nil {
		return candidates.after, candidates.afterDigest, nil
	}
	return changeplan.Catalog{}, changeplan.Digest{}, errors.Join(
		fmt.Errorf("before identity: %w", candidates.beforeErr),
		fmt.Errorf("after identity: %w", candidates.afterErr),
	)
}

func installForbiddenPrefix(
	ctx context.Context,
	connection *sql.Conn,
	run planRunState,
	boundary planApplyBoundary,
	state verifiedPlanState,
) error {
	if state.progressAction == planProgressNone {
		return commitForbiddenDecision(ctx, boundary)
	}
	if boundary != nil {
		if state.progressAction == planProgressInsert {
			if err := run.store.ensure(ctx, boundary); err != nil {
				return rollbackForbiddenDecision(boundary, err)
			}
			if err := run.store.insert(ctx, boundary, newPlanProgressEntry(run.prepared, 0)); err != nil {
				return rollbackForbiddenDecision(boundary, err)
			}
		} else if err := run.store.replace(ctx, boundary, state.replacedPlanID, newPlanProgressEntry(run.prepared, 0)); err != nil {
			return rollbackForbiddenDecision(boundary, err)
		}
		return commitForbiddenDecision(ctx, boundary)
	}
	if state.progressAction == planProgressInsert {
		if err := run.store.ensure(ctx, connection); err != nil {
			return err
		}
	}
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if state.progressAction == planProgressInsert {
		err = run.store.insert(ctx, tx, newPlanProgressEntry(run.prepared, 0))
	} else {
		err = run.store.replace(ctx, tx, state.replacedPlanID, newPlanProgressEntry(run.prepared, 0))
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}

func executeForbiddenStatements(
	ctx context.Context,
	connection *sql.Conn,
	run planRunState,
	operation preparedPlanOperation,
	startStatement int,
) (forbiddenPlanResult, error) {
	if err := runRunnerEnsureProgress(ctx, connection, run); err != nil {
		return forbiddenPlanResult{}, err
	}
	migration := journalMigration(run.prepared, operation)
	sqlStarted := startStatement > 0
	for index := startStatement; index < len(operation.operation.Statements()); index++ {
		if err := ctx.Err(); err != nil {
			if !sqlStarted {
				return forbiddenPlanResult{}, err
			}
			return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
				ChangePlanStageStatement, index, err)
		}
		if err := writePlanJournalIntent(ctx, connection, run, migration, index); err != nil {
			if !sqlStarted {
				return forbiddenPlanResult{}, err
			}
			return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
				ChangePlanStageCheckpoint, -1, err)
		}
		if err := ctx.Err(); err != nil {
			cleanupErr := restorePlanJournal(ctx, connection, nil, run, migration, index)
			if !sqlStarted {
				return forbiddenPlanResult{}, errors.Join(err, cleanupErr)
			}
			return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
				ChangePlanStageStatement, index, errors.Join(err, cleanupErr))
		}
		statement := operation.operation.Statements()[index]
		sqlStarted = true
		if _, err := connection.ExecContext(ctx, statement.SQL(), statement.BoundArgs()...); err != nil {
			return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
				ChangePlanStageStatement, index, err)
		}
		if err := ctx.Err(); err != nil {
			return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
				ChangePlanStageCheckpoint, -1, err)
		}
		if err := checkpointPlanJournal(ctx, connection, run, operation, migration, index); err != nil {
			return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
				ChangePlanStageCheckpoint, -1, err)
		}
	}
	return finishForbiddenOperation(ctx, connection, run, operation)
}

func finishForbiddenOperation(
	ctx context.Context,
	connection *sql.Conn,
	run planRunState,
	operation preparedPlanOperation,
) (forbiddenPlanResult, error) {
	boundary, err := beginPlanBoundary(ctx, connection, run.prepared.profile.Engine)
	if err != nil {
		return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
			ChangePlanStagePostconditions, -1, err)
	}
	catalog, digest, err := readForbiddenCatalog(ctx, connection, boundary, run, operation.index+1)
	if err == nil {
		err = operationFactsMatch(catalog, operation.operation.Postconditions(), digest, operation.afterDigest)
	}
	if err != nil {
		return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
			ChangePlanStagePostconditions, -1, rollbackForbiddenDecision(boundary, err))
	}
	if err := run.store.update(ctx, boundaryOrConnection(boundary, connection), newPlanProgressEntry(run.prepared, operation.index+1)); err != nil {
		return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
			ChangePlanStageCheckpoint, -1, rollbackForbiddenDecision(boundary, err))
	}
	migration := journalMigration(run.prepared, operation)
	if err := deletePlanJournal(ctx, boundaryOrConnection(boundary, connection), run, migration.id); err != nil {
		commitErr := commitForbiddenDecision(ctx, boundary)
		completed := operation.operation
		return forbiddenPlanResult{completed: &completed}, errors.Join(err, commitErr)
	}
	if err := commitForbiddenDecision(ctx, boundary); err != nil {
		return forbiddenPlanResult{}, forbiddenIncomplete(run.prepared, operation.index,
			ChangePlanStageCommit, -1, err)
	}
	completed := operation.operation
	return forbiddenPlanResult{completed: &completed}, nil
}

func readForbiddenCatalog(
	ctx context.Context,
	connection *sql.Conn,
	boundary planApplyBoundary,
	run planRunState,
	prefix int,
) (changeplan.Catalog, changeplan.Digest, error) {
	if boundary != nil {
		return boundary.readCatalog(ctx, run.prepared, run.history, prefix)
	}
	return readPlanCatalogSnapshot(ctx, connection, run.prepared, run.history, prefix)
}

func readForbiddenCandidates(
	ctx context.Context,
	connection *sql.Conn,
	boundary planApplyBoundary,
	run planRunState,
	prefix int,
) (planCatalogCandidates, error) {
	if boundary != nil {
		return boundary.readCatalogCandidates(ctx, run.prepared, run.history, prefix)
	}
	return readPlanCatalogCandidatesSnapshot(ctx, connection, run.prepared, run.history, prefix)
}

func beginPlanBoundary(ctx context.Context, connection *sql.Conn, engine changeplan.EngineID) (planApplyBoundary, error) {
	switch engine {
	case changeplan.SQLiteEngine:
		return beginSQLitePlanBoundary(ctx, connection)
	case changeplan.PostgreSQLEngine:
		return beginSQLTxPlanBoundary(ctx, connection)
	case changeplan.MySQLEngine:
		return nil, nil
	default:
		return nil, errors.New("migrate: unsupported plan engine")
	}
}

func runRunnerEnsureProgress(ctx context.Context, connection *sql.Conn, run planRunState) error {
	runner := planJournalRunner(run)
	return runner.ensureProgress(ctx, connection)
}

func writePlanJournalIntent(ctx context.Context, connection *sql.Conn, run planRunState, migration preparedMigration, index int) error {
	runner := planJournalRunner(run)
	if run.prepared.profile.Engine != changeplan.SQLiteEngine {
		return runner.upsertProgress(ctx, connection, migration, DirectionUp, index)
	}
	boundary, err := beginSQLitePlanBoundary(ctx, connection)
	if err != nil {
		return err
	}
	if err := deletePlanJournal(ctx, boundary, run, migration.id); err != nil {
		return rollbackForbiddenDecision(boundary, err)
	}
	if err := runner.upsertProgress(ctx, boundary, migration, DirectionUp, index); err != nil {
		return rollbackForbiddenDecision(boundary, err)
	}
	return boundary.Commit(ctx)
}

func checkpointPlanJournal(
	ctx context.Context,
	connection *sql.Conn,
	run planRunState,
	operation preparedPlanOperation,
	migration preparedMigration,
	index int,
) error {
	runner := planJournalRunner(run)
	boundary, err := beginForbiddenCheckpointBoundary(ctx, connection, run.prepared.profile.Engine)
	if err != nil {
		return err
	}
	candidates, err := boundary.readCatalogCandidates(ctx, run.prepared, run.history, operation.index)
	if err != nil {
		return rollbackForbiddenDecision(boundary, err)
	}
	_, digest, err := currentForbiddenCatalog(candidates)
	if err != nil {
		return rollbackForbiddenDecision(boundary, err)
	}
	if err := runner.checkpointProgress(ctx, boundary, migration, DirectionUp, index); err != nil {
		return rollbackForbiddenDecision(boundary, err)
	}
	entry := newPlanProgressEntryWithDigest(run.prepared, operation.index, digest)
	if err := run.store.update(ctx, boundary, entry); err != nil {
		return rollbackForbiddenDecision(boundary, err)
	}
	return boundary.Commit(ctx)
}

func beginForbiddenCheckpointBoundary(
	ctx context.Context,
	connection *sql.Conn,
	engine changeplan.EngineID,
) (planApplyBoundary, error) {
	if engine == changeplan.MySQLEngine {
		return beginSQLTxPlanBoundary(ctx, connection)
	}
	return beginPlanBoundary(ctx, connection, engine)
}

func restorePlanJournal(
	ctx context.Context,
	connection *sql.Conn,
	boundary planApplyBoundary,
	run planRunState,
	migration preparedMigration,
	nextIndex int,
) error {
	runner := planJournalRunner(run)
	owner := boundary
	if owner == nil && run.prepared.profile.Engine == changeplan.SQLiteEngine {
		var err error
		owner, err = beginSQLitePlanBoundary(ctx, connection)
		if err != nil {
			return err
		}
	}
	execution := boundaryOrConnection(owner, connection)
	var err error
	if nextIndex == 0 {
		err = deletePlanJournal(ctx, execution, run, migration.id)
	} else {
		if run.prepared.profile.Engine == changeplan.SQLiteEngine {
			err = deletePlanJournal(ctx, execution, run, migration.id)
		}
		if err == nil {
			err = runner.upsertProgress(ctx, execution, migration, DirectionUp, nextIndex-1)
		}
		if err == nil {
			err = runner.checkpointProgress(ctx, execution, migration, DirectionUp, nextIndex-1)
		}
	}
	if err != nil {
		return rollbackForbiddenDecision(owner, err)
	}
	return commitForbiddenDecision(ctx, owner)
}

func finishRecoveredForbidden(
	ctx context.Context,
	connection *sql.Conn,
	boundary planApplyBoundary,
	run planRunState,
	operationIndex int,
	journalID string,
) error {
	execution := boundaryOrConnection(boundary, connection)
	if err := run.store.update(ctx, execution, newPlanProgressEntry(run.prepared, operationIndex+1)); err != nil {
		return rollbackForbiddenDecision(boundary, err)
	}
	if err := deletePlanJournal(ctx, execution, run, journalID); err != nil {
		return rollbackForbiddenDecision(boundary, err)
	}
	return commitForbiddenDecision(ctx, boundary)
}

func deleteMatchingJournal(
	ctx context.Context,
	connection *sql.Conn,
	boundary planApplyBoundary,
	run planRunState,
	journalID string,
) error {
	if err := deletePlanJournal(ctx, boundaryOrConnection(boundary, connection), run, journalID); err != nil {
		return rollbackForbiddenDecision(boundary, err)
	}
	return commitForbiddenDecision(ctx, boundary)
}

func deletePlanJournal(ctx context.Context, execution executor, run planRunState, journalID string) error {
	return planJournalRunner(run).deleteProgress(ctx, execution, journalID)
}

func planJournalRunner(run planRunState) Runner {
	return Runner{database: nil, dialect: run.store.dialect, historyTable: run.prepared.history.Table(),
		progressSQL: mustProgressSQL(run.store.dialect, run.prepared.history.Table()), idSQL: mustQuote(run.store.dialect, "id"),
		checksumSQL: mustQuote(run.store.dialect, "checksum")}
}

func mustProgressSQL(d interface{ QuoteIdentifier(string) (string, error) }, history string) string {
	value, _ := d.QuoteIdentifier(history + "_progress")
	return value
}

func mustQuote(d interface{ QuoteIdentifier(string) (string, error) }, name string) string {
	value, _ := d.QuoteIdentifier(name)
	return value
}

func boundaryOrConnection(boundary planApplyBoundary, connection *sql.Conn) executor {
	if boundary != nil {
		return boundary
	}
	return connection
}

func commitForbiddenDecision(ctx context.Context, boundary planApplyBoundary) error {
	if boundary == nil {
		return nil
	}
	return boundary.Commit(ctx)
}

func rollbackForbiddenDecision(boundary planApplyBoundary, cause error) error {
	if boundary == nil {
		return cause
	}
	cleanupCtx, cancel := planCleanupContext()
	err := boundary.Rollback(cleanupCtx)
	cancel()
	return errors.Join(cause, err)
}

func forbiddenIncomplete(
	prepared preparedChangePlan,
	operationIndex int,
	stage ChangePlanOperationStage,
	statementIndex int,
	cause error,
) error {
	incomplete, err := newIncompleteOperation(prepared, operationIndex, operationIndex+1, operationIndex,
		stage, statementIndex, 1, ChangePlanOperationUnknown)
	if err != nil {
		return errors.Join(cause, err)
	}
	return newIncompleteChangePlanError(incomplete, cause)
}
