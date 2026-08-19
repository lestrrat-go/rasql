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

// Runner applies migrations to one database and records each completed migration.
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

// Apply executes migrations in ID order.
// PostgreSQL and SQLite apply a complete migration atomically. MySQL DDL may
// commit implicitly, so a failure can leave completed statements in place and
// the migration unrecorded; resolve that state before running Apply again.
func (r Runner) Apply(ctx context.Context, migrations ...Migration) error {
	if err := r.validate(); err != nil {
		return err
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

	switch r.dialect.Name() {
	case "postgresql":
		return r.applyPostgreSQL(ctx, connection, prepared)
	case "mysql":
		return r.applyMySQL(ctx, connection, prepared)
	case "sqlite":
		return r.applySQLite(ctx, connection, prepared)
	default:
		return fmt.Errorf("migrate: dialect %q is not supported", r.dialect.Name())
	}
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

func (r Runner) applyPostgreSQL(ctx context.Context, connection *sql.Conn, migrations []preparedMigration) error {
	if err := r.ensureHistory(ctx, connection); err != nil {
		return err
	}
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("migrate: begin PostgreSQL transaction: %w", err)
	}
	defer func() { _ = transaction.Rollback() }()
	if _, err := transaction.ExecContext(ctx, "LOCK TABLE "+r.historySQL+" IN EXCLUSIVE MODE"); err != nil {
		return fmt.Errorf("migrate: lock migration history: %w", err)
	}
	if err := r.applyPrepared(ctx, transaction, transaction, migrations); err != nil {
		return err
	}
	if err := transaction.Commit(); err != nil {
		return fmt.Errorf("migrate: commit PostgreSQL migration transaction: %w", err)
	}
	return nil
}

func (r Runner) applyMySQL(ctx context.Context, connection *sql.Conn, migrations []preparedMigration) error {
	var acquired int
	if err := connection.QueryRowContext(ctx, "SELECT GET_LOCK(?, ?)", r.historyTable, 30).Scan(&acquired); err != nil {
		return fmt.Errorf("migrate: acquire MySQL migration lock: %w", err)
	}
	if acquired != 1 {
		return fmt.Errorf("migrate: acquire MySQL migration lock: timed out")
	}
	defer r.releaseMySQLLock(connection)
	if err := r.ensureHistory(ctx, connection); err != nil {
		return err
	}
	return r.applyPrepared(ctx, connection, connection, migrations)
}

func (r Runner) releaseMySQLLock(connection *sql.Conn) {
	var released int
	_ = connection.QueryRowContext(context.Background(), "SELECT RELEASE_LOCK(?)", r.historyTable).Scan(&released)
}

func (r Runner) applySQLite(ctx context.Context, connection *sql.Conn, migrations []preparedMigration) error {
	if _, err := connection.ExecContext(ctx, "BEGIN IMMEDIATE"); err != nil {
		return fmt.Errorf("migrate: begin SQLite migration transaction: %w", err)
	}
	committed := false
	defer func() {
		if !committed {
			_, _ = connection.ExecContext(context.Background(), "ROLLBACK")
		}
	}()
	if err := r.ensureHistory(ctx, connection); err != nil {
		return err
	}
	if err := r.applyPrepared(ctx, connection, connection, migrations); err != nil {
		return err
	}
	if _, err := connection.ExecContext(ctx, "COMMIT"); err != nil {
		return fmt.Errorf("migrate: commit SQLite migration transaction: %w", err)
	}
	committed = true
	return nil
}

type queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func (r Runner) applyPrepared(ctx context.Context, queries queryer, executions executor, migrations []preparedMigration) error {
	applied, err := r.applied(ctx, queries)
	if err != nil {
		return err
	}
	expected := make(map[string]struct{}, len(migrations))
	for _, migration := range migrations {
		expected[migration.id] = struct{}{}
	}
	for id := range applied {
		if _, exists := expected[id]; !exists {
			return fmt.Errorf("migrate: recorded migration %q was not supplied", id)
		}
	}
	pending := false
	for _, migration := range migrations {
		recordedChecksum, exists := applied[migration.id]
		if exists {
			if recordedChecksum != migration.checksum {
				return fmt.Errorf("migrate: migration %q checksum does not match recorded migration", migration.id)
			}
			if pending {
				return fmt.Errorf("migrate: migration %q is recorded after a missing migration", migration.id)
			}
			continue
		}
		pending = true
		for _, statement := range migration.statements {
			if _, err := executions.ExecContext(ctx, statement.SQL); err != nil {
				return fmt.Errorf("migrate: execute migration %q SQL source %q: %w", migration.id, statement.Source, err)
			}
		}
		if err := r.record(ctx, executions, migration); err != nil {
			return err
		}
	}
	return nil
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
