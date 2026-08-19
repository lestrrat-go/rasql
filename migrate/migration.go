// Package migrate applies ordered SQL migrations, and reverses them.
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Migration is one ordered database change and the sources that undo it.
//
// Every Statement contains native SQL. The runner sends the source unchanged
// to the database driver and does not parse, split, or render it.
type Migration struct {
	ID string

	// Statements are the forward sources, in the order they are applied.
	// They alone form the recorded checksum.
	Statements []Statement

	// Down are the reverse sources, in the order they run to undo this
	// migration. They are deliberately not part of the checksum, so a
	// reverse script can be added or corrected for a migration that is
	// already applied without invalidating its history record.
	//
	// Every migration read from disk has them, because a migration with no
	// reverse source fails to load. A Migration built in Go may leave them
	// empty, and Revert then refuses to select it.
	Down []Statement
}

// Statement is one native SQL source file within a Migration.
// Source identifies the file in errors and contributes to the migration
// checksum. SQL must contain one database statement.
type Statement struct {
	Source string
	SQL    string
}

// Validate reports whether m has a usable ID and SQL sources.
func (m Migration) Validate() error {
	return m.validate()
}

func (m Migration) validate() error {
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
		if strings.TrimSpace(statement.SQL) == "" {
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
	hash := sha256.New()
	for _, statement := range statements {
		hash.Write([]byte(statement.Source))
		hash.Write([]byte{0})
		hash.Write([]byte(statement.SQL))
		hash.Write([]byte{0})
	}
	return hex.EncodeToString(hash.Sum(nil))
}
