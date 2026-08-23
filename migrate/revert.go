package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

// RevertTarget names how far back a revert goes. Build one with Through or
// Steps; the zero value selects nothing and is refused, so a caller cannot
// reach a revert by forgetting an argument.
//
// It is an immutable value, like every other input a Runner takes, so one
// target may be reused across concurrent Revert calls.
type RevertTarget struct {
	kind  revertKind
	id    string
	steps int
}

type revertKind int

const (
	revertNothing revertKind = iota
	revertThrough
	revertSteps
)

// Through leaves the database at the point where id was applied: id itself
// stays applied, and every applied migration after it is reverted. Naming
// the newest applied migration therefore reverts nothing.
func Through(id string) RevertTarget {
	return RevertTarget{kind: revertThrough, id: id}
}

// Steps reverts the n newest applied migrations. n must be positive.
func Steps(n int) RevertTarget {
	return RevertTarget{kind: revertSteps, steps: n}
}

// Revert undoes migrations, newest first, down to target, and returns what
// it reverted in the order it reverted them.
//
// Each selected migration has its Down sources executed in order, and its
// history record deleted after them, so an interrupted run leaves a history
// that describes what actually happened. Nothing runs at all unless every
// selected migration can be reverted; see RevertPlan for what is checked.
// A target that selects nothing, such as Through naming the newest applied
// migration, is not an error and returns no migrations.
//
// PostgreSQL and SQLite revert a complete migration atomically. MySQL DDL
// may commit implicitly, so a failure can leave completed statements in
// place and the migration still recorded; resolve that state before running
// Revert again.
func (r Runner) Revert(ctx context.Context, target RevertTarget, migrations ...Migration) ([]Migration, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	prepared, err := prepareMigrations(migrations)
	if err != nil {
		return nil, err
	}
	connection, err := r.database.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate: open database connection: %w", err)
	}
	defer func() { _ = connection.Close() }()

	switch r.dialect.Name() {
	case "postgresql":
		return r.revertPostgreSQL(ctx, connection, target, prepared)
	case "mysql":
		return r.revertMySQL(ctx, connection, target, prepared)
	case "sqlite":
		return r.revertSQLite(ctx, connection, target, prepared)
	default:
		return nil, fmt.Errorf("migrate: dialect %q is not supported", r.dialect.Name())
	}
}

// RevertPlan reports the migrations Revert would undo, newest first, and
// changes nothing. It reads the history table, creating it when it does not
// exist, and returns the same refusals Revert would return, so a caller can
// show what is about to happen without risking that the run then refuses.
//
// The plan it returns describes the history as it was read. A concurrent
// apply or revert can change that history afterwards, which is why Revert
// resolves the target again under its own lock rather than taking a plan.
func (r Runner) RevertPlan(ctx context.Context, target RevertTarget, migrations ...Migration) ([]Migration, error) {
	if err := r.validate(); err != nil {
		return nil, err
	}
	prepared, err := prepareMigrations(migrations)
	if err != nil {
		return nil, err
	}
	connection, err := r.database.Conn(ctx)
	if err != nil {
		return nil, fmt.Errorf("migrate: open database connection: %w", err)
	}
	defer func() { _ = connection.Close() }()
	if err := r.ensureHistory(ctx, connection); err != nil {
		return nil, err
	}
	applied, err := r.applied(ctx, connection)
	if err != nil {
		return nil, err
	}
	selected, err := selectReverts(applied, prepared, target)
	if err != nil {
		return nil, err
	}
	return exportMigrations(selected), nil
}

// exportMigrations copies prepared migrations back into the public type,
// so a caller cannot reach the runner's own slices.
func exportMigrations(selected []preparedMigration) []Migration {
	exported := make([]Migration, len(selected))
	for index, migration := range selected {
		exported[index] = Migration{
			ID:         migration.id,
			Statements: append([]Statement(nil), migration.statements...),
			Down:       append([]Statement(nil), migration.down...),
		}
	}
	return exported
}

func (r Runner) revertPostgreSQL(ctx context.Context, connection *sql.Conn, target RevertTarget, migrations []preparedMigration) ([]Migration, error) {
	if err := r.ensureHistory(ctx, connection); err != nil {
		return nil, err
	}
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("migrate: begin PostgreSQL transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, "LOCK TABLE "+r.historySQL+" IN EXCLUSIVE MODE"); err != nil {
		return nil, fmt.Errorf("migrate: lock migration history: %w", err)
	}
	reverted, err := r.revertPrepared(ctx, transaction, transaction, target, migrations)
	if err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("migrate: commit PostgreSQL revert transaction: %w", err)
	}
	return reverted, nil
}

func (r Runner) revertMySQL(ctx context.Context, connection *sql.Conn, target RevertTarget, migrations []preparedMigration) ([]Migration, error) {
	var acquired int
	if err := connection.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", r.historyTable, 30).Scan(&acquired); err != nil {
		return nil, fmt.Errorf("migrate: acquire MySQL migration lock: %w", err)
	}
	if acquired != 1 {
		return nil, fmt.Errorf("migrate: acquire MySQL migration lock: timed out")
	}
	defer r.releaseMySQLLock(connection)
	if err := r.ensureHistory(ctx, connection); err != nil {
		return nil, err
	}
	return r.revertPrepared(ctx, connection, connection, target, migrations)
}

func (r Runner) revertSQLite(ctx context.Context, connection *sql.Conn, target RevertTarget, migrations []preparedMigration) ([]Migration, error) {
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("migrate: begin SQLite revert transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := r.ensureHistory(ctx, connection); err != nil {
		return nil, err
	}
	reverted, err := r.revertPrepared(ctx, connection, connection, target, migrations)
	if err != nil {
		return nil, err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("migrate: commit SQLite revert transaction: %w", err)
	}
	committed = true
	return reverted, nil
}

func (r Runner) revertPrepared(ctx context.Context, queries queryer, executions executor, target RevertTarget, migrations []preparedMigration) ([]Migration, error) {
	applied, err := r.applied(ctx, queries)
	if err != nil {
		return nil, err
	}
	selected, err := selectReverts(applied, migrations, target)
	if err != nil {
		return nil, err
	}
	for _, migration := range selected {
		for _, statement := range migration.down {
			if _, err := executions.ExecContext(ctx, string(statement.SQL)); err != nil {
				return nil, fmt.Errorf("migrate: execute migration %q reverse SQL source %q: %w", migration.id, statement.Source, err)
			}
		}
		if err := r.forget(ctx, executions, migration.id); err != nil {
			return nil, err
		}
	}
	return exportMigrations(selected), nil
}

// selectReverts resolves target against the applied history and returns the
// migrations to revert, newest first.
//
// It refuses the whole run rather than reverting part of the way: the
// history must describe exactly the supplied migrations with no gap, every
// selected migration's recorded checksum must still match its sources, and
// every selected migration must carry reverse sources. A run that would
// stop halfway at an unreachable migration leaves a database no one can
// describe, which is worse than a run that does nothing.
func selectReverts(applied map[string]string, migrations []preparedMigration, target RevertTarget) ([]preparedMigration, error) {
	byID := make(map[string]preparedMigration, len(migrations))
	for _, migration := range migrations {
		byID[migration.id] = migration
	}
	for id := range applied {
		if _, exists := byID[id]; !exists {
			return nil, fmt.Errorf("migrate: recorded migration %q was not supplied", id)
		}
	}

	// migrations is already sorted by ID, so the applied ones are collected
	// in the order they ran, and a pending migration before an applied one
	// is the same out-of-order history Apply refuses.
	appliedInOrder := make([]preparedMigration, 0, len(applied))
	pending := false
	for _, migration := range migrations {
		if _, exists := applied[migration.id]; !exists {
			pending = true
			continue
		}
		if pending {
			return nil, fmt.Errorf("migrate: migration %q is recorded after a missing migration", migration.id)
		}
		appliedInOrder = append(appliedInOrder, migration)
	}

	count, err := revertCount(appliedInOrder, target)
	if err != nil {
		return nil, err
	}
	selected := make([]preparedMigration, 0, count)
	for index := len(appliedInOrder) - 1; index >= len(appliedInOrder)-count; index-- {
		migration := appliedInOrder[index]
		if applied[migration.id] != migration.checksum {
			return nil, fmt.Errorf("migrate: migration %q checksum does not match recorded migration", migration.id)
		}
		if len(migration.down) == 0 {
			return nil, fmt.Errorf("migrate: migration %q has no reverse SQL source", migration.id)
		}
		selected = append(selected, migration)
	}
	return selected, nil
}

// revertCount turns a target into how many of the newest applied migrations
// it selects.
func revertCount(appliedInOrder []preparedMigration, target RevertTarget) (int, error) {
	switch target.kind {
	case revertThrough:
		index := sort.Search(len(appliedInOrder), func(position int) bool {
			return appliedInOrder[position].id >= target.id
		})
		if index == len(appliedInOrder) || appliedInOrder[index].id != target.id {
			return 0, fmt.Errorf("migrate: migration %q is not applied, so nothing can be reverted through it", target.id)
		}
		return len(appliedInOrder) - index - 1, nil
	case revertSteps:
		if target.steps <= 0 {
			return 0, fmt.Errorf("migrate: revert step count %d must be positive", target.steps)
		}
		if target.steps > len(appliedInOrder) {
			return 0, fmt.Errorf("migrate: cannot revert %d migrations; %d are applied", target.steps, len(appliedInOrder))
		}
		return target.steps, nil
	default:
		return 0, fmt.Errorf("migrate: revert requires a target; build one with Through or Steps")
	}
}

// forget deletes one migration's history record.
func (r Runner) forget(ctx context.Context, executions executor, id string) error {
	placeholder, err := r.dialect.Placeholder(1)
	if err != nil {
		return fmt.Errorf("migrate: render migration history delete: %w", err)
	}
	statement := "DELETE FROM " + r.historySQL + " WHERE " + r.idSQL + " = " + placeholder
	if _, err := executions.ExecContext(ctx, statement, id); err != nil {
		return fmt.Errorf("migrate: remove migration record %q: %w", id, err)
	}
	return nil
}
