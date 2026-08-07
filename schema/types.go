// Package schema defines dialect-neutral database schema descriptors.
//
// Every exported descriptor struct in this package is built with a keyed
// composite literal, and every literal of one in this repository is keyed.
// These structs gain fields as the descriptor model grows, and a new field is
// placed next to the fields it belongs with rather than appended at the end:
// Table.Schema and ForeignKey.ReferencedSchema were each inserted ahead of
// existing fields. An unkeyed composite literal matches fields by position and
// must list every one of them, so it is not a supported way to build a
// descriptor.
package schema

import "fmt"

// LogicalType identifies the logical type of a column.
type LogicalType string

const (
	TypeBoolean LogicalType = "boolean"
	TypeInteger LogicalType = "integer"
	TypeFloat   LogicalType = "float"
	TypeText    LogicalType = "text"
	TypeBytes   LogicalType = "bytes"
	TypeTime    LogicalType = "time"
	TypeJSON    LogicalType = "json"
	TypeUUID    LogicalType = "uuid"

	// TypeDecimal is an exact decimal number. Unlike TypeFloat it never rounds,
	// so it is the type for money, quantities, and any other value where a
	// binary floating-point approximation is wrong. A column of this type must
	// state a Precision and a Scale.
	TypeDecimal LogicalType = "decimal"
)

// Valid reports whether the logical type is supported by the core schema model.
func (t LogicalType) Valid() bool {
	switch t {
	case TypeBoolean, TypeInteger, TypeFloat, TypeText, TypeBytes, TypeTime, TypeJSON, TypeUUID, TypeDecimal:
		return true
	default:
		return false
	}
}

// ReferenceAction defines the action to take when a referenced row changes.
type ReferenceAction string

const (
	ReferenceActionNoAction   ReferenceAction = "NO ACTION"
	ReferenceActionRestrict   ReferenceAction = "RESTRICT"
	ReferenceActionCascade    ReferenceAction = "CASCADE"
	ReferenceActionSetNull    ReferenceAction = "SET NULL"
	ReferenceActionSetDefault ReferenceAction = "SET DEFAULT"
)

func (a ReferenceAction) valid() bool {
	switch a {
	case "", ReferenceActionNoAction, ReferenceActionRestrict, ReferenceActionCascade, ReferenceActionSetNull, ReferenceActionSetDefault:
		return true
	default:
		return false
	}
}

// ValidationError identifies an invalid part of a schema descriptor.
type ValidationError struct {
	Path    string
	Message string
}

func (e *ValidationError) Error() string {
	if e.Path == "" {
		return e.Message
	}
	return fmt.Sprintf("schema: %s: %s", e.Path, e.Message)
}

func validationError(path string, format string, args ...any) error {
	return &ValidationError{Path: path, Message: fmt.Sprintf(format, args...)}
}

// ValidateIdentifier reports whether name is a simple SQL identifier.
// Dialects quote validated identifiers when rendering SQL.
func ValidateIdentifier(name string) error {
	if name == "" {
		return validationError("identifier", "must not be empty")
	}
	for i, r := range name {
		if i == 0 {
			if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') {
				return validationError("identifier", "must start with a letter or underscore")
			}
			continue
		}
		if r != '_' && (r < 'A' || r > 'Z') && (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return validationError("identifier", "contains invalid character %q", r)
		}
	}
	return nil
}
