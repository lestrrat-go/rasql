package migrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
)

type progressEntry struct {
	id          string
	checksum    string
	direction   Direction
	sourceIndex int
	source      string
	nextIndex   int
}

// journalWriteHook is a test seam for failures at journal boundaries. It is
// deliberately limited to intent, checkpoint, history, and cleanup writes;
// migration SQL always goes directly to the configured database.
var journalWriteHook = func(string) error { return nil }

func (r Runner) progressColumn(name string) string {
	quoted, _ := r.dialect.QuoteIdentifier(name)
	return quoted
}

func (r Runner) progressTableDDL() string {
	return "CREATE TABLE IF NOT EXISTS " + r.progressSQL + " (" +
		r.idSQL + " VARCHAR(255) NOT NULL PRIMARY KEY, " +
		r.checksumSQL + " CHAR(64) NOT NULL, " + r.progressColumn("direction") + " VARCHAR(16) NOT NULL, " + r.progressColumn("source_index") + " INTEGER NOT NULL, " + r.progressColumn("source") + " TEXT NOT NULL, " + r.progressColumn("next_index") + " INTEGER NOT NULL, " + r.progressColumn("started_at") + " TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)"
}

func (r Runner) ensureProgress(ctx context.Context, connection executor) error {
	if _, err := connection.ExecContext(ctx, r.progressTableDDL()); err != nil {
		return fmt.Errorf("migrate: create migration progress: %w", err)
	}
	return nil
}

func (r Runner) progress(ctx context.Context, queries queryer) (*progressEntry, error) {
	rows, err := queries.QueryContext(ctx, "SELECT "+r.idSQL+", "+r.checksumSQL+", "+r.progressColumn("direction")+", "+r.progressColumn("source_index")+", "+r.progressColumn("source")+", "+r.progressColumn("next_index")+" FROM "+r.progressSQL+" ORDER BY "+r.idSQL)
	if err != nil {
		return nil, fmt.Errorf("migrate: read migration progress: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		return nil, rows.Err()
	}
	var entry progressEntry
	if err := rows.Scan(&entry.id, &entry.checksum, &entry.direction, &entry.sourceIndex, &entry.source, &entry.nextIndex); err != nil {
		return nil, fmt.Errorf("migrate: scan migration progress: %w", err)
	}
	if rows.Next() {
		return nil, errors.New("migrate: more than one migration progress row exists")
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: read migration progress: %w", err)
	}
	return &entry, nil
}

// Reconcile resolves a retained migration intent after an interrupted
// nontransactional migration. The check runs while the migration lock is held
// and must report whether the source took effect.
func (r Runner) Reconcile(ctx context.Context, check ReconcileCheck, migrations ...Migration) error {
	if check == nil {
		return errors.New("migrate: reconcile check must not be nil")
	}
	if err := r.validate(); err != nil {
		return err
	}
	if r.dialect.Name() != "mysql" && r.dialect.Name() != "postgresql" {
		return fmt.Errorf("migrate: reconcile is supported only for MySQL and PostgreSQL")
	}
	prepared, err := prepareMigrations(migrations)
	if err != nil {
		return err
	}
	connection, err := r.database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("migrate: open database connection: %w", err)
	}
	defer func() { _ = connection.Close() }()
	run := func() ([]Migration, error) {
		if err := r.ensureProgress(ctx, connection); err != nil {
			return nil, err
		}
		if err := r.ensureHistory(ctx, connection); err != nil {
			return nil, err
		}
		entry, err := r.progress(ctx, connection)
		if err != nil || entry == nil {
			if err == nil {
				err = errors.New("migrate: no incomplete migration is recorded")
			}
			return nil, err
		}
		if err := r.validateProgress(entry, prepared); err != nil {
			return nil, err
		}
		if entry.nextIndex > entry.sourceIndex {
			return nil, incompleteError(*entry, errors.New("migrate: progress source is already known complete; retry to finalize it"))
		}
		decision, err := check.Check(ctx, connection, IncompleteMigration{ID: entry.id, Checksum: entry.checksum, Source: entry.source, Direction: entry.direction, SourceIndex: entry.sourceIndex})
		if err != nil {
			return nil, err
		}
		migration := findProgressMigration(prepared, entry.id)
		switch decision {
		case ReconcileNotExecuted:
			return nil, r.restoreKnownCheckpoint(ctx, connection, migration, entry.direction, entry.nextIndex)
		case ReconcileExecuted:
			if err := r.checkpointProgress(ctx, connection, migration, entry.direction, entry.sourceIndex); err != nil {
				return nil, err
			}
			statements, _ := progressStatements(migration, entry.direction)
			if entry.sourceIndex+1 == len(statements) {
				entry.nextIndex = entry.sourceIndex + 1
				if err := r.finalizeProgress(ctx, connection, *entry, migration); err != nil {
					return nil, err
				}
			}
			return nil, nil
		default:
			return nil, fmt.Errorf("migrate: invalid reconcile decision %q", decision)
		}
	}
	if r.dialect.Name() == "postgresql" {
		_, err = r.withPostgreSQLLock(ctx, connection, run)
	} else {
		_, err = r.withMySQLLock(ctx, connection, run)
	}
	return err
}

func findProgressMigration(migrations []preparedMigration, id string) preparedMigration {
	for _, migration := range migrations {
		if migration.id == id {
			return migration
		}
	}
	return preparedMigration{}
}

func prioritizeProgress(selected, migrations []preparedMigration, id string) []preparedMigration {
	var prioritized []preparedMigration
	for _, migration := range selected {
		if migration.id == id {
			prioritized = append(prioritized, migration)
		}
	}
	if len(prioritized) == 0 {
		migration := findProgressMigration(migrations, id)
		if migration.id != "" {
			prioritized = append(prioritized, migration)
		}
	}
	for _, migration := range selected {
		if migration.id != id {
			prioritized = append(prioritized, migration)
		}
	}
	return prioritized
}

func (r Runner) upsertProgress(ctx context.Context, connection executor, migration preparedMigration, direction Direction, index int) error {
	if err := journalWriteHook("intent"); err != nil {
		return err
	}
	first, err := r.dialect.Placeholder(1)
	if err != nil {
		return err
	}
	second, err := r.dialect.Placeholder(2)
	if err != nil {
		return err
	}
	third, err := r.dialect.Placeholder(3)
	if err != nil {
		return err
	}
	fourth, err := r.dialect.Placeholder(4)
	if err != nil {
		return err
	}
	fifth, err := r.dialect.Placeholder(5)
	if err != nil {
		return err
	}
	sixth, err := r.dialect.Placeholder(6)
	if err != nil {
		return err
	}
	statement := "INSERT INTO " + r.progressSQL + " (" + r.idSQL + ", " + r.checksumSQL + ", " + r.progressColumn("direction") + ", " + r.progressColumn("source_index") + ", " + r.progressColumn("source") + ", " + r.progressColumn("next_index") + ") VALUES (" + first + ", " + second + ", " + third + ", " + fourth + ", " + fifth + ", " + sixth + ")"
	if r.dialect.Name() == "mysql" {
		statement += " ON DUPLICATE KEY UPDATE " + r.checksumSQL + "=VALUES(" + r.checksumSQL + "), " + r.progressColumn("direction") + "=VALUES(" + r.progressColumn("direction") + "), " + r.progressColumn("source_index") + "=VALUES(" + r.progressColumn("source_index") + "), " + r.progressColumn("source") + "=VALUES(" + r.progressColumn("source") + "), " + r.progressColumn("next_index") + "=VALUES(" + r.progressColumn("next_index") + ")"
	}
	if _, err := connection.ExecContext(ctx, statement, migration.id, migration.checksum, direction, index, migrationSource(migration, direction, index), index); err != nil {
		return err
	}
	return nil
}

func migrationSource(migration preparedMigration, direction Direction, index int) string {
	if direction == DirectionDown {
		return migration.down[index].Source
	}
	return migration.statements[index].Source
}

func progressStatements(migration preparedMigration, direction Direction) ([]Statement, error) {
	switch direction {
	case DirectionUp:
		return migration.statements, nil
	case DirectionDown:
		return migration.down, nil
	default:
		return nil, fmt.Errorf("migrate: invalid progress direction %q", direction)
	}
}

func (r Runner) checkpointProgress(ctx context.Context, connection executor, migration preparedMigration, direction Direction, index int) error {
	if err := journalWriteHook("checkpoint"); err != nil {
		return err
	}
	entrySource := migrationSource(migration, direction, index)
	first, err := r.dialect.Placeholder(1)
	if err != nil {
		return err
	}
	second, err := r.dialect.Placeholder(2)
	if err != nil {
		return err
	}
	third, err := r.dialect.Placeholder(3)
	if err != nil {
		return err
	}
	statement := "UPDATE " + r.progressSQL + " SET " + r.progressColumn("next_index") + "=" + first + ", " + r.progressColumn("source") + "=" + second + " WHERE " + r.idSQL + "=" + third
	if _, err := connection.ExecContext(ctx, statement, index+1, entrySource, migration.id); err != nil {
		return err
	}
	return nil
}

func (r Runner) deleteProgress(ctx context.Context, connection executor, id string) error {
	if err := journalWriteHook("cleanup"); err != nil {
		return err
	}
	placeholder, err := r.dialect.Placeholder(1)
	if err != nil {
		return err
	}
	_, err = connection.ExecContext(ctx, "DELETE FROM "+r.progressSQL+" WHERE "+r.idSQL+"="+placeholder, id)
	return err
}

func (r Runner) restoreKnownCheckpoint(ctx context.Context, connection executor, migration preparedMigration, direction Direction, nextIndex int) error {
	statements, err := progressStatements(migration, direction)
	if err != nil {
		return err
	}
	if nextIndex == 0 {
		return r.deleteProgress(ctx, connection, migration.id)
	}
	if nextIndex > len(statements) {
		return fmt.Errorf("migrate: progress checkpoint %d is outside source count %d", nextIndex, len(statements))
	}
	if err := r.upsertProgress(ctx, connection, migration, direction, nextIndex-1); err != nil {
		return err
	}
	return r.checkpointProgress(ctx, connection, migration, direction, nextIndex-1)
}

func (r Runner) finalizeProgress(ctx context.Context, connection interface {
	executor
	queryer
}, entry progressEntry, migration preparedMigration) error {
	statements, err := progressStatements(migration, entry.direction)
	if err != nil {
		return err
	}
	if entry.nextIndex != len(statements) || entry.sourceIndex != len(statements)-1 {
		return fmt.Errorf("migrate: progress row is not terminal")
	}
	applied, err := r.applied(ctx, connection)
	if err != nil {
		return err
	}
	if entry.direction == DirectionUp {
		recorded, exists := applied[migration.id]
		if exists && recorded != migration.checksum {
			return fmt.Errorf("migrate: migration %q history checksum does not match progress", migration.id)
		}
		if !exists {
			if err := r.record(ctx, connection, migration); err != nil {
				return err
			}
		}
	} else if recorded, exists := applied[migration.id]; exists {
		if recorded != migration.checksum {
			return fmt.Errorf("migrate: migration %q history checksum does not match progress", migration.id)
		}
		if err := r.forget(ctx, connection, migration.id); err != nil {
			return err
		}
	}
	return r.deleteProgress(ctx, connection, migration.id)
}

func incompleteError(entry progressEntry, cause error) error {
	return &IncompleteMigrationError{Incomplete: IncompleteMigration{ID: entry.id, Checksum: entry.checksum, Source: entry.source, Direction: entry.direction, SourceIndex: entry.sourceIndex}, Cause: cause}
}

func (r Runner) validateProgress(entry *progressEntry, migrations []preparedMigration) error {
	for _, migration := range migrations {
		if migration.id != entry.id {
			continue
		}
		if migration.checksum != entry.checksum {
			return fmt.Errorf("migrate: migration %q progress checksum does not match supplied migration", entry.id)
		}
		if entry.direction != DirectionUp && entry.direction != DirectionDown {
			return fmt.Errorf("migrate: migration %q progress direction is invalid", entry.id)
		}
		statements, err := progressStatements(migration, entry.direction)
		if err != nil {
			return err
		}
		if entry.sourceIndex < 0 || entry.sourceIndex >= len(statements) || statements[entry.sourceIndex].Source != entry.source {
			return fmt.Errorf("migrate: migration %q progress source does not match supplied migration", entry.id)
		}
		if entry.nextIndex < entry.sourceIndex || entry.nextIndex > entry.sourceIndex+1 || entry.nextIndex > len(statements) {
			return fmt.Errorf("migrate: migration %q progress index is invalid", entry.id)
		}
		if entry.nextIndex == len(statements) && entry.sourceIndex != len(statements)-1 {
			return fmt.Errorf("migrate: migration %q progress terminal index is invalid", entry.id)
		}
		return nil
	}
	return fmt.Errorf("migrate: migration %q progress was not supplied", entry.id)
}

func (r Runner) applyPreparedMySQL(ctx context.Context, connection *sql.Conn, target ApplyTarget, migrations []preparedMigration) ([]Migration, error) {
	entry, err := r.progress(ctx, connection)
	if err != nil {
		return nil, err
	}
	completed := make([]Migration, 0)
	if entry != nil {
		if err := r.validateProgress(entry, migrations); err != nil {
			return nil, err
		}
		if entry.direction != DirectionUp {
			return nil, fmt.Errorf("migrate: apply cannot resume %s progress; reconcile or revert it first", entry.direction)
		}
		if entry.nextIndex <= entry.sourceIndex {
			return nil, incompleteError(*entry, errors.New("source outcome is uncertain; reconcile it before retrying"))
		}
		migration := findProgressMigration(migrations, entry.id)
		statements, _ := progressStatements(migration, entry.direction)
		if entry.nextIndex == len(statements) {
			if err := r.finalizeProgress(ctx, connection, *entry, migration); err != nil {
				return nil, incompleteError(*entry, fmt.Errorf("finalize progress: %w", err))
			}
			completed = append(completed, exportMigrationsForResult([]Migration{{ID: migration.id, Statements: migration.statements, Down: migration.down}})...)
			entry = nil
		}
	}
	recorded, err := r.applied(ctx, connection)
	if err != nil {
		return nil, err
	}
	selected, err := selectApplies(recorded, migrations, target)
	if err != nil {
		return nil, err
	}
	if entry != nil {
		selected = []preparedMigration{findProgressMigration(migrations, entry.id)}
	}
	for _, migration := range selected {
		start := 0
		if entry != nil && entry.id == migration.id {
			start = entry.nextIndex
		}
		for index := start; index < len(migration.statements); index++ {
			statement := migration.statements[index]
			entry := progressEntry{id: migration.id, checksum: migration.checksum, direction: DirectionUp, sourceIndex: index, source: statement.Source, nextIndex: index}
			if err := r.upsertProgress(ctx, connection, migration, DirectionUp, index); err != nil {
				return exportMigrationsForResult(completed), incompleteError(entry, fmt.Errorf("write progress: %w", err))
			}
			if _, err := connection.ExecContext(ctx, string(statement.SQL)); err != nil {
				return exportMigrationsForResult(completed), incompleteError(entry, fmt.Errorf("execute source: %w", err))
			}
			if err := r.checkpointProgress(ctx, connection, migration, DirectionUp, index); err != nil {
				return exportMigrationsForResult(completed), incompleteError(entry, fmt.Errorf("checkpoint source: %w", err))
			}
		}
		terminal := progressEntry{id: migration.id, checksum: migration.checksum, direction: DirectionUp, sourceIndex: len(migration.statements) - 1, source: migration.statements[len(migration.statements)-1].Source, nextIndex: len(migration.statements)}
		if err := r.finalizeProgress(ctx, connection, terminal, migration); err != nil {
			return exportMigrationsForResult(completed), incompleteError(terminal, fmt.Errorf("finalize progress: %w", err))
		}
		completed = append(completed, Migration{ID: migration.id, Mode: migration.mode, Statements: append([]Statement(nil), migration.statements...), Down: append([]Statement(nil), migration.down...)})
	}
	return exportMigrationsForResult(completed), nil
}

func (r Runner) revertPreparedMySQL(ctx context.Context, connection *sql.Conn, target RevertTarget, migrations []preparedMigration) ([]Migration, error) {
	entry, err := r.progress(ctx, connection)
	if err != nil {
		return nil, err
	}
	completed := make([]Migration, 0)
	if entry != nil {
		if err := r.validateProgress(entry, migrations); err != nil {
			return nil, err
		}
		if entry.direction != DirectionDown {
			return nil, fmt.Errorf("migrate: revert cannot resume %s progress; reconcile or apply it first", entry.direction)
		}
		if entry.nextIndex <= entry.sourceIndex {
			return nil, incompleteError(*entry, errors.New("source outcome is uncertain; reconcile it before retrying"))
		}
		migration := findProgressMigration(migrations, entry.id)
		statements, _ := progressStatements(migration, entry.direction)
		if entry.nextIndex == len(statements) {
			if err := r.finalizeProgress(ctx, connection, *entry, migration); err != nil {
				return nil, incompleteError(*entry, fmt.Errorf("finalize progress: %w", err))
			}
			completed = append(completed, exportMigrationsForResult([]Migration{{ID: migration.id, Statements: migration.statements, Down: migration.down}})...)
			entry = nil
		}
	}
	recorded, err := r.applied(ctx, connection)
	if err != nil {
		return nil, err
	}
	selected, err := selectReverts(recorded, migrations, target)
	if err != nil {
		return nil, err
	}
	if entry != nil {
		selected = []preparedMigration{findProgressMigration(migrations, entry.id)}
	}
	for _, migration := range selected {
		start := 0
		if entry != nil && entry.id == migration.id {
			start = entry.nextIndex
		}
		for index := start; index < len(migration.down); index++ {
			statement := migration.down[index]
			entry := progressEntry{id: migration.id, checksum: migration.checksum, direction: DirectionDown, sourceIndex: index, source: statement.Source, nextIndex: index}
			if err := r.upsertProgress(ctx, connection, migration, DirectionDown, index); err != nil {
				return exportMigrationsForResult(completed), incompleteError(entry, fmt.Errorf("write progress: %w", err))
			}
			if _, err := connection.ExecContext(ctx, string(statement.SQL)); err != nil {
				return exportMigrationsForResult(completed), incompleteError(entry, fmt.Errorf("execute source: %w", err))
			}
			if err := r.checkpointProgress(ctx, connection, migration, DirectionDown, index); err != nil {
				return exportMigrationsForResult(completed), incompleteError(entry, fmt.Errorf("checkpoint source: %w", err))
			}
		}
		terminal := progressEntry{id: migration.id, checksum: migration.checksum, direction: DirectionDown, sourceIndex: len(migration.down) - 1, source: migration.down[len(migration.down)-1].Source, nextIndex: len(migration.down)}
		if err := r.finalizeProgress(ctx, connection, terminal, migration); err != nil {
			return exportMigrationsForResult(completed), incompleteError(terminal, fmt.Errorf("finalize progress: %w", err))
		}
		completed = append(completed, Migration{ID: migration.id, Mode: migration.mode, Statements: append([]Statement(nil), migration.statements...), Down: append([]Statement(nil), migration.down...)})
	}
	return exportMigrationsForResult(completed), nil
}

func exportMigrationsForResult(migrations []Migration) []Migration {
	return append([]Migration(nil), migrations...)
}
