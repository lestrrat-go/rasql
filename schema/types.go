// Package schema defines dialect-neutral database schema descriptors.
//
// Build a TableDef with NewTableDef or MustTableDef: a column constructor
// such as Integer or Text and a constraint constructor such as PrimaryKey,
// Unique, Check, Index, or ForeignKey each return a TableOption, and
// NewTableDef assembles them into a descriptor in any order. This is the
// recommended way to describe a table by hand, and it covers everything a
// struct literal does: a composite foreign key, a named unique constraint or
// check, and a unique index each have their own option-form constructor.
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

// MatchType names a foreign key's MATCH clause, which controls how a
// composite key with some NULL columns is checked. Its zero value, the
// empty string, means MATCH SIMPLE, the SQL default and the only form
// every dialect this package renders builds today, so every ForeignKeyDef
// and every checked-in generated file written before this field existed
// keeps meaning exactly what it always meant.
//
// A non-default MatchType is describable but not yet renderable: inspect
// records what a live PostgreSQL or SQLite foreign key actually declares,
// and TableDef.Validate accepts it, but render.CreateTable and the migrate
// diff-live path refuse to build DDL for one, because rasql does not yet
// know how to construct anything other than a plain MATCH SIMPLE foreign
// key.
type MatchType string

const (
	// MatchFull requires every column in a composite key to be NULL or
	// every column to be non-NULL.
	MatchFull MatchType = "FULL"
	// MatchPartial permits some columns in a composite key to be NULL as
	// long as a matching referenced row could still exist.
	MatchPartial MatchType = "PARTIAL"
)

func (m MatchType) valid() bool {
	switch m {
	case "", MatchFull, MatchPartial:
		return true
	default:
		return false
	}
}

// Deferrability names when PostgreSQL or SQLite checks a foreign key's
// constraint: immediately after each statement, or deferred until the
// enclosing transaction commits. Its zero value, the empty string, means
// NOT DEFERRABLE, the only form every dialect this package renders builds
// today, so every ForeignKeyDef and every checked-in generated file
// written before this field existed keeps meaning exactly what it always
// meant.
//
// A non-default Deferrability is describable but not yet renderable:
// inspect records what a live PostgreSQL or SQLite foreign key actually
// declares, and TableDef.Validate accepts it, but render.CreateTable and
// the migrate diff-live path refuse to build DDL for one, because rasql
// does not yet know how to construct anything other than a plain NOT
// DEFERRABLE foreign key.
type Deferrability string

const (
	// DeferrableInitiallyImmediate marks a foreign key DEFERRABLE INITIALLY
	// IMMEDIATE: checked at the end of each statement by default, but
	// deferrable to transaction commit with SET CONSTRAINTS.
	DeferrableInitiallyImmediate Deferrability = "DEFERRABLE INITIALLY IMMEDIATE"
	// DeferrableInitiallyDeferred marks a foreign key DEFERRABLE INITIALLY
	// DEFERRED: checked at transaction commit unless SET CONSTRAINTS moves
	// it earlier.
	DeferrableInitiallyDeferred Deferrability = "DEFERRABLE INITIALLY DEFERRED"
)

func (d Deferrability) valid() bool {
	switch d {
	case "", DeferrableInitiallyImmediate, DeferrableInitiallyDeferred:
		return true
	default:
		return false
	}
}

// ConflictResolution names a SQLite unique constraint's or primary key's ON
// CONFLICT resolution: how SQLite responds when the constraint is violated.
// Its zero value, the empty string, means SQLite's own default resolution,
// ABORT, which is what every UniqueDef, every TableDef, and every
// checked-in generated file written before this field existed has always
// meant. An explicit ON CONFLICT ABORT clause names the same zero value,
// since it behaves identically to no clause at all, the same way MatchType
// folds an explicit MATCH SIMPLE into its own zero value.
//
// A non-default ConflictResolution is describable but not yet renderable:
// inspect records what a live SQLite unique constraint's or primary key's
// own CREATE TABLE text declares, and TableDef.Validate accepts it, but
// render.CreateTable and the migrate diff-live path refuse to build DDL for
// one, because rasql does not yet know how to construct an ON CONFLICT
// clause.
type ConflictResolution string

const (
	// ConflictRollback rolls back the current transaction when the
	// constraint is violated.
	ConflictRollback ConflictResolution = "ROLLBACK"
	// ConflictFail preserves changes already made by the current
	// statement and returns an error.
	ConflictFail ConflictResolution = "FAIL"
	// ConflictIgnore skips the row that would violate the constraint,
	// keeping every earlier change the current statement made.
	ConflictIgnore ConflictResolution = "IGNORE"
	// ConflictReplace deletes the conflicting row before inserting or
	// updating.
	ConflictReplace ConflictResolution = "REPLACE"
)

func (c ConflictResolution) valid() bool {
	switch c {
	case "", ConflictRollback, ConflictFail, ConflictIgnore, ConflictReplace:
		return true
	default:
		return false
	}
}

// GeneratedStorage names whether a generated column's computed value is
// written into the table or produced each time the column is read. Its zero
// value, the empty string, means the column is not generated at all: it is
// meaningless unless ColumnDef.GeneratedExpression is also stated, and
// Table.Validate rejects the two stated apart from each other.
//
// A generated column is describable but not yet renderable: inspect records
// what a live SQLite, PostgreSQL, or MySQL generated column actually
// declares, and TableDef.Validate accepts it, but render.CreateTable and the migrate
// diff-live path refuse to build DDL for one, because rasql does not yet
// know how to construct a GENERATED ALWAYS AS clause, and because a
// generated column cannot be written to like an ordinary one, so silently
// rendering it as a plain writable column would be a worse failure than
// refusing outright.
type GeneratedStorage string

const (
	// GeneratedStored writes a generated column's computed value into the
	// table, so it occupies storage and is read back like any other column.
	GeneratedStored GeneratedStorage = "STORED"
	// GeneratedVirtual computes a generated column's value each time it is
	// read, rather than storing it.
	GeneratedVirtual GeneratedStorage = "VIRTUAL"
)

func (g GeneratedStorage) valid() bool {
	switch g {
	case "", GeneratedStored, GeneratedVirtual:
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
