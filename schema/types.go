// Package schema defines dialect-neutral database schema descriptors.
//
// Build a TableDef with NewTable or MustTable: a column constructor such as
// Integer or Text and a constraint constructor such as PrimaryKey, Unique,
// Check, Index, or ForeignKey each return a TableOption, and NewTable
// assembles them into a descriptor in any order. This is the recommended way
// to describe a table by hand, and it covers everything a struct literal
// does: a composite foreign key, a named unique constraint or check, and a
// unique index each have their own option-form constructor.
//
// TableDef is also the descriptor itself, and building one directly with a
// keyed composite literal remains fully supported: it is what inspect
// returns from a live database and what migrate's diff compares between two
// descriptors, so code that reads a descriptor back reads this struct
// either way. Every exported descriptor struct in this package accepts a
// keyed composite literal, and every literal of one in this repository is
// keyed. These structs gain fields as the descriptor model grows, and a new
// field is placed next to the fields it belongs with rather than appended at
// the end: TableDef.Schema and ForeignKeyDef.ReferencedSchema were each
// inserted ahead of existing fields. An unkeyed composite literal matches
// fields by position and must list every one of them, so it is not a
// supported way to build a descriptor.
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

// RelationshipDef describes a navigable relationship derived from a foreign key.
// The first relationship slice supports belongs-to relationships. The column
// lists are copied by Table.Relationships, so callers may inspect them safely.
type RelationshipDef struct {
	Name              string
	Kind              RelationshipKind
	Columns           []string
	ReferencedSchema  string
	ReferencedTable   string
	ReferencedColumns []string
}

// Clone returns a copy of r that does not share slices with r.
func (r RelationshipDef) Clone() RelationshipDef {
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
