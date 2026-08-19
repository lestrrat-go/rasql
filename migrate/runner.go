package migrate

import (
	"context"
	"database/sql"
	"fmt"
	"sort"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
)

const defaultHistoryTable = "rasql_schema_migrations"

// Runner applies and reverts migrations on one database, and records each
// completed migration.
// Its configuration is immutable, so Apply is safe to invoke concurrently.
type Runner struct {
	database     *sql.DB
	dialect      dialect.Dialect
	historyTable string
	historySQL   string
	idSQL        string
	checksumSQL  string
	appliedAtSQL string
}

// New creates a Runner with the default migration-history table.
func New(database *sql.DB, d dialect.Dialect) (Runner, error) {
	return NewWithHistoryTable(database, d, defaultHistoryTable)
}

// NewWithHistoryTable creates a Runner that stores migration records in historyTable.
func NewWithHistoryTable(database *sql.DB, d dialect.Dialect, historyTable string) (Runner, error) {
	if database == nil {
		return Runner{}, fmt.Errorf("migrate: database must not be nil")
	}
	if d == nil {
		return Runner{}, fmt.Errorf("migrate: dialect must not be nil")
	}
	if err := schema.ValidateIdentifier(historyTable); err != nil {
		return Runner{}, fmt.Errorf("migrate: history table: %w", err)
	}
	switch d.Name() {
	case "postgresql", "mysql", "sqlite":
	default:
		return Runner{}, fmt.Errorf("migrate: dialect %q is not supported", d.Name())
	}
	historySQL, err := d.QuoteIdentifier(historyTable)
	if err != nil {
		return Runner{}, fmt.Errorf("migrate: quote history table: %w", err)
	}
	idSQL, err := d.QuoteIdentifier("id")
	if err != nil {
		return Runner{}, fmt.Errorf("migrate: quote migration ID column: %w", err)
	}
	checksumSQL, err := d.QuoteIdentifier("checksum")
	if err != nil {
		return Runner{}, fmt.Errorf("migrate: quote migration checksum column: %w", err)
	}
	appliedAtSQL, err := d.QuoteIdentifier("applied_at")
	if err != nil {
		return Runner{}, fmt.Errorf("migrate: quote migration applied-at column: %w", err)
	}
	return Runner{
		database:     database,
		dialect:      d,
		historyTable: historyTable,
		historySQL:   historySQL,
		idSQL:        idSQL,
		checksumSQL:  checksumSQL,
		appliedAtSQL: appliedAtSQL,
	}, nil
}

func (r Runner) validate() error {
	if r.database == nil || r.dialect == nil || r.historyTable == "" || r.historySQL == "" || r.idSQL == "" || r.checksumSQL == "" || r.appliedAtSQL == "" {
		return fmt.Errorf("migrate: invalid runner")
	}
	return nil
}

type preparedMigration struct {
	id         string
	statements []Statement
	down       []Statement
	checksum   string
}

func prepareMigrations(migrations []Migration) ([]preparedMigration, error) {
	prepared := make([]preparedMigration, len(migrations))
	ids := make(map[string]struct{}, len(migrations))
	for index, migration := range migrations {
		if _, exists := ids[migration.ID]; exists {
			return nil, fmt.Errorf("migrate: duplicate migration ID %q", migration.ID)
		}
		if err := migration.validate(); err != nil {
			return nil, err
		}
		ids[migration.ID] = struct{}{}
		prepared[index] = preparedMigration{
			id:         migration.ID,
			statements: append([]Statement(nil), migration.Statements...),
			down:       append([]Statement(nil), migration.Down...),
			checksum:   checksum(migration.Statements),
		}
	}
	sort.Slice(prepared, func(left, right int) bool {
		return prepared[left].id < prepared[right].id
	})
	return prepared, nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (r Runner) ensureHistory(ctx context.Context, executions executor) error {
	statement, err := r.historyTableDDL()
	if err != nil {
		return err
	}
	if _, err := executions.ExecContext(ctx, statement); err != nil {
		return fmt.Errorf("migrate: create migration history: %w", err)
	}
	return nil
}

func (r Runner) historyTableDDL() (string, error) {
	switch r.dialect.Name() {
	case "postgresql":
		return "CREATE TABLE IF NOT EXISTS " + r.historySQL + " (" + r.idSQL + " TEXT NOT NULL PRIMARY KEY, " + r.checksumSQL + " TEXT NOT NULL, " + r.appliedAtSQL + " TIMESTAMPTZ NOT NULL DEFAULT CURRENT_TIMESTAMP)", nil
	case "mysql":
		return "CREATE TABLE IF NOT EXISTS " + r.historySQL + " (" + r.idSQL + " VARCHAR(255) NOT NULL PRIMARY KEY, " + r.checksumSQL + " CHAR(64) NOT NULL, " + r.appliedAtSQL + " TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP)", nil
	case "sqlite":
		return "CREATE TABLE IF NOT EXISTS " + r.historySQL + " (" + r.idSQL + " TEXT NOT NULL PRIMARY KEY, " + r.checksumSQL + " TEXT NOT NULL, " + r.appliedAtSQL + " TEXT NOT NULL DEFAULT CURRENT_TIMESTAMP)", nil
	default:
		return "", fmt.Errorf("migrate: dialect %q is not supported", r.dialect.Name())
	}
}

func (r Runner) applied(ctx context.Context, queries queryer) (map[string]string, error) {
	rows, err := queries.QueryContext(ctx, "SELECT "+r.idSQL+", "+r.checksumSQL+" FROM "+r.historySQL+" ORDER BY "+r.idSQL)
	if err != nil {
		return nil, fmt.Errorf("migrate: read applied migrations: %w", err)
	}
	defer func() { _ = rows.Close() }()
	applied := make(map[string]string)
	for rows.Next() {
		var id string
		var recordedChecksum string
		if err := rows.Scan(&id, &recordedChecksum); err != nil {
			return nil, fmt.Errorf("migrate: scan applied migration: %w", err)
		}
		if _, exists := applied[id]; exists {
			return nil, fmt.Errorf("migrate: migration history contains duplicate ID %q", id)
		}
		applied[id] = recordedChecksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("migrate: read applied migrations: %w", err)
	}
	return applied, nil
}

func (r Runner) record(ctx context.Context, executions executor, migration preparedMigration) error {
	firstPlaceholder, err := r.dialect.Placeholder(1)
	if err != nil {
		return fmt.Errorf("migrate: render migration history insert: %w", err)
	}
	secondPlaceholder, err := r.dialect.Placeholder(2)
	if err != nil {
		return fmt.Errorf("migrate: render migration history insert: %w", err)
	}
	statement := "INSERT INTO " + r.historySQL + " (" + r.idSQL + ", " + r.checksumSQL + ") VALUES (" + firstPlaceholder + ", " + secondPlaceholder + ")"
	if _, err := executions.ExecContext(ctx, statement, migration.id, migration.checksum); err != nil {
		return fmt.Errorf("migrate: record migration %q: %w", migration.id, err)
	}
	return nil
}
