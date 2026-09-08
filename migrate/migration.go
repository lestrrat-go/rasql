// Package migrate applies ordered SQL migrations, and reverses them.
package migrate

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/sqltext"
)

type Direction string

const (
	DirectionUp   Direction = "up"
	DirectionDown Direction = "down"
)

// ExecutionMode selects the execution policy for a migration. The zero value
// preserves the historical transactional behavior.
type ExecutionMode string

const (
	ExecutionModeAtomic           ExecutionMode = ""
	ExecutionModeNonTransactional ExecutionMode = "nontransactional"
)

type IncompleteMigration struct {
	ID          string
	Checksum    string
	Source      string
	Direction   Direction
	SourceIndex int
}

type ExecutionResult struct {
	Completed           []Migration
	Incomplete          *IncompleteMigration
	CompletedOperations []changeplan.Operation
	IncompleteOperation *IncompleteOperation
}

type IncompleteMigrationError struct {
	Incomplete IncompleteMigration
	Cause      error
}

func (e *IncompleteMigrationError) Error() string {
	return fmt.Sprintf("migrate: incomplete %s migration %q at source %q (index %d): %v", e.Incomplete.Direction, e.Incomplete.ID, e.Incomplete.Source, e.Incomplete.SourceIndex, e.Cause)
}

func (e *IncompleteMigrationError) Unwrap() error { return e.Cause }

type ReconcileDecision string

const (
	ReconcileExecuted    ReconcileDecision = "executed"
	ReconcileNotExecuted ReconcileDecision = "not_executed"
)

type ReconcileCheck interface {
	Check(context.Context, *sql.Conn, IncompleteMigration) (ReconcileDecision, error)
}

func executionResult(completed []Migration, err error) (ExecutionResult, error) {
	result := ExecutionResult{Completed: append([]Migration(nil), completed...)}
	var incomplete *IncompleteMigrationError
	if errors.As(err, &incomplete) {
		value := incomplete.Incomplete
		result.Incomplete = &value
	}
	return result, err
}

// Migration is one ordered database change and the sources that undo it.
//
// Every Statement contains native SQL. The runner sends the source unchanged
// to the database driver and does not parse, split, or render it.
type Migration struct {
	ID   string
	Mode ExecutionMode

	// Statements are the forward sources, in the order they are applied.
	// They alone form the recorded checksum.
	Statements []Statement

	// Down are the reverse sources, in the order they run to undo this
	// migration. They are deliberately not part of the checksum, so a
	// reverse script can be added or corrected for a migration that is
	// already applied without invalidating its history record.
	//
	// A migration read from disk may have no reverse sources when it carries
	// an explicit irreversibility marker. A Migration built in Go may also
	// leave them empty, and Revert then refuses to select it.
	Down []Statement
}

// Statement is one native SQL source file within a Migration.
// Source identifies the file in errors and contributes to the migration
// checksum. SQL must contain one database statement.
type Statement struct {
	Source string
	SQL    sqltext.Text
}

// Validate reports whether m has a usable ID and SQL sources.
func (m Migration) Validate() error {
	return m.validate()
}

func (m Migration) validate() error {
	if m.Mode != ExecutionModeAtomic && m.Mode != ExecutionModeNonTransactional {
		return fmt.Errorf("migrate: migration %q has invalid execution mode %q", m.ID, m.Mode)
	}
	if err := validateMigrationID(m.ID); err != nil {
		return err
	}
	if len(m.Statements) == 0 {
		return fmt.Errorf("migrate: migration %q must contain at least one SQL source", m.ID)
	}
	if err := m.validateStatements(m.Statements); err != nil {
		return err
	}
	// Down is checked by the same rules, and separately: a reverse source
	// may reuse a forward source's name, since the two sets are executed by
	// different calls and each is reported by its own file name.
	return m.validateStatements(m.Down)
}

// validateStatements checks one set of sources. An empty set is valid here;
// whether a set may be empty is the caller's question, because Statements
// must not be and Down may be.
func (m Migration) validateStatements(statements []Statement) error {
	sources := make(map[string]struct{}, len(statements))
	for index, statement := range statements {
		if statement.Source == "" || !utf8.ValidString(statement.Source) || strings.ContainsRune(statement.Source, '\x00') {
			return fmt.Errorf("migrate: migration %q SQL source %d is invalid", m.ID, index+1)
		}
		if _, exists := sources[statement.Source]; exists {
			return fmt.Errorf("migrate: migration %q contains duplicate SQL source %q", m.ID, statement.Source)
		}
		if strings.TrimSpace(string(statement.SQL)) == "" {
			return fmt.Errorf("migrate: migration %q SQL source %q is empty", m.ID, statement.Source)
		}
		sources[statement.Source] = struct{}{}
	}
	return nil
}

func validateMigrationID(id string) error {
	if id == "" {
		return fmt.Errorf("migrate: migration ID must not be empty")
	}
	if !utf8.ValidString(id) || len(id) > 255 || strings.ContainsRune(id, '\x00') {
		return fmt.Errorf("migrate: migration ID %q is invalid", id)
	}
	return nil
}

func checksum(statements []Statement) string {
	return checksumMode(ExecutionModeAtomic, statements)
}

func checksumMode(mode ExecutionMode, statements []Statement) string {
	hash := sha256.New()
	if mode == ExecutionModeNonTransactional {
		hash.Write([]byte("rasql-execution-mode\x00nontransactional\x00"))
	}
	for _, statement := range statements {
		hash.Write([]byte(statement.Source))
		hash.Write([]byte{0})
		hash.Write([]byte(statement.SQL))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
