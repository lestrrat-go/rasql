// Package dialect defines SQL rendering rules for supported databases.
package dialect

import (
	"fmt"

	"github.com/lestrrat-go/rasql/schema"
)

// Capability identifies optional SQL syntax supported by a dialect.
type Capability uint32

const (
	CapabilityReturning Capability = 1 << iota
	CapabilityUpsert
	CapabilityConflictTarget
)

// Dialect defines database-specific SQL rendering rules.
type Dialect interface {
	Name() string
	QuoteIdentifier(string) (string, error)
	Placeholder(int) (string, error)
	TypeName(schema.LogicalType) (string, error)
	Supports(Capability) bool
}

// PostgreSQL returns the PostgreSQL dialect.
func PostgreSQL() Dialect {
	return builtin{
		name:         "postgresql",
		quote:        '"',
		placeholder:  dollarPlaceholder,
		capabilities: CapabilityReturning | CapabilityUpsert | CapabilityConflictTarget,
		types: map[schema.LogicalType]string{
			schema.TypeBoolean: "BOOLEAN",
			schema.TypeInteger: "BIGINT",
			schema.TypeFloat:   "DOUBLE PRECISION",
			schema.TypeText:    "TEXT",
			schema.TypeBytes:   "BYTEA",
			schema.TypeTime:    "TIMESTAMPTZ",
			schema.TypeJSON:    "JSONB",
			schema.TypeUUID:    "UUID",
		},
	}
}

// MySQL returns the MySQL dialect.
func MySQL() Dialect {
	return builtin{
		name:         "mysql",
		quote:        '`',
		placeholder:  questionPlaceholder,
		capabilities: CapabilityUpsert,
		types: map[schema.LogicalType]string{
			schema.TypeBoolean: "BOOLEAN",
			schema.TypeInteger: "BIGINT",
			schema.TypeFloat:   "DOUBLE",
			schema.TypeText:    "TEXT",
			schema.TypeBytes:   "BLOB",
			schema.TypeTime:    "DATETIME",
			schema.TypeJSON:    "JSON",
			schema.TypeUUID:    "CHAR(36)",
		},
	}
}

// SQLite returns the SQLite dialect.
func SQLite() Dialect {
	return builtin{
		name:         "sqlite",
		quote:        '"',
		placeholder:  questionPlaceholder,
		capabilities: CapabilityReturning | CapabilityUpsert | CapabilityConflictTarget,
		types: map[schema.LogicalType]string{
			schema.TypeBoolean: "INTEGER",
			schema.TypeInteger: "INTEGER",
			schema.TypeFloat:   "REAL",
			schema.TypeText:    "TEXT",
			schema.TypeBytes:   "BLOB",
			schema.TypeTime:    "TEXT",
			schema.TypeJSON:    "TEXT",
			schema.TypeUUID:    "TEXT",
		},
	}
}

// Spanner returns the Google Cloud Spanner dialect.
func Spanner() Dialect {
	return builtin{
		name:        "spanner",
		quote:       '`',
		placeholder: namedPlaceholder,
		types: map[schema.LogicalType]string{
			schema.TypeBoolean: "BOOL",
			schema.TypeInteger: "INT64",
			schema.TypeFloat:   "FLOAT64",
			schema.TypeText:    "STRING(MAX)",
			schema.TypeBytes:   "BYTES(MAX)",
			schema.TypeTime:    "TIMESTAMP",
			schema.TypeJSON:    "JSON",
			schema.TypeUUID:    "STRING(36)",
		},
	}
}

type builtin struct {
	name         string
	quote        rune
	placeholder  func(int) string
	capabilities Capability
	types        map[schema.LogicalType]string
}

func (d builtin) Name() string {
	return d.name
}

func (d builtin) QuoteIdentifier(name string) (string, error) {
	if err := schema.ValidateIdentifier(name); err != nil {
		return "", fmt.Errorf("dialect %s: invalid identifier: %w", d.name, err)
	}
	return string(d.quote) + name + string(d.quote), nil
}

func (d builtin) Placeholder(position int) (string, error) {
	if position < 1 {
		return "", fmt.Errorf("dialect %s: placeholder position must be positive", d.name)
	}
	return d.placeholder(position), nil
}

func (d builtin) TypeName(logicalType schema.LogicalType) (string, error) {
	typeName, ok := d.types[logicalType]
	if !ok {
		return "", fmt.Errorf("dialect %s: unsupported logical type %q", d.name, logicalType)
	}
	return typeName, nil
}

func (d builtin) Supports(capability Capability) bool {
	return d.capabilities&capability == capability
}

func dollarPlaceholder(position int) string {
	return fmt.Sprintf("$%d", position)
}

func questionPlaceholder(int) string {
	return "?"
}

func namedPlaceholder(position int) string {
	return fmt.Sprintf("@p%d", position)
}
