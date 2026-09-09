package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
)

type sqlTxPlanBoundary struct {
	tx     *sql.Tx
	active bool
}

func beginSQLTxPlanBoundary(ctx context.Context, connection *sql.Conn) (*sqlTxPlanBoundary, error) {
	if connection == nil {
		return nil, errors.New("migrate: plan transaction requires a connection")
	}
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("migrate: begin plan transaction: %w", err)
	}
	return &sqlTxPlanBoundary{tx: tx, active: true}, nil
}

func (b *sqlTxPlanBoundary) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if b == nil || !b.active {
		return nil, errors.New("migrate: plan transaction is closed")
	}
	return b.tx.ExecContext(ctx, query, args...)
}
func (b *sqlTxPlanBoundary) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if b == nil || !b.active {
		return nil, errors.New("migrate: plan transaction is closed")
	}
	return b.tx.QueryContext(ctx, query, args...)
}
func (b *sqlTxPlanBoundary) readCatalog(
	ctx context.Context,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (changeplan.Catalog, changeplan.Digest, error) {
	if b == nil || !b.active {
		return changeplan.Catalog{}, changeplan.Digest{}, errors.New("migrate: plan transaction is closed")
	}
	return readPlanCatalogTx(ctx, b.tx, prepared, history, prefix)
}
func (b *sqlTxPlanBoundary) readCatalogCandidates(
	ctx context.Context,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (planCatalogCandidates, error) {
	if b == nil || !b.active {
		return planCatalogCandidates{}, errors.New("migrate: plan transaction is closed")
	}
	return readPlanCatalogCandidatesTx(ctx, b.tx, prepared, history, prefix)
}
func (b *sqlTxPlanBoundary) Commit(ctx context.Context) error {
	if b == nil || !b.active {
		return errors.New("migrate: plan transaction is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.active = false
	return b.tx.Commit()
}
func (b *sqlTxPlanBoundary) Rollback(context.Context) error {
	if b == nil || !b.active {
		return nil
	}
	b.active = false
	err := b.tx.Rollback()
	if errors.Is(err, sql.ErrTxDone) {
		return nil
	}
	return err
}

type sqlitePlanBoundary struct {
	connection *sql.Conn
	active     bool
}

func beginSQLitePlanBoundary(ctx context.Context, connection *sql.Conn) (*sqlitePlanBoundary, error) {
	if connection == nil {
		return nil, errors.New("migrate: SQLite plan transaction requires a connection")
	}
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("migrate: begin SQLite plan transaction: %w", err)
	}
	return &sqlitePlanBoundary{connection: connection, active: true}, nil
}

func (b *sqlitePlanBoundary) ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error) {
	if b == nil || !b.active {
		return nil, errors.New("migrate: SQLite plan transaction is closed")
	}
	return b.connection.ExecContext(ctx, query, args...)
}
func (b *sqlitePlanBoundary) QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error) {
	if b == nil || !b.active {
		return nil, errors.New("migrate: SQLite plan transaction is closed")
	}
	return b.connection.QueryContext(ctx, query, args...)
}
func (b *sqlitePlanBoundary) readCatalog(
	ctx context.Context,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (changeplan.Catalog, changeplan.Digest, error) {
	if b == nil || !b.active {
		return changeplan.Catalog{}, changeplan.Digest{}, errors.New("migrate: SQLite plan transaction is closed")
	}
	return readPlanCatalogConnTx(ctx, b.connection, prepared, history, prefix)
}
func (b *sqlitePlanBoundary) readCatalogCandidates(
	ctx context.Context,
	prepared preparedChangePlan,
	history resolvedPlanHistory,
	prefix int,
) (planCatalogCandidates, error) {
	if b == nil || !b.active {
		return planCatalogCandidates{}, errors.New("migrate: SQLite plan transaction is closed")
	}
	return readPlanCatalogCandidatesConnTx(ctx, b.connection, prepared, history, prefix)
}
func (b *sqlitePlanBoundary) Commit(ctx context.Context) error {
	if b == nil || !b.active {
		return errors.New("migrate: SQLite plan transaction is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	b.active = false
	_, err := b.connection.ExecContext(ctx, "COMMIT")
	return err
}
func (b *sqlitePlanBoundary) Rollback(ctx context.Context) error {
	if b == nil || !b.active {
		return nil
	}
	b.active = false
	_, err := b.connection.ExecContext(ctx, "ROLLBACK")
	return err
}

func (r Runner) ApplyChangePlan(ctx context.Context, plan changeplan.Plan) (ExecutionResult, error) {
	prepared, err := prepareChangePlan(r, plan)
	if err != nil {
		return ExecutionResult{}, err
	}
	connection, err := r.database.Conn(ctx)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("migrate: open database connection: %w", err)
	}
	defer func() { _ = connection.Close() }()
	completed := make([]changeplan.Operation, 0, len(prepared.operations))
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
		return r.applyPreparedChangePlan(ctx, connection, planRunState{prepared: prepared, history: history, store: store}, &completed)
	}
	if r.dialect.Name() == "sqlite" && prepared.hasForbidden {
		lock, lockErr := r.acquireSQLiteChangePlanLock(ctx, connection)
		if lockErr != nil {
			return changePlanExecutionResult(completed, lockErr)
		}
		err = run()
		err = releaseChangePlanLock(lock, err)
		return changePlanExecutionResult(completed, err)
	}
	switch r.dialect.Name() {
	case "postgresql":
		_, err = r.withPostgreSQLLock(ctx, connection, func() ([]Migration, error) { return nil, run() })
	case "mysql":
		_, err = r.withMySQLLock(ctx, connection, func() ([]Migration, error) { return nil, run() })
	case "sqlite":
		err = run()
	default:
		err = fmt.Errorf("migrate: unsupported plan dialect %q", r.dialect.Name())
	}
	return changePlanExecutionResult(completed, err)
}

func (r Runner) applyPreparedChangePlan(
	ctx context.Context,
	connection *sql.Conn,
	run planRunState,
	completed *[]changeplan.Operation,
) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		boundary, err := beginPlanBoundary(ctx, connection, run.prepared.profile.Engine)
		if err != nil {
			return err
		}
		queries := queryer(connection)
		if boundary != nil {
			queries = boundary
		}
		entry, legacy, prefix, err := readClassifiedPlanRows(ctx, r, queries, run.prepared, run.history, run.store)
		if err != nil {
			return rollbackForbiddenDecision(boundary, err)
		}
		if legacy != nil {
			result, err := runForbiddenPlanOperation(ctx, connection, run, boundary)
			if result.completed != nil {
				*completed = append(*completed, *result.completed)
			}
			if err != nil {
				return err
			}
			continue
		}
		catalog, digest, err := readForbiddenCatalog(ctx, connection, boundary, run, prefix)
		if err != nil {
			return rollbackForbiddenDecision(boundary, err)
		}
		state, err := verifyPlanState(run.prepared, entry, nil, catalog, digest)
		if err != nil {
			return rollbackForbiddenDecision(boundary, err)
		}
		if state.complete {
			if state.progressAction == planProgressNone {
				return rollbackForbiddenDecision(boundary, nil)
			}
			return installTerminalPlanState(ctx, connection, run, boundary, state)
		}
		operation := run.prepared.operations[state.nextIndex]
		if operation.mode == planModeForbidden {
			result, err := runForbiddenPlanOperation(ctx, connection, run, boundary)
			if result.completed != nil {
				*completed = append(*completed, *result.completed)
			}
			if err != nil {
				return err
			}
			continue
		}
		end, err := maximalRequiredPlanEnd(run.prepared, state.nextIndex)
		if err != nil {
			return rollbackForbiddenDecision(boundary, err)
		}
		result, err := runRequiredPlanGroup(ctx, boundary, run, state, state.nextIndex, end)
		if err != nil {
			return err
		}
		*completed = append(*completed, result.completed...)
	}
}

func installTerminalPlanState(
	ctx context.Context,
	connection *sql.Conn,
	run planRunState,
	boundary planApplyBoundary,
	state verifiedPlanState,
) error {
	entry := newPlanProgressEntry(run.prepared, 0)
	if boundary != nil {
		if state.progressAction == planProgressInsert {
			if err := run.store.ensure(ctx, boundary); err != nil {
				return rollbackForbiddenDecision(boundary, err)
			}
			if err := run.store.insert(ctx, boundary, entry); err != nil {
				return rollbackForbiddenDecision(boundary, err)
			}
		} else if err := run.store.replace(ctx, boundary, state.replacedPlanID, entry); err != nil {
			return rollbackForbiddenDecision(boundary, err)
		}
		return boundary.Commit(ctx)
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
		err = run.store.insert(ctx, tx, entry)
	} else {
		err = run.store.replace(ctx, tx, state.replacedPlanID, entry)
	}
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	return tx.Commit()
}
