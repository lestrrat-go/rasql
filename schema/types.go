// Package schema defines dialect-neutral database schema descriptors.
//
// A Table is built either with NewTable or MustTable, which assemble a
// descriptor from Column and constraint options such as Integer, PrimaryKey,
// and ForeignKey, or with a keyed composite literal. Every exported
// descriptor struct in this package accepts a keyed composite literal, and
// every literal of one in this repository is keyed. These structs gain
// fields as the descriptor model grows, and a new field is placed next to
// the fields it belongs with rather than appended at the end: Table.Schema
// and ForeignKeyDef.ReferencedSchema were each inserted ahead of existing
// fields. An unkeyed composite literal matches fields by position and must
// list every one of them, so it is not a supported way to build a
// descriptor.
package schema

import "fmt"

// ReferenceAction defines the action to take when a referenced row changes.
type ReferenceAction string

const (
	NoAction   ReferenceAction = "NO ACTION"
	Restrict   ReferenceAction = "RESTRICT"
	Cascade    ReferenceAction = "CASCADE"
	SetNull    ReferenceAction = "SET NULL"
	SetDefault ReferenceAction = "SET DEFAULT"
)

func (a ReferenceAction) valid() bool {
	switch a {
	case "", NoAction, Restrict, Cascade, SetNull, SetDefault:
		return true
	default:
		return false
	}
}

// RelationshipKind identifies the relationship shape represented by a
// descriptor.
type RelationshipKind string

const (
	// RelationshipBelongsTo identifies a row that points at one related row
	// through a foreign key.
	RelationshipBelongsTo RelationshipKind = "belongs_to"
	// RelationshipHasMany identifies the inverse collection of a belongs-to
	// relationship.
	RelationshipHasMany RelationshipKind = "has_many"
)

// Relationship describes a navigable relationship derived from a foreign key.
// The first relationship slice supports belongs-to relationships. The column
// lists are copied by Table.Relationships, so callers may inspect them safely.
type Relationship struct {
	Name              string
	Kind              RelationshipKind
	Columns           []string
	ReferencedSchema  string
	ReferencedTable   string
	ReferencedColumns []string
}

// Clone returns a copy of r that does not share slices with r.
func (r Relationship) Clone() Relationship {
	r.Columns = append([]string(nil), r.Columns...)
	r.ReferencedColumns = append([]string(nil), r.ReferencedColumns...)
	return r
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
