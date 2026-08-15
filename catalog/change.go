package catalog

import "strings"

// ChangeKind names what happened to one subject within a changed table.
// The zero ChangeKind is not a kind and never appears in a Change.
type ChangeKind int

const (
	// ChangeAdded marks something the database has that the application
	// does not describe. It prints with a leading "+".
	ChangeAdded ChangeKind = iota + 1
	// ChangeRemoved marks something the application describes that the
	// database does not have. It prints with a leading "-".
	ChangeRemoved
	// ChangeModified marks something both sides have whose facts differ.
	// It prints with a leading "~" and names every fact that moved.
	ChangeModified
	// ChangeMoved marks something both sides have, identical in every
	// fact, at a different position in its list. It prints with a leading
	// "~" and names the two positions. It is a real difference, not
	// cosmetic: regenerating would write the descriptor's list in the new
	// order.
	ChangeMoved
)

// String returns the kind's name: "added", "removed", "modified", or
// "moved".
func (k ChangeKind) String() string {
	switch k {
	case ChangeAdded:
		return "added"
	case ChangeRemoved:
		return "removed"
	case ChangeModified:
		return "modified"
	case ChangeMoved:
		return "moved"
	default:
		return "unknown"
	}
}

// mark returns the leading punctuation Change.String prints for k: "+" for
// ChangeAdded, "-" for ChangeRemoved, and "~" for both ChangeModified and
// ChangeMoved.
func (k ChangeKind) mark() string {
	switch k {
	case ChangeAdded:
		return "+"
	case ChangeRemoved:
		return "-"
	default:
		return "~"
	}
}

// Change is one difference within a changed table: what happened, to what,
// and to which of its facts.
type Change struct {
	// Kind is what happened.
	Kind ChangeKind

	// Subject names what it happened to, in database terms: `column
	// "email"`, `unique constraint "users_email_key"`, `index
	// "users_phone_idx"`. A constraint that states no name is not given
	// one -- naming it after some of its fields cannot tell two
	// constraints apart -- so its subject is the whole constraint,
	// rendered with every fact it states: `check {Expression:"id > 0"}`.
	// Two subjects rendered that way are equal only when the constraints
	// are equal.
	//
	// Subject is empty for a fact belonging to the table itself rather
	// than to anything inside it, such as Strict or VirtualTableModule.
	Subject string

	// Fields names every fact that moved, empty for a plain addition or
	// removal. Each Path is the Go field path within the subject, which is
	// the name the descriptor in schema_gen.go uses and the name
	// schema.TableDef documents: "Nullable", "OnDelete",
	// "Type.DisplayWidth".
	Fields []FieldChange
}

// String renders the change as the report prints it, without the report's
// own indentation: a mark, the subject when there is one, and the fields
// joined with "; ".
func (c Change) String() string {
	var b strings.Builder
	b.WriteString(c.Kind.mark())
	if c.Subject != "" {
		b.WriteString(" ")
		b.WriteString(c.Subject)
	}
	if len(c.Fields) > 0 {
		if c.Subject != "" {
			b.WriteString(": ")
		} else {
			b.WriteString(" ")
		}
		for i, field := range c.Fields {
			if i > 0 {
				b.WriteString("; ")
			}
			b.WriteString(field.String())
		}
	}
	return b.String()
}

// FieldChange is one fact that moved: where it lives, what it was, and what
// it is now.
//
// Was and Now are rendered forms, not values. A string is quoted, a
// zero-valued field inside a struct is omitted from the rendering, and a
// nil and an empty list both render as an absent field, so two renderings
// are equal only when the two values are equal.
type FieldChange struct {
	// Path is the Go field path within the change's subject, dotted for
	// nesting and indexed for a position inside a list: "Nullable",
	// "Type.Unsigned", "Columns[1]". For the whole-descriptor fallback
	// described in Change's own doc it reads "descriptor".
	Path string

	// Was is the fact as the application describes it.
	Was string

	// Now is the fact as the database reports it.
	Now string
}

// String renders the field change as "Path: Was -> Now".
func (f FieldChange) String() string {
	return f.Path + ": " + f.Was + " -> " + f.Now
}
