// Package migrate applies ordered, forward-only SQL migrations.
package migrate

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Migration is one ordered, forward-only database change.
//
// Every Statement contains native SQL. The runner sends the source unchanged
// to the database driver and does not parse, split, or render it.
type Migration struct {
	ID         string
	Statements []Statement
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
	sources := make(map[string]struct{}, len(m.Statements))
	for index, statement := range m.Statements {
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
