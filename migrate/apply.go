package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
)

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
// PostgreSQL and SQLite apply a complete migration atomically. MySQL DDL may
// commit implicitly, so a failure can leave completed statements in place and
// the migration unrecorded; resolve that state before running Apply again.
func (r Runner) Apply(ctx context.Context, target ApplyTarget, migrations ...Migration) ([]Migration, error) {
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
		return r.applyPostgreSQL(ctx, connection, target, prepared)
	case "mysql":
		return r.applyMySQL(ctx, connection, target, prepared)
	case "sqlite":
		return r.applySQLite(ctx, connection, target, prepared)
	default:
		return nil, fmt.Errorf("migrate: dialect %q is not supported", r.dialect.Name())
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
	if err := r.ensureHistory(ctx, connection); err != nil {
		return nil, err
	}
	applied, err := r.applied(ctx, connection)
	if err != nil {
		return nil, err
	}
	selected, err := selectApplies(applied, prepared, target)
	if err != nil {
		return nil, err
	}
	return exportMigrations(selected), nil
}

func (r Runner) applyPostgreSQL(ctx context.Context, connection *sql.Conn, target ApplyTarget, migrations []preparedMigration) ([]Migration, error) {
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
	applied, err := r.applyPrepared(ctx, transaction, transaction, target, migrations)
	if err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("migrate: commit PostgreSQL migration transaction: %w", err)
	}
	return applied, nil
}

func (r Runner) applyMySQL(ctx context.Context, connection *sql.Conn, target ApplyTarget, migrations []preparedMigration) ([]Migration, error) {
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
	return r.applyPrepared(ctx, connection, connection, target, migrations)
}

func (r Runner) releaseMySQLLock(connection *sql.Conn) {
	var released int
	_ = connection.QueryRowContext(context.Background(), "SELECT RELEASE_LOCK(?)", r.historyTable).Scan(&released)
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
			if _, err := executions.ExecContext(ctx, statement.SQL); err != nil {
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
