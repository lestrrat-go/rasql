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
	CapabilityDefaultValues
	CapabilityEmptyInsert
	CapabilityDefaultValuesUpsert
	// CapabilitySubqueryLimit reports whether a subquery on the right-hand side
	// of IN may set a LIMIT or an OFFSET. MySQL rejects that combination.
	CapabilitySubqueryLimit
)

// UpsertStyle identifies a dialect's conflict-handling syntax.
type UpsertStyle uint8

const (
	UpsertUnsupported UpsertStyle = iota
	UpsertOnConflict
	UpsertDuplicateKey
)

// Dialect defines database-specific SQL rendering rules.
type Dialect interface {
	Name() string
	QuoteIdentifier(string) (string, error)
	Placeholder(int) (string, error)

	// TypeName returns the DDL type for column. It takes the whole column
	// because a type name can depend on more than the logical type: a decimal
	// column's name carries its precision and scale. It reports an error for a
	// logical type this dialect does not map, and for a decimal whose
	// precision or scale is outside what this dialect can express.
	TypeName(schema.Column) (string, error)

	UpsertStyle() UpsertStyle
	Supports(Capability) bool
}

// PostgreSQL returns the PostgreSQL dialect.
func PostgreSQL() Dialect {
	return builtin{
		name:         "postgresql",
		quote:        '"',
		placeholder:  dollarPlaceholder,
		upsert:       UpsertOnConflict,
		capabilities: CapabilityReturning | CapabilityUpsert | CapabilityConflictTarget | CapabilityDefaultValues | CapabilityDefaultValuesUpsert | CapabilitySubqueryLimit,
		decimalName:  "NUMERIC",
		maxPrecision: 1000,
		maxScale:     1000,
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
		upsert:       UpsertDuplicateKey,
		capabilities: CapabilityUpsert | CapabilityDefaultValuesUpsert | CapabilityEmptyInsert,
		decimalName:  "DECIMAL",
		maxPrecision: 65,
		maxScale:     30,
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
		upsert:       UpsertOnConflict,
		capabilities: CapabilityReturning | CapabilityUpsert | CapabilityConflictTarget | CapabilityDefaultValues | CapabilitySubqueryLimit,
		decimalName:  "TEXT",
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

type builtin struct {
	name         string
	quote        rune
	placeholder  func(int) string
	upsert       UpsertStyle
	capabilities Capability
	types        map[schema.LogicalType]string
	decimalName  string
	maxPrecision int
	maxScale     int
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

func (d builtin) TypeName(column schema.Column) (string, error) {
	if column.Type == schema.TypeDecimal {
		return d.decimalTypeName(column)
	}
	typeName, ok := d.types[column.Type]
	if !ok {
		return "", fmt.Errorf("dialect %s: unsupported logical type %q", d.name, column.Type)
	}
	return typeName, nil
}

// decimalTypeName renders the DDL type for a TypeDecimal column. SQLite has
// no bound to check and renders its decimalName with no precision/scale
// suffix; PostgreSQL and MySQL each enforce their own maximum and render
// NAME(p,s).
func (d builtin) decimalTypeName(column schema.Column) (string, error) {
	if d.maxPrecision == 0 && d.maxScale == 0 {
		return d.decimalName, nil
	}
	if column.Precision > d.maxPrecision {
		return "", fmt.Errorf("dialect %s: decimal precision %d exceeds the maximum of %d", d.name, column.Precision, d.maxPrecision)
	}
	if column.Scale > d.maxScale {
		return "", fmt.Errorf("dialect %s: decimal scale %d exceeds the maximum of %d", d.name, column.Scale, d.maxScale)
	}
	return fmt.Sprintf("%s(%d,%d)", d.decimalName, column.Precision, column.Scale), nil
}

func (d builtin) UpsertStyle() UpsertStyle {
	return d.upsert
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
