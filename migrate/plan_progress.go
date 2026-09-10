package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
)

type planProgressEntry struct {
	checkpoint     changeplan.Checkpoint
	operationCount int
}

type planProgressStore struct {
	dialect   dialect.Dialect
	tableSQL  string
	columnSQL preparedPlanProgressName
	catalog   string
	table     string
}

// planCheckpointWriteHook is a narrow test seam for plan-progress writes.
var planCheckpointWriteHook = func(string) error { return nil }

func newPlanProgressStore(prepared preparedChangePlan, history resolvedPlanHistory) (planProgressStore, error) {
	dialectValue, err := resolvedHistoryDialect(history)
	if err != nil {
		return planProgressStore{}, err
	}
	expectedEngine, ok := engineForDialect(dialectValue.Name())
	if !ok || expectedEngine != prepared.profile.Engine {
		return planProgressStore{}, fmt.Errorf("migrate: resolved plan history dialect %q does not match plan engine %d", dialectValue.Name(), prepared.profile.Engine)
	}
	if history.qualifiedPlanSQL == "" || history.planProgressTable.Name == "" {
		return planProgressStore{}, errors.New("migrate: plan progress history is incomplete")
	}
	return planProgressStore{
		dialect:   dialectValue,
		tableSQL:  history.qualifiedPlanSQL,
		columnSQL: prepared.progressName,
		catalog:   history.schema,
		table:     history.planProgressTable.Name,
	}, nil
}

func (store planProgressStore) exists(ctx context.Context, queries queryer) (bool, error) {
	query, args, err := store.catalogQuery()
	if err != nil {
		return false, err
	}
	rows, err := queries.QueryContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("migrate: inspect plan progress table: %w", err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("migrate: inspect plan progress table: %w", err)
		}
		if err := rows.Close(); err != nil {
			return false, fmt.Errorf("migrate: close plan progress rows: %w", err)
		}
		return false, nil
	}
	var marker int
	if err := rows.Scan(&marker); err != nil {
		_ = rows.Close()
		return false, fmt.Errorf("migrate: scan plan progress table: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("migrate: close plan progress rows: %w", err)
	}
	return true, nil
}

func (store planProgressStore) ensure(ctx context.Context, executions executor) error {
	statement := "CREATE TABLE IF NOT EXISTS " + store.tableSQL + " (" +
		store.columnSQL.quotedPlanID + " VARCHAR(64) NOT NULL PRIMARY KEY, " +
		store.columnSQL.quotedNextIndex + " INTEGER NOT NULL, " +
		store.columnSQL.quotedCount + " INTEGER NOT NULL, " +
		store.columnSQL.quotedCatalogHash + " CHAR(64) NOT NULL)"
	if _, err := executions.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("migrate: create plan progress: %w", err)
	}
	return nil
}

func (store planProgressStore) read(ctx context.Context, queries queryer) (*planProgressEntry, error) {
	query := "SELECT " + store.columnSQL.quotedPlanID + ", " + store.columnSQL.quotedNextIndex + ", " +
		store.columnSQL.quotedCount + ", " + store.columnSQL.quotedCatalogHash + " FROM " + store.tableSQL
	rows, err := queries.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("migrate: read plan progress: %w", err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return nil, fmt.Errorf("migrate: read plan progress: %w", err)
		}
		if err := rows.Close(); err != nil {
			return nil, fmt.Errorf("migrate: close plan progress rows: %w", err)
		}
		return nil, nil
	}
	var planID, catalogDigest string
	var nextIndex, operationCount int
	if err := rows.Scan(&planID, &nextIndex, &operationCount, &catalogDigest); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("migrate: scan plan progress: %w", err)
	}
	if rows.Next() {
		_ = rows.Close()
		return nil, errors.New("migrate: more than one plan progress row exists")
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return nil, fmt.Errorf("migrate: read plan progress: %w", err)
	}
	if err := rows.Close(); err != nil {
		return nil, fmt.Errorf("migrate: close plan progress rows: %w", err)
	}
	parsedPlan, err := changeplan.ParseDigest(planID)
	if err != nil {
		return nil, fmt.Errorf("migrate: parse plan progress ID: %w", err)
	}
	parsedCatalog, err := changeplan.ParseDigest(catalogDigest)
	if err != nil {
		return nil, fmt.Errorf("migrate: parse plan progress catalog digest: %w", err)
	}
	checkpoint, err := changeplan.NewCheckpoint(changeplan.PlanID(parsedPlan), nextIndex, parsedCatalog)
	if err != nil {
		return nil, fmt.Errorf("migrate: construct plan progress checkpoint: %w", err)
	}
	if operationCount < 0 || nextIndex < 0 || nextIndex > operationCount {
		return nil, fmt.Errorf("migrate: invalid plan progress boundary next_index=%d operation_count=%d", nextIndex, operationCount)
	}
	return &planProgressEntry{checkpoint: checkpoint, operationCount: operationCount}, nil
}

func (store planProgressStore) insert(ctx context.Context, executions executor, entry planProgressEntry) error {
	if err := validatePlanProgressEntry(entry); err != nil {
		return err
	}
	if err := planCheckpointWriteHook("insert"); err != nil {
		return fmt.Errorf("migrate: plan progress write hook: %w", err)
	}
	p1, err := store.placeholder(1)
	if err != nil {
		return err
	}
	p2, err := store.placeholder(2)
	if err != nil {
		return err
	}
	p3, err := store.placeholder(3)
	if err != nil {
		return err
	}
	p4, err := store.placeholder(4)
	if err != nil {
		return err
	}
	query := "INSERT INTO " + store.tableSQL + " (" + store.columnSQL.quotedPlanID + ", " + store.columnSQL.quotedNextIndex +
		", " + store.columnSQL.quotedCount + ", " + store.columnSQL.quotedCatalogHash + ") VALUES (" + p1 + ", " + p2 + ", " + p3 + ", " + p4 + ")"
	if _, err := executions.ExecContext(ctx, query, progressArgs(entry)...); err != nil {
		return fmt.Errorf("migrate: insert plan progress: %w", err)
	}
	return nil
}

func (store planProgressStore) update(ctx context.Context, executions executor, entry planProgressEntry) error {
	if err := validatePlanProgressEntry(entry); err != nil {
		return err
	}
	if err := planCheckpointWriteHook("update"); err != nil {
		return fmt.Errorf("migrate: plan progress write hook: %w", err)
	}
	p1, err := store.placeholder(1)
	if err != nil {
		return err
	}
	p2, err := store.placeholder(2)
	if err != nil {
		return err
	}
	p3, err := store.placeholder(3)
	if err != nil {
		return err
	}
	p4, err := store.placeholder(4)
	if err != nil {
		return err
	}
	query := "UPDATE " + store.tableSQL + " SET " + store.columnSQL.quotedNextIndex + " = " + p1 + ", " +
		store.columnSQL.quotedCount + " = " + p2 + ", " + store.columnSQL.quotedCatalogHash + " = " + p3 + " WHERE " +
		store.columnSQL.quotedPlanID + " = " + p4
	result, err := executions.ExecContext(ctx, query, progressArgs(entry)[1], progressArgs(entry)[2], progressArgs(entry)[3], entry.checkpoint.PlanID().String())
	if err != nil {
		return fmt.Errorf("migrate: update plan progress: %w", err)
	}
	return requireOneAffected(result, "update plan progress")
}

func (store planProgressStore) replace(ctx context.Context, executions executor, oldPlanID changeplan.PlanID, entry planProgressEntry) error {
	if oldPlanID == (changeplan.PlanID{}) {
		return errors.New("migrate: replace plan progress requires an old plan ID")
	}
	if err := validatePlanProgressEntry(entry); err != nil {
		return err
	}
	if err := planCheckpointWriteHook("replace"); err != nil {
		return fmt.Errorf("migrate: plan progress write hook: %w", err)
	}
	placeholders := make([]string, 5)
	for index := range placeholders {
		value, err := store.placeholder(index + 1)
		if err != nil {
			return err
		}
		placeholders[index] = value
	}
	query := "UPDATE " + store.tableSQL + " SET " + store.columnSQL.quotedPlanID + " = " + placeholders[0] + ", " +
		store.columnSQL.quotedNextIndex + " = " + placeholders[1] + ", " + store.columnSQL.quotedCount + " = " + placeholders[2] +
		", " + store.columnSQL.quotedCatalogHash + " = " + placeholders[3] + " WHERE " + store.columnSQL.quotedPlanID +
		" = " + placeholders[4]
	args := []any{entry.checkpoint.PlanID().String(), entry.checkpoint.NextIndex(), entry.operationCount, entry.checkpoint.CatalogDigest().String(), oldPlanID.String()}
	result, err := executions.ExecContext(ctx, query, args...)
	if err != nil {
		return fmt.Errorf("migrate: replace plan progress: %w", err)
	}
	return requireOneAffected(result, "replace plan progress")
}

func (store planProgressStore) catalogQuery() (string, []any, error) {
	p1, err := store.placeholder(1)
	if err != nil {
		return "", nil, err
	}
	p2, err := store.placeholder(2)
	if err != nil {
		return "", nil, err
	}
	switch store.dialect.Name() {
	case "sqlite":
		return "SELECT 1 FROM " + quoteSQLiteCatalogSchema(store.catalog) + ".sqlite_master WHERE type = 'table' AND name = " + p1,
			[]any{store.table}, nil
	case "postgresql", "mysql":
		return "SELECT 1 FROM information_schema.tables WHERE table_schema = " + p1 + " AND table_name = " + p2,
			[]any{store.catalog, store.table}, nil
	default:
		return "", nil, fmt.Errorf("migrate: unsupported plan progress dialect %q", store.dialect.Name())
	}
}

func (store planProgressStore) placeholder(position int) (string, error) {
	placeholder, err := store.dialect.Placeholder(position)
	if err != nil {
		return "", fmt.Errorf("migrate: plan progress placeholder: %w", err)
	}
	return placeholder, nil
}

func progressArgs(entry planProgressEntry) []any {
	return []any{entry.checkpoint.PlanID().String(), entry.checkpoint.NextIndex(), entry.operationCount, entry.checkpoint.CatalogDigest().String()}
}

func validatePlanProgressEntry(entry planProgressEntry) error {
	if entry.checkpoint.PlanID() == (changeplan.PlanID{}) {
		return errors.New("migrate: plan progress requires a plan ID")
	}
	if entry.operationCount < 0 || entry.checkpoint.NextIndex() < 0 || entry.checkpoint.NextIndex() > entry.operationCount {
		return fmt.Errorf("migrate: invalid plan progress boundary next_index=%d operation_count=%d", entry.checkpoint.NextIndex(), entry.operationCount)
	}
	return nil
}

func requireOneAffected(result sql.Result, action string) error {
	if result == nil {
		return fmt.Errorf("migrate: %s returned no result", action)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return fmt.Errorf("migrate: %s rows affected: %w", action, err)
	}
	if affected != 1 {
		return fmt.Errorf("migrate: %s affected %d rows, want 1", action, affected)
	}
	return nil
}
