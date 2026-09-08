package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
)

type ChangePlanCheck struct {
	planID      changeplan.PlanID
	nextIndex   int
	lastCatalog changeplan.Digest
	complete    bool
}

func (c ChangePlanCheck) PlanID() changeplan.PlanID            { return c.planID }
func (c ChangePlanCheck) NextOperationIndex() int              { return c.nextIndex }
func (c ChangePlanCheck) LastCatalogDigest() changeplan.Digest { return c.lastCatalog }
func (c ChangePlanCheck) Complete() bool                       { return c.complete }

type planProgressAction uint8

const (
	planProgressNone planProgressAction = iota
	planProgressInsert
	planProgressUpdate
	planProgressReplace
)

type verifiedPlanState struct {
	nextIndex      int
	digest         changeplan.Digest
	complete       bool
	progressAction planProgressAction
	replacedPlanID changeplan.PlanID
}

type planProgressClassification uint8

const (
	planProgressAbsent planProgressClassification = iota + 1
	planProgressSame
	planProgressSupersessionCandidate
)

type classifiedPlanProgress struct {
	entry          *planProgressEntry
	classification planProgressClassification
}

func classifyPlanProgress(prepared preparedChangePlan, entry *planProgressEntry) (classifiedPlanProgress, error) {
	if entry == nil {
		return classifiedPlanProgress{classification: planProgressAbsent}, nil
	}
	copyEntry := *entry
	planID := entry.checkpoint.PlanID()
	if entry.operationCount < 0 || entry.checkpoint.NextIndex() < 0 || entry.checkpoint.NextIndex() > entry.operationCount {
		return classifiedPlanProgress{}, planReconciliation(prepared, "", planID.String(), errors.New("invalid plan progress range"))
	}
	if planID != prepared.id {
		if entry.checkpoint.NextIndex() != entry.operationCount {
			return classifiedPlanProgress{}, planReconciliation(prepared, "", planID.String(), errors.New("different plan is incomplete"))
		}
		return classifiedPlanProgress{entry: &copyEntry, classification: planProgressSupersessionCandidate}, nil
	}
	if entry.operationCount != len(prepared.operations) {
		return classifiedPlanProgress{}, planReconciliation(prepared, "", planID.String(), errors.New("operation count differs from plan"))
	}
	want, err := prepared.expectedPrefixDigest(entry.checkpoint.NextIndex())
	if err != nil || entry.checkpoint.CatalogDigest() != want {
		return classifiedPlanProgress{}, planReconciliation(prepared, "", planID.String(), errors.New("stored digest differs from plan prefix"))
	}
	return classifiedPlanProgress{entry: &copyEntry, classification: planProgressSame}, nil
}

func verifyPlanState(
	prepared preparedChangePlan,
	entry *planProgressEntry,
	legacy *progressEntry,
	live changeplan.Catalog,
	liveDigest changeplan.Digest,
) (verifiedPlanState, error) {
	classified, err := classifyPlanProgress(prepared, entry)
	if err != nil {
		return verifiedPlanState{}, err
	}
	if legacy != nil {
		return verifiedPlanState{}, planReconciliation(prepared, "", legacy.id, errors.New("legacy migration progress exists"))
	}
	baseline := prepared.baseline.Catalog().CatalogDigest()
	switch classified.classification {
	case planProgressAbsent:
		if liveDigest != baseline {
			return verifiedPlanState{}, planReconciliation(prepared, "", "", errors.New("live catalog differs from plan baseline"))
		}
		return verifiedPlanState{nextIndex: 0, digest: liveDigest, complete: len(prepared.operations) == 0,
			progressAction: planProgressInsert}, nil
	case planProgressSame:
		if liveDigest != classified.entry.checkpoint.CatalogDigest() {
			return verifiedPlanState{}, planReconciliation(prepared, "", prepared.id.String(), errors.New("live catalog differs from stored checkpoint"))
		}
		next := classified.entry.checkpoint.NextIndex()
		return verifiedPlanState{nextIndex: next, digest: liveDigest, complete: next == len(prepared.operations)}, nil
	case planProgressSupersessionCandidate:
		if classified.entry.checkpoint.CatalogDigest() != liveDigest || liveDigest != baseline {
			return verifiedPlanState{}, planReconciliation(prepared, "", classified.entry.checkpoint.PlanID().String(),
				errors.New("completed plan cannot be superseded at this catalog"))
		}
		if len(prepared.operations) > 0 {
			if err := changeplan.EvaluateFacts(live, prepared.operations[0].operation.Preconditions()); err != nil {
				return verifiedPlanState{}, planReconciliation(prepared, prepared.operations[0].operation.ID(),
					classified.entry.checkpoint.PlanID().String(), err)
			}
		}
		return verifiedPlanState{nextIndex: 0, digest: liveDigest, complete: len(prepared.operations) == 0,
			progressAction: planProgressReplace, replacedPlanID: classified.entry.checkpoint.PlanID()}, nil
	default:
		return verifiedPlanState{}, planReconciliation(prepared, "", "", errors.New("unknown plan progress classification"))
	}
}

func planReconciliation(
	prepared preparedChangePlan,
	operationID changeplan.OperationID,
	progressID string,
	cause error,
) error {
	return &ChangePlanReconciliationError{
		PlanID: prepared.id, OperationID: operationID, ProgressID: progressID, Cause: cause,
	}
}

func (r Runner) CheckChangePlan(ctx context.Context, plan changeplan.Plan) (ChangePlanCheck, error) {
	prepared, err := prepareChangePlan(r, plan)
	if err != nil {
		return ChangePlanCheck{}, err
	}
	connection, err := r.database.Conn(ctx)
	if err != nil {
		return ChangePlanCheck{}, fmt.Errorf("migrate: open database connection: %w", err)
	}
	defer func() { _ = connection.Close() }()
	var check ChangePlanCheck
	run := func() error {
		observed, err := engineprofile.Discover(ctx, connection, prepared.profile.Engine, prepared.profile.ID)
		if err != nil {
			return fmt.Errorf("migrate: discover plan profile: %w", err)
		}
		if observed != prepared.profile {
			return fmt.Errorf("migrate: live profile differs from change plan")
		}
		history, err := resolvePlanHistory(ctx, connection, prepared)
		if err != nil {
			return err
		}
		store, err := newPlanProgressStore(prepared, history)
		if err != nil {
			return err
		}
		if prepared.profile.Engine == changeplan.SQLiteEngine {
			return r.checkSQLitePlan(ctx, connection, prepared, history, store, &check)
		}
		return r.checkLockedPlan(ctx, connection, prepared, history, store, &check)
	}
	switch r.dialect.Name() {
	case "postgresql":
		err = r.withPostgreSQLReadLock(ctx, connection, run)
	case "mysql":
		err = r.withMySQLReadLock(ctx, connection, run)
	case "sqlite":
		err = run()
	default:
		err = fmt.Errorf("migrate: unsupported plan dialect %q", r.dialect.Name())
	}
	return check, err
}

func (r Runner) checkSQLitePlan(
	ctx context.Context,
	connection *sql.Conn,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	store planProgressStore,
	check *ChangePlanCheck,
) (retErr error) {
	tx, err := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("migrate: begin SQLite plan check: %w", err)
	}
	defer func() {
		cleanupErr := tx.Rollback()
		if cleanupErr != nil && !errors.Is(cleanupErr, sql.ErrTxDone) {
			retErr = errors.Join(retErr, fmt.Errorf("migrate: roll back SQLite plan check: %w", cleanupErr))
		}
	}()
	entry, legacy, prefix, err := readClassifiedPlanRows(ctx, r, tx, prepared, history, store)
	if err != nil {
		return err
	}
	if legacy != nil {
		return planReconciliation(prepared, "", legacy.id, errors.New("legacy migration progress exists"))
	}
	catalog, digest, err := readPlanCatalogTx(ctx, tx, prepared, history, prefix)
	if err != nil {
		return err
	}
	return finishPlanCheck(prepared, entry, legacy, catalog, digest, check)
}

func (r Runner) checkLockedPlan(
	ctx context.Context,
	connection *sql.Conn,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	store planProgressStore,
	check *ChangePlanCheck,
) error {
	entry, legacy, prefix, err := readClassifiedPlanRows(ctx, r, connection, prepared, history, store)
	if err != nil {
		return err
	}
	if legacy != nil {
		return planReconciliation(prepared, "", legacy.id, errors.New("legacy migration progress exists"))
	}
	catalog, digest, err := readPlanCatalogSnapshot(ctx, connection, prepared, history, prefix)
	if err != nil {
		return err
	}
	return finishPlanCheck(prepared, entry, legacy, catalog, digest, check)
}

func readClassifiedPlanRows(
	ctx context.Context,
	runner Runner,
	queries queryer,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	store planProgressStore,
) (*planProgressEntry, *progressEntry, int, error) {
	legacy, err := readLegacyProgressIfExists(ctx, runner, queries, history)
	if err != nil {
		return nil, nil, 0, err
	}
	exists, err := store.exists(ctx, queries)
	if err != nil {
		return nil, nil, 0, err
	}
	var entry *planProgressEntry
	if exists {
		entry, err = store.read(ctx, queries)
		if err != nil {
			return nil, nil, 0, err
		}
	}
	classified, err := classifyPlanProgress(prepared, entry)
	if err != nil {
		return nil, nil, 0, err
	}
	prefix := 0
	if classified.classification == planProgressSame {
		prefix = classified.entry.checkpoint.NextIndex()
	}
	return entry, legacy, prefix, nil
}

func finishPlanCheck(
	prepared preparedChangePlan,
	entry *planProgressEntry,
	legacy *progressEntry,
	catalog changeplan.Catalog,
	digest changeplan.Digest,
	check *ChangePlanCheck,
) error {
	state, err := verifyPlanState(prepared, entry, legacy, catalog, digest)
	if err != nil {
		return err
	}
	if !state.complete {
		operation := prepared.operations[state.nextIndex].operation
		if err := changeplan.EvaluateFacts(catalog, operation.Preconditions()); err != nil {
			return planReconciliation(prepared, operation.ID(), "", err)
		}
	}
	*check = ChangePlanCheck{planID: prepared.id, nextIndex: state.nextIndex, lastCatalog: state.digest, complete: state.complete}
	return nil
}
