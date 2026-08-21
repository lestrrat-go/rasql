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
	// CapabilityQualifiedReference reports that a REFERENCES clause accepts a
	// schema-qualified table name.
	CapabilityQualifiedReference
	// CapabilityQualifiedIndexTarget reports that CREATE INDEX qualifies the
	// indexed table and leaves the index name bare.
	CapabilityQualifiedIndexTarget
	// CapabilityQualifiedIndexName reports that CREATE INDEX qualifies the
	// index name and leaves the indexed table bare, which is SQLite's form.
	CapabilityQualifiedIndexName
	// CapabilityPartialIndex reports that CREATE INDEX accepts a WHERE
	// clause, restricting the index to rows matching a predicate.
	// PostgreSQL and SQLite both have this; MySQL rejects a WHERE clause
	// on CREATE INDEX with a syntax error.
	CapabilityPartialIndex
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
	// column's name carries its precision and scale, and an integer column's
	// name carries its signedness. It reports an error for a logical type this
	// dialect does not map, for a decimal whose precision or scale is outside
	// what this dialect can express, and for an unsigned column on a dialect
	// with no unsigned integer type.
	TypeName(schema.ColumnDef) (string, error)

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
		capabilities: CapabilityReturning | CapabilityUpsert | CapabilityConflictTarget | CapabilityDefaultValues | CapabilityDefaultValuesUpsert | CapabilitySubqueryLimit | CapabilityQualifiedReference | CapabilityQualifiedIndexTarget | CapabilityPartialIndex,
		decimalName:  "NUMERIC",
		maxPrecision: 1000,
		maxScale:     1000,
		// PostgreSQL's VARCHAR(n) rejects an over-length value whatever the
		// server settings, truncating instead only when the excess is all
		// spaces, where MySQL rejects it only under strict SQL mode, so a
		// stated schema.TextType.Width renders VARCHAR(n) here too rather
		// than being dropped: see builtin.textTypeName.
		varcharText: true,
		types: map[schema.TypeKind]string{
			schema.KindBoolean: "BOOLEAN",
			schema.KindInteger: "BIGINT",
			schema.KindFloat:   "DOUBLE PRECISION",
			schema.KindText:    "TEXT",
			schema.KindBytes:   "BYTEA",
			schema.KindTime:    "TIMESTAMPTZ",
			schema.KindJSON:    "JSONB",
			schema.KindUUID:    "UUID",
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
		capabilities: CapabilityUpsert | CapabilityDefaultValuesUpsert | CapabilityEmptyInsert | CapabilityQualifiedReference | CapabilityQualifiedIndexTarget,
		decimalName:  "DECIMAL",
		maxPrecision: 65,
		maxScale:     30,
		// MySQL is the only supported engine with unsigned integer types, so it
		// is the only builtin that states one here.
		unsignedInteger: "BIGINT UNSIGNED",
		// A stated schema.TextType.Width renders VARCHAR(n): see
		// builtin.textTypeName. This is also what lets a text column carry a
		// key length short enough for a MySQL index, primary key, or unique
		// constraint; render.CreateTable and render.CreateIndexes refuse an
		// unbounded one used that way instead of forwarding MySQL's opaque
		// error 1170.
		varcharText: true,
		types: map[schema.TypeKind]string{
			schema.KindBoolean: "BOOLEAN",
			schema.KindInteger: "BIGINT",
			schema.KindFloat:   "DOUBLE",
			schema.KindText:    "TEXT",
			schema.KindBytes:   "BLOB",
			schema.KindTime:    "DATETIME",
			schema.KindJSON:    "JSON",
			schema.KindUUID:    "CHAR(36)",
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
		capabilities: CapabilityReturning | CapabilityUpsert | CapabilityConflictTarget | CapabilityDefaultValues | CapabilitySubqueryLimit | CapabilityQualifiedIndexName | CapabilityPartialIndex,
		decimalName:  "TEXT",
		// varcharText is left false: SQLite already drops schema.DecimalType's
		// Precision and Scale for the same reason (see decimalTypeName below),
		// and a stated schema.TextType.Width gets the same treatment here, for
		// the same reason. SQLite assigns column type by affinity rather than
		// by declared type, so a VARCHAR(n) column is stored exactly like a
		// bare TEXT one and enforces no maximum length; rendering VARCHAR(n)
		// syntax there would claim an enforcement SQLite does not perform.
		// SQLite also never hits the problem Width exists to solve: unlike
		// MySQL, it indexes, and builds a primary key or unique constraint
		// over, an unbounded TEXT column natively.
		types: map[schema.TypeKind]string{
			schema.KindBoolean: "INTEGER",
			schema.KindInteger: "INTEGER",
			schema.KindFloat:   "REAL",
			schema.KindText:    "TEXT",
			schema.KindBytes:   "BLOB",
			schema.KindTime:    "TEXT",
			schema.KindJSON:    "TEXT",
			schema.KindUUID:    "TEXT",
		},
	}
}

type builtin struct {
	name         string
	quote        rune
	placeholder  func(int) string
	upsert       UpsertStyle
	capabilities Capability
	types        map[schema.TypeKind]string
	decimalName  string
	maxPrecision int
	maxScale     int
	// unsignedInteger is the DDL type for an unsigned schema.IntegerType
	// column, and is empty on a dialect that has no unsigned integer type.
	unsignedInteger string
	// varcharText reports whether a stated schema.TextType.Width renders as
	// VARCHAR(width) on this dialect. It is false on a dialect that cannot
	// enforce a maximum length, which renders its plain unbounded text type
	// instead of claiming an enforcement it will not perform.
	varcharText bool
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

func (d builtin) TypeName(column schema.ColumnDef) (string, error) {
	if column.Type == nil {
		return "", fmt.Errorf("dialect %s: unsupported nil column type", d.name)
	}
	switch typed := column.Type.(type) {
	case schema.IntegerType:
		if typed.Unsigned {
			return d.unsignedTypeName(column)
		}
	case schema.DecimalType:
		return d.decimalTypeName(column, typed)
	case schema.TextType:
		return d.textTypeName(typed), nil
	case schema.BooleanType, schema.FloatType, schema.BytesType, schema.TimeType, schema.JSONType, schema.UUIDType:
	default:
		return "", fmt.Errorf("dialect %s: unsupported column type %T", d.name, column.Type)
	}
	typeName, ok := d.types[column.Type.Kind()]
	if !ok {
		return "", fmt.Errorf("dialect %s: unsupported column type %q", d.name, column.Type.Kind())
	}
	return typeName, nil
}

// unsignedTypeName renders the DDL type for a column that states no negative
// values. Only MySQL has such a type, so PostgreSQL and SQLite refuse the
// column here rather than render it signed: a signed BIGINT stops at
// 9223372036854775807 where an unsigned one reaches 18446744073709551615, and
// a column silently rendered signed would reject values the descriptor permits.
// Refusing is loud and recoverable, and the caller's fix is to declare the
// column signed and say so in the descriptor.
func (d builtin) unsignedTypeName(column schema.ColumnDef) (string, error) {
	if d.unsignedInteger == "" {
		return "", fmt.Errorf("dialect %s: unsigned integer column %q cannot be represented: this dialect has no unsigned integer type, and rendering the column signed would narrow the values it permits", d.name, column.Name)
	}
	return d.unsignedInteger, nil
}

// textTypeName renders the DDL type for a TextType column. An unstated width
// (the zero value of schema.TextType.Width) always renders this dialect's
// plain, unbounded text type. A stated width renders VARCHAR(width), or
// CHAR(width) when text.Fixed says the column is fixed-width, on a dialect
// that enforces it (see the varcharText field on each builtin above); on one
// that does not, the width is dropped and the plain text type renders
// instead, the same way decimalTypeName below drops SQLite's decimal
// precision and scale rather than pretend to enforce them. SQLite's
// varcharText is always false, so a stated Fixed there renders plain TEXT
// too, for the same reason: SQLite's type affinity never enforces a
// declared width, fixed or not.
func (d builtin) textTypeName(text schema.TextType) string {
	width, stated := text.Width.Value()
	if !stated || !d.varcharText {
		return d.types[schema.KindText]
	}
	if text.Fixed {
		return fmt.Sprintf("CHAR(%d)", width)
	}
	return fmt.Sprintf("VARCHAR(%d)", width)
}

// decimalTypeName renders the DDL type for a DecimalType column. Every dialect
// requires a stated scale, because a descriptor that leaves it unstated does
// not say what the column means. SQLite then has no bound to check and renders
// its decimalName with no precision/scale suffix; PostgreSQL and MySQL each
// enforce their own maximum and render NAME(p,s).
func (d builtin) decimalTypeName(column schema.ColumnDef, decimal schema.DecimalType) (string, error) {
	scale, stated := decimal.Scale.Value()
	if !stated {
		return "", fmt.Errorf("dialect %s: decimal column %q states no scale", d.name, column.Name)
	}
	if d.maxPrecision == 0 && d.maxScale == 0 {
		return d.decimalName, nil
	}
	if decimal.Precision > d.maxPrecision {
		return "", fmt.Errorf("dialect %s: decimal precision %d exceeds the maximum of %d", d.name, decimal.Precision, d.maxPrecision)
	}
	if scale > d.maxScale {
		return "", fmt.Errorf("dialect %s: decimal scale %d exceeds the maximum of %d", d.name, scale, d.maxScale)
	}
	return fmt.Sprintf("%s(%d,%d)", d.decimalName, decimal.Precision, scale), nil
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
