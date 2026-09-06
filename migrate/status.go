package migrate

import (
	"context"
	"fmt"
	"sort"
)

// StatusState describes one migration's state in a database.
type StatusState string

const (
	// StatusApplied identifies a migration recorded with its current checksum.
	StatusApplied StatusState = "applied"
	// StatusPending identifies a migration that has not been recorded.
	StatusPending StatusState = "pending"
	// StatusChanged identifies a migration whose recorded checksum differs.
	StatusChanged StatusState = "changed"
	// StatusOutOfOrder identifies a recorded migration after a pending migration.
	StatusOutOfOrder StatusState = "out_of_order"
	// StatusUnknown identifies a recorded migration absent from the supplied set.
	StatusUnknown    StatusState = "unknown"
	StatusIncomplete StatusState = "incomplete"
)

// StatusEntry reports the database state of one migration ID.
type StatusEntry struct {
	ID         string
	State      StatusState
	Incomplete *IncompleteMigration
}

// Status reads migration history and reports every supplied and recorded migration.
// It creates the migration-history table when it does not exist.
func (r Runner) Status(ctx context.Context, migrations ...Migration) ([]StatusEntry, error) {
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
	var result []StatusEntry
	observe := func() error {
		if err := r.ensureHistory(ctx, connection); err != nil {
			return err
		}
		var progress *progressEntry
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
				if progress.direction != DirectionUp && progress.direction != DirectionDown {
					return fmt.Errorf("migrate: invalid progress direction %q", progress.direction)
				}
				migration := findProgressMigration(prepared, progress.id)
				statements, _ := progressStatements(migration, progress.direction)
				if progress.nextIndex == len(statements) && progress.nextIndex == progress.sourceIndex+1 {
					if err := r.finalizeProgress(ctx, connection, *progress, migration); err != nil {
						return incompleteError(*progress, err)
					}
					progress = nil
				}
			}
		}
		applied, err := r.applied(ctx, connection)
		if err != nil {
			return err
		}
		result = statusEntries(applied, prepared, progress)
		return nil
	}
	if r.dialect.Name() == "mysql" {
		if err := r.withMySQLReadLock(ctx, connection, observe); err != nil {
			return nil, err
		}
	} else if r.dialect.Name() == "postgresql" {
		if err := r.withPostgreSQLReadLock(ctx, connection, observe); err != nil {
			return nil, err
		}
	} else if err := observe(); err != nil {
		return nil, err
	}
	return result, nil
}

func statusEntries(applied map[string]string, migrations []preparedMigration, progress *progressEntry) []StatusEntry {
	expected := make(map[string]struct{}, len(migrations))
	entries := make([]StatusEntry, 0, len(applied)+len(migrations))
	for _, migration := range migrations {
		expected[migration.id] = struct{}{}
	}
	unknown := make([]string, 0)
	for id := range applied {
		if _, exists := expected[id]; !exists {
			unknown = append(unknown, id)
		}
	}
	sort.Strings(unknown)
	for _, id := range unknown {
		entries = append(entries, StatusEntry{ID: id, State: StatusUnknown})
	}

	pending := false
	for _, migration := range migrations {
		recordedChecksum, exists := applied[migration.id]
		if progress != nil && progress.id == migration.id {
			value := IncompleteMigration{ID: progress.id, Checksum: progress.checksum, Source: progress.source, Direction: progress.direction, SourceIndex: progress.sourceIndex}
			entries = append(entries, StatusEntry{ID: migration.id, State: StatusIncomplete, Incomplete: &value})
			continue
		}
		if !exists {
			pending = true
			entries = append(entries, StatusEntry{ID: migration.id, State: StatusPending})
			continue
		}
		state := StatusApplied
		if recordedChecksum != migration.checksum {
			state = StatusChanged
		} else if pending {
			state = StatusOutOfOrder
		}
		entries = append(entries, StatusEntry{ID: migration.id, State: state})
	}
	return entries
}
