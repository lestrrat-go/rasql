package migrate

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"sort"
	"time"
)

const mysqlLockReleaseTimeout = 5 * time.Second

// ApplyTarget names how far forward an apply goes. Build one with AllPending
// or ApplyThrough.
//
// Its zero value selects every pending migration, which is what AllPending
// returns and is the opposite of RevertTarget's zero value. A forgotten
// argument therefore brings the database up to date rather than leaving it
// behind, while a forgotten revert target still selects nothing.
//
// It is an immutable value, like every other input a Runner takes, so one
// target may be reused across concurrent Apply calls.
type ApplyTarget struct {
	kind applyKind
	id   string
}

type applyKind int

const (
	applyEverything applyKind = iota
	applyThrough
)

// AllPending selects every migration that is not yet applied.
func AllPending() ApplyTarget {
	return ApplyTarget{kind: applyEverything}
}

// ApplyThrough leaves the database where id was applied: id itself is
// applied along with every pending migration before it, and every migration
// after it stays pending. Naming a migration that is already applied
// therefore applies nothing.
func ApplyThrough(id string) ApplyTarget {
	return ApplyTarget{kind: applyThrough, id: id}
}

// Apply executes pending migrations in ID order, up to target, and returns
// what it applied in the order it applied them.
//
// Atomic PostgreSQL, MySQL, and SQLite migrations run in a transaction. A
// nontransactional migration may leave a source outcome uncertain; reconcile
// that state before running Apply again.
func (r Runner) Apply(ctx context.Context, target ApplyTarget, migrations ...Migration) ([]Migration, error) {
	result, err := r.ApplyResult(ctx, target, migrations...)
	return result.Completed, err
}

func (r Runner) ApplyResult(ctx context.Context, target ApplyTarget, migrations ...Migration) (ExecutionResult, error) {
	if err := r.validate(); err != nil {
		return ExecutionResult{}, err
	}
	prepared, err := prepareMigrations(migrations)
	if err != nil {
		return ExecutionResult{}, err
	}
	connection, err := r.database.Conn(ctx)
	if err != nil {
		return ExecutionResult{}, fmt.Errorf("migrate: open database connection: %w", err)
	}
	defer func() { _ = connection.Close() }()

	switch r.dialect.Name() {
	case "postgresql":
		completed, err := r.applyPostgreSQL(ctx, connection, target, prepared)
		return executionResult(completed, err)
	case "mysql":
		completed, err := r.applyMySQL(ctx, connection, target, prepared)
		return executionResult(completed, err)
	case "sqlite":
		completed, err := r.applySQLite(ctx, connection, target, prepared)
		return executionResult(completed, err)
	default:
		return ExecutionResult{}, fmt.Errorf("migrate: dialect %q is not supported", r.dialect.Name())
	}
}

// ApplyPlan reports the migrations Apply would run, in the order it would
// run them, and changes nothing. It reads the history table, creating it
// when it does not exist, and returns the same refusals Apply would return,
// so a caller can show what is about to happen without risking that the run
// then refuses.
//
// The plan it returns describes the history as it was read. A concurrent
// apply or revert can change that history afterwards, which is why Apply
// resolves the target again under its own lock rather than taking a plan.
func (r Runner) ApplyPlan(ctx context.Context, target ApplyTarget, migrations ...Migration) ([]Migration, error) {
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
	var result []Migration
	plan := func() error {
		var progress *progressEntry
		var progressMigration preparedMigration
		if err := r.ensureHistory(ctx, connection); err != nil {
			return err
		}
		if r.dialect.Name() == "mysql" || (r.dialect.Name() == "postgresql" && needsProgress(prepared)) {
			if err := r.ensureProgress(ctx, connection); err != nil {
				return err
			}
			var err error
			progress, err = r.progress(ctx, connection)
			if err != nil {
				return err
			}
			if progress != nil {
				if err := r.validateProgress(progress, prepared); err != nil {
					return err
				}
				if progress.direction != DirectionUp {
					return fmt.Errorf("migrate: apply plan cannot inspect %s progress", progress.direction)
				}
				if progress.nextIndex <= progress.sourceIndex {
					return incompleteError(*progress, errors.New("source outcome is uncertain; reconcile it before retrying"))
				}
				progressMigration = findProgressMigration(prepared, progress.id)
				statements, err := progressStatements(progressMigration, progress.direction)
				if err != nil {
					return err
				}
				if progress.nextIndex == len(statements) {
					if err := r.finalizeProgress(ctx, connection, *progress, progressMigration); err != nil {
						return incompleteError(*progress, err)
					}
					progress = nil
				} else {
					progressMigration.statements = append([]Statement(nil), statements[progress.nextIndex:]...)
					progressMigration.down = append([]Statement(nil), progressMigration.down...)
				}
			}
		}
		applied, err := r.applied(ctx, connection)
		if err != nil {
			return err
		}
		selected, err := selectApplies(applied, prepared, target)
		if err != nil {
			return err
		}
		if progress != nil {
			ordered := make([]preparedMigration, 0, len(selected)+1)
			ordered = append(ordered, progressMigration)
			for _, migration := range selected {
				if migration.id != progress.id {
					ordered = append(ordered, migration)
				}
			}
			selected = ordered
		}
		result = exportMigrations(selected)
		return nil
	}
	if r.dialect.Name() == "mysql" {
		if err := r.withMySQLReadLock(ctx, connection, plan); err != nil {
			return nil, err
		}
	} else if r.dialect.Name() == "postgresql" {
		if err := r.withPostgreSQLReadLock(ctx, connection, plan); err != nil {
			return nil, err
		}
	} else if err := plan(); err != nil {
		return nil, err
	}
	return result, nil
}

func (r Runner) applyPostgreSQL(ctx context.Context, connection *sql.Conn, target ApplyTarget, migrations []preparedMigration) ([]Migration, error) {
	return r.withPostgreSQLLock(ctx, connection, func() ([]Migration, error) {
		if err := r.ensureHistory(ctx, connection); err != nil {
			return nil, err
		}
		applied, err := r.applied(ctx, connection)
		if err != nil {
			return nil, err
		}
		selected, err := selectApplies(applied, migrations, target)
		if err != nil {
			return nil, err
		}
		completed := make([]Migration, 0, len(selected))
		for _, migration := range selected {
			if migration.mode == ExecutionModeNonTransactional {
				if err := r.ensureProgress(ctx, connection); err != nil {
					return completed, err
				}
				applied, err := r.applyPreparedMySQL(ctx, connection, ApplyThrough(migration.id), migrations)
				completed = append(completed, applied...)
				if err != nil {
					return completed, err
				}
				continue
			}
			transaction, err := connection.BeginTx(ctx, nil)
			if err != nil {
				return completed, fmt.Errorf("migrate: begin PostgreSQL transaction: %w", err)
			}
			var operationErr error
			for _, statement := range migration.statements {
				if _, operationErr = transaction.ExecContext(ctx, string(statement.SQL)); operationErr != nil {
					operationErr = fmt.Errorf("migrate: execute migration %q SQL source %q: %w", migration.id, statement.Source, operationErr)
					break
				}
			}
			if operationErr == nil {
				operationErr = r.record(ctx, transaction, migration)
			}
			var atomicApplied []Migration
			if operationErr == nil {
				operationErr = transaction.Commit()
				atomicApplied = exportMigrations([]preparedMigration{migration})
			}
			if operationErr != nil {
				_ = transaction.Rollback()
				return completed, operationErr
			}
			completed = append(completed, atomicApplied...)
		}
		return completed, nil
	})
}

func (r Runner) applyMySQL(ctx context.Context, connection *sql.Conn, target ApplyTarget, migrations []preparedMigration) ([]Migration, error) {
	return r.withMySQLLock(ctx, connection, func() ([]Migration, error) {
		if err := r.ensureHistory(ctx, connection); err != nil {
			return nil, err
		}
		applied, err := r.applied(ctx, connection)
		if err != nil {
			return nil, err
		}
		selected, err := selectApplies(applied, migrations, target)
		if err != nil {
			return nil, err
		}
		completed := make([]Migration, 0, len(selected))
		for _, migration := range selected {
			if migration.mode == ExecutionModeNonTransactional {
				if err := r.ensureProgress(ctx, connection); err != nil {
					return completed, err
				}
				applied, err := r.applyPreparedMySQL(ctx, connection, ApplyThrough(migration.id), migrations)
				completed = append(completed, applied...)
				if err != nil {
					return completed, err
				}
				continue
			}
			applied, err := r.applyAtomicMySQL(ctx, connection, migration)
			completed = append(completed, applied...)
			if err != nil {
				return completed, err
			}
		}
		return completed, nil
	})
}

func (r Runner) applyAtomicMySQL(ctx context.Context, connection *sql.Conn, migration preparedMigration) ([]Migration, error) {
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("migrate: begin MySQL transaction: %w", err)
	}
	rollback := func() { _ = transaction.Rollback() }
	applied, err := r.applied(ctx, transaction)
	if err != nil {
		rollback()
		return nil, err
	}
	if recorded, exists := applied[migration.id]; exists {
		if recorded != migration.checksum {
			rollback()
			return nil, fmt.Errorf("migrate: migration %q checksum does not match recorded migration", migration.id)
		}
		rollback()
		return nil, nil
	}
	for _, statement := range migration.statements {
		if _, err := transaction.ExecContext(ctx, string(statement.SQL)); err != nil {
			rollback()
			return nil, fmt.Errorf("migrate: execute migration %q SQL source %q: %w", migration.id, statement.Source, err)
		}
	}
	if err := r.record(ctx, transaction, migration); err != nil {
		rollback()
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		rollback()
		return nil, fmt.Errorf("migrate: commit MySQL transaction: %w", err)
	}
	return exportMigrations([]preparedMigration{migration}), nil
}

func (r Runner) withMySQLLock(ctx context.Context, connection *sql.Conn, run func() ([]Migration, error)) ([]Migration, error) {
	var acquired int
	if err := connection.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", r.historyTable, 30).Scan(&acquired); err != nil {
		return nil, fmt.Errorf("migrate: acquire MySQL migration lock: %w", err)
	}
	if acquired != 1 {
		return nil, fmt.Errorf("migrate: acquire MySQL migration lock: timed out")
	}
	migrationsApplied, operationErr := run()
	cleanupErr := r.releaseMySQLLock(connection)
	return migrationsApplied, errors.Join(operationErr, cleanupErr)
}

func (r Runner) withMySQLReadLock(ctx context.Context, connection *sql.Conn, run func() error) error {
	_, err := r.withMySQLLock(ctx, connection, func() ([]Migration, error) {
		return nil, run()
	})
	return err
}

func (r Runner) releaseMySQLLock(connection *sql.Conn) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(context.Background()), mysqlLockReleaseTimeout)
	defer cancel()

	var released sql.NullInt64
	err := connection.QueryRowContext(cleanupCtx, "SELECT RELEASE_LOCK(?)", r.historyTable).Scan(&released)
	if err == nil && released.Valid && released.Int64 == 1 {
		return nil
	}

	var releaseErr error
	switch {
	case err != nil:
		releaseErr = fmt.Errorf("migrate: release MySQL migration lock: %w", err)
	case !released.Valid:
		releaseErr = errors.New("migrate: release MySQL migration lock: unexpected release result NULL")
	default:
		releaseErr = fmt.Errorf("migrate: release MySQL migration lock: unexpected release result %d", released.Int64)
	}

	markBadErr := connection.Raw(func(any) error { return driver.ErrBadConn })
	if markBadErr != nil {
		if errors.Is(markBadErr, driver.ErrBadConn) {
			return errors.Join(releaseErr, fmt.Errorf("migrate: marked MySQL migration connection bad: %w", markBadErr))
		}
		return errors.Join(releaseErr, fmt.Errorf("migrate: could not mark MySQL migration connection bad: %w", markBadErr))
	}
	return fmt.Errorf("%w; MySQL migration connection marked bad", releaseErr)
}

func (r Runner) applySQLite(ctx context.Context, connection *sql.Conn, target ApplyTarget, migrations []preparedMigration) ([]Migration, error) {
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return nil, fmt.Errorf("migrate: begin SQLite migration transaction: %w", err)
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
	applied, err := r.applyPrepared(ctx, connection, connection, target, migrations)
	if err != nil {
		return nil, err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return nil, fmt.Errorf("migrate: commit SQLite migration transaction: %w", err)
	}
	committed = true
	return applied, nil
}

func (r Runner) applyPrepared(ctx context.Context, queries queryer, executions executor, target ApplyTarget, migrations []preparedMigration) ([]Migration, error) {
	recorded, err := r.applied(ctx, queries)
	if err != nil {
		return nil, err
	}
	selected, err := selectApplies(recorded, migrations, target)
	if err != nil {
		return nil, err
	}
	for _, migration := range selected {
		for _, statement := range migration.statements {
			if _, err := executions.ExecContext(ctx, string(statement.SQL)); err != nil {
				return nil, fmt.Errorf("migrate: execute migration %q SQL source %q: %w", migration.id, statement.Source, err)
			}
		}
		if err := r.record(ctx, executions, migration); err != nil {
			return nil, err
		}
	}
	return exportMigrations(selected), nil
}

// selectApplies resolves target against the applied history and returns the
// migrations to apply, oldest first.
//
// It refuses the whole run rather than applying part of the way: the history
// must name only supplied migrations, every recorded migration's checksum
// must still match its sources, and no recorded migration may sit after a
// pending one, which would mean the history skipped a migration that is
// about to run under it.
func selectApplies(applied map[string]string, migrations []preparedMigration, target ApplyTarget) ([]preparedMigration, error) {
	expected := make(map[string]struct{}, len(migrations))
	for _, migration := range migrations {
		expected[migration.id] = struct{}{}
	}
	for id := range applied {
		if _, exists := expected[id]; !exists {
			return nil, fmt.Errorf("migrate: recorded migration %q was not supplied", id)
		}
	}

	limit, err := applyLimit(migrations, target)
	if err != nil {
		return nil, err
	}

	// migrations is already sorted by ID, so a recorded migration reached
	// after a pending one is a history with a gap in it.
	selected := make([]preparedMigration, 0, len(migrations))
	pending := false
	for index, migration := range migrations {
		recordedChecksum, exists := applied[migration.id]
		if exists {
			if recordedChecksum != migration.checksum {
				return nil, fmt.Errorf("migrate: migration %q checksum does not match recorded migration", migration.id)
			}
			if pending {
				return nil, fmt.Errorf("migrate: migration %q is recorded after a missing migration", migration.id)
			}
			continue
		}
		pending = true
		if index >= limit {
			continue
		}
		selected = append(selected, migration)
	}
	return selected, nil
}

// applyLimit turns a target into the number of supplied migrations it
// reaches, counted from the oldest.
func applyLimit(migrations []preparedMigration, target ApplyTarget) (int, error) {
	switch target.kind {
	case applyThrough:
		index := sort.Search(len(migrations), func(position int) bool {
			return migrations[position].id >= target.id
		})
		if index == len(migrations) || migrations[index].id != target.id {
			return 0, fmt.Errorf("migrate: migration %q was not supplied, so nothing can be applied through it", target.id)
		}
		return index + 1, nil
	default:
		return len(migrations), nil
	}
}
