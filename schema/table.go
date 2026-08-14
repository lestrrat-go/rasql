package schema

import (
	"encoding/json"
	"fmt"
	"go/token"
	"slices"
	"unicode"
	"unicode/utf8"
)

// DecimalScale is the number of digits a DecimalType column keeps to the right
// of the decimal point. Its zero value states no scale at all, which is a
// different thing from a stated scale of zero: DECIMAL(19,0) is a legitimate
// column and a descriptor that simply forgot to say so is not. Use
// NewDecimalScale to state one, including a scale of zero.
type DecimalScale struct {
	value int
	set   bool
}

// NewDecimalScale returns a DecimalScale that states value.
func NewDecimalScale(value int) DecimalScale {
	return DecimalScale{value: value, set: true}
}

// Value returns the stated scale and reports whether a scale was stated at all.
// The returned scale is meaningless when the second result is false.
func (s DecimalScale) Value() (int, bool) {
	return s.value, s.set
}

// MarshalJSON encodes a stated scale as a JSON number and an unstated one as
// null, so that a snapshot of a schema.TableDef keeps the plain-number form a
// tool such as rasqlgen reads back.
func (s DecimalScale) MarshalJSON() ([]byte, error) {
	if !s.set {
		return []byte("null"), nil
	}
	return json.Marshal(s.value)
}

// UnmarshalJSON decodes a JSON number as a stated scale and null as an
// unstated one. A snapshot written before a column had a scale therefore
// decodes as unstated and is refused by Table.Validate rather than read as 0.
func (s *DecimalScale) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*s = DecimalScale{}
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("schema: decode decimal scale: %w", err)
	}
	*s = NewDecimalScale(value)
	return nil
}

// TextWidth is the maximum number of characters a TextType column may store.
// Its zero value states no width at all, which is a different thing from a
// stated width of zero: VARCHAR(0), while unusual, is a legitimate column in
// the dialects that accept it, and a descriptor that simply never called
// Width is not the same as one that asked for zero characters. Use
// NewTextWidth, or the Width ColumnOption, to state one, including a width
// of 0.
type TextWidth struct {
	value int
	set   bool
}

// NewTextWidth returns a TextWidth that states value.
func NewTextWidth(value int) TextWidth {
	return TextWidth{value: value, set: true}
}

// Value returns the stated width and reports whether a width was stated at
// all. The returned width is meaningless when the second result is false.
func (w TextWidth) Value() (int, bool) {
	return w.value, w.set
}

// MarshalJSON encodes a stated width as a JSON number and an unstated one as
// null, so that a snapshot of a schema.TableDef keeps the plain-number form a
// tool such as rasqlgen reads back.
func (w TextWidth) MarshalJSON() ([]byte, error) {
	if !w.set {
		return []byte("null"), nil
	}
	return json.Marshal(w.value)
}

// UnmarshalJSON decodes a JSON number as a stated width and null as an
// unstated one. A snapshot written before a column had a width therefore
// decodes as unstated rather than as a width of 0.
func (w *TextWidth) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*w = TextWidth{}
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("schema: decode text width: %w", err)
	}
	*w = NewTextWidth(value)
	return nil
}

// IntegerDisplayWidth is the display width MySQL's integer types carry, such
// as the 11 in int(11): a minimum number of digits MySQL pads with spaces or,
// under ZEROFILL, zeros in query output, never a constraint on the values the
// column stores. Its zero value states no width at all, which is a different
// thing from a stated width of zero, and is what every IntegerType and every
// checked-in generated file written before this field existed has always
// meant. Only MySQL has a display width; PostgreSQL and SQLite integer
// columns never carry one. Use NewIntegerDisplayWidth to state one.
type IntegerDisplayWidth struct {
	value int
	set   bool
}

// NewIntegerDisplayWidth returns an IntegerDisplayWidth that states value.
func NewIntegerDisplayWidth(value int) IntegerDisplayWidth {
	return IntegerDisplayWidth{value: value, set: true}
}

// Value returns the stated width and reports whether a width was stated at
// all. The returned width is meaningless when the second result is false.
func (w IntegerDisplayWidth) Value() (int, bool) {
	return w.value, w.set
}

// MarshalJSON encodes a stated width as a JSON number and an unstated one as
// null, the same convention TextWidth and DecimalScale use.
func (w IntegerDisplayWidth) MarshalJSON() ([]byte, error) {
	if !w.set {
		return []byte("null"), nil
	}
	return json.Marshal(w.value)
}

// UnmarshalJSON decodes a JSON number as a stated width and null as an
// unstated one. A snapshot written before a column had a display width
// therefore decodes as unstated rather than as a width of 0.
func (w *IntegerDisplayWidth) UnmarshalJSON(data []byte) error {
	if string(data) == "null" {
		*w = IntegerDisplayWidth{}
		return nil
	}
	var value int
	if err := json.Unmarshal(data, &value); err != nil {
		return fmt.Errorf("schema: decode integer display width: %w", err)
	}
	*w = NewIntegerDisplayWidth(value)
	return nil
}

// ColumnDef describes a table column.
type ColumnDef struct {
	Name     string
	Type     ColumnType
	Nullable bool
	Default  string

	// GeneratedExpression is the expression a generated column computes,
	// exactly as the server reports it, or empty for an ordinary column.
	// Its zero value, the empty string, means the column is not generated,
	// which is what every ColumnDef and every checked-in generated file
	// written before this field existed has always meant.
	//
	// A non-empty GeneratedExpression always carries a non-empty
	// GeneratedStorage; Table.Validate rejects one stated without the
	// other. See GeneratedStorage's own doc for what recording this fact
	// currently means for rendering.
	GeneratedExpression string `json:",omitempty"`

	// GeneratedStorage names whether GeneratedExpression is stored or
	// computed on read. See GeneratedStorage's own doc.
	GeneratedStorage GeneratedStorage `json:",omitempty"`
}

// MarshalJSON encodes a column type as a tagged object so type-specific
// options cannot appear as fields on unrelated column types.
func (c ColumnDef) MarshalJSON() ([]byte, error) {
	type wireColumn struct {
		Name                string           `json:"Name"`
		Type                json.RawMessage  `json:"Type"`
		Nullable            bool             `json:"Nullable"`
		Default             string           `json:"Default"`
		GeneratedExpression string           `json:"GeneratedExpression,omitempty"`
		GeneratedStorage    GeneratedStorage `json:"GeneratedStorage,omitempty"`
	}
	typeData, err := marshalColumnType(c.Type)
	if err != nil {
		return nil, err
	}
	return json.Marshal(wireColumn{
		Name:                c.Name,
		Type:                typeData,
		Nullable:            c.Nullable,
		Default:             c.Default,
		GeneratedExpression: c.GeneratedExpression,
		GeneratedStorage:    c.GeneratedStorage,
	})
}

// UnmarshalJSON decodes the tagged column type representation.
func (c *ColumnDef) UnmarshalJSON(data []byte) error {
	type wireColumn struct {
		Name                string           `json:"Name"`
		Type                json.RawMessage  `json:"Type"`
		Nullable            bool             `json:"Nullable"`
		Default             string           `json:"Default"`
		GeneratedExpression string           `json:"GeneratedExpression,omitempty"`
		GeneratedStorage    GeneratedStorage `json:"GeneratedStorage,omitempty"`
	}
	var wire wireColumn
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	columnType, err := unmarshalColumnType(wire.Type)
	if err != nil {
		return err
	}
	*c = ColumnDef{
		Name:                wire.Name,
		Type:                columnType,
		Nullable:            wire.Nullable,
		Default:             wire.Default,
		GeneratedExpression: wire.GeneratedExpression,
		GeneratedStorage:    wire.GeneratedStorage,
	}
	return nil
}

// UniqueDef requires the listed columns to be unique together.
type UniqueDef struct {
	Name    string
	Columns []string

	// Deferrable names when the unique constraint is checked. See
	// Deferrability's own doc for what its zero value means and what
	// currently accepts it. omitempty keeps a NOT DEFERRABLE UniqueDef's
	// JSON identical to what it encoded before this field existed.
	Deferrable Deferrability `json:",omitempty"`

	// NullsNotDistinct marks a PostgreSQL 15+ UNIQUE NULLS NOT DISTINCT
	// constraint, under which two NULLs in the constrained columns
	// conflict with each other instead of coexisting. Its zero value,
	// false, means NULLS DISTINCT, the SQL default and the only behavior
	// every UniqueDef written before this field existed has always meant.
	//
	// A true NullsNotDistinct is describable but not yet renderable:
	// inspect records what a live PostgreSQL unique constraint actually
	// declares, and TableDef.Validate accepts it, but render.CreateTable
	// and the migrate diff-live path refuse to build DDL for one, because
	// rasql does not yet know how to construct a NULLS NOT DISTINCT
	// clause.
	NullsNotDistinct bool `json:",omitempty"`

	// IncludeColumns lists a PostgreSQL unique constraint's INCLUDE
	// columns: columns carried by the constraint's backing index for
	// covering reads without taking part in the uniqueness check itself.
	// Its zero value, nil, means the constraint has no INCLUDE clause,
	// which is what every UniqueDef written before this field existed has
	// always meant.
	//
	// A non-empty IncludeColumns is describable but not yet renderable:
	// inspect records a live PostgreSQL unique constraint's included
	// columns, and TableDef.Validate accepts them, but render.CreateTable
	// and the migrate diff-live path refuse to build DDL for one, because
	// rasql does not yet know how to construct an INCLUDE clause.
	IncludeColumns []string `json:",omitempty"`

	// OnConflict names a SQLite unique constraint's ON CONFLICT
	// resolution. See ConflictResolution's own doc for what its zero
	// value means and what currently accepts it. omitempty keeps a
	// clause-free UniqueDef's JSON identical to what it encoded before
	// this field existed.
	OnConflict ConflictResolution `json:",omitempty"`
}

// CheckDef requires Expression to evaluate to true for each row.
// Expression is a schema-level SQL expression and is rendered only by a DDL-capable dialect.
type CheckDef struct {
	Name       string
	Expression string

	// NoInherit records a PostgreSQL check constraint declared NO
	// INHERIT: a child table in an inheritance hierarchy neither
	// inherits nor enforces it. Its zero value, false, means the
	// constraint is inherited normally, which is what every CheckDef and
	// every checked-in generated file written before this field existed
	// has always meant. MySQL and SQLite have no NO INHERIT concept, so
	// this never comes from a MySQL or SQLite descriptor.
	//
	// NoInherit is describable but not yet renderable: inspect records
	// what a live PostgreSQL check constraint actually declares, and
	// TableDef.Validate accepts it, but render.CreateTable and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct a NO INHERIT check constraint.
	NoInherit bool `json:",omitempty"`

	// NotValid records a check constraint declared NOT VALID: existing
	// rows were never checked against it. Its zero value, false, means
	// the constraint was validated against existing rows, which is what
	// every CheckDef and every checked-in generated file written before
	// this field existed has always meant. MySQL and SQLite have no NOT
	// VALID concept, so this never comes from a MySQL or SQLite
	// descriptor.
	//
	// NotValid is describable but not yet renderable, on the same terms
	// as NoInherit.
	NotValid bool `json:",omitempty"`

	// NotEnforced records a check constraint declared NOT ENFORCED,
	// which PostgreSQL 18+ and MySQL both support. Its zero value,
	// false, means the constraint is enforced, which is what every
	// CheckDef and every checked-in generated file written before this
	// field existed has always meant. SQLite has no NOT ENFORCED
	// concept, so this never comes from a SQLite descriptor.
	//
	// NotEnforced is describable but not yet renderable, on the same
	// terms as NoInherit.
	NotEnforced bool `json:",omitempty"`
}

// IndexMethod names the index access method (what PostgreSQL calls an
// access method and MySQL calls an index type) an IndexDef uses, such as
// PostgreSQL's "gin" or MySQL's "FULLTEXT". Its zero value, the empty
// string, means the engine's own default method — the plain B-tree every
// dialect this package renders builds today — so every IndexDef and every
// checked-in generated file written before this field existed keeps meaning
// exactly what it always meant.
//
// A non-default IndexMethod is describable but not yet renderable: inspect
// records what a live PostgreSQL or MySQL index actually uses, and
// TableDef.Validate accepts it, but render.CreateIndexes and the migrate
// diff-live path refuse to build DDL for one, because rasql does not yet
// know how to construct anything other than a plain default index.
type IndexMethod string

// IndexDef describes an index owned by a table.
type IndexDef struct {
	// Name is the index's own identifier.
	Name string

	// Columns lists the plain column names that key a column-only index, in
	// key order. This is the only meaning Columns has ever had: an index
	// whose keys are not all plain columns describes its full key order in
	// Expressions instead and leaves Columns empty, rather than
	// repurposing Columns to hold a subset of those keys. A checked-in
	// descriptor written before expression indexes existed always leaves
	// Expressions empty, so Columns keeps meaning exactly what it always
	// meant.
	Columns []string
	Unique  bool

	// Method names a non-default index access method. See IndexMethod's own
	// doc for what its zero value means and what currently accepts it.
	// omitempty keeps a default-method IndexDef's JSON identical to what it
	// encoded before this field existed.
	Method IndexMethod `json:",omitempty"`

	// Expressions, when non-empty, is the full ordered list of key
	// expressions for an index that has at least one key that is not a
	// plain column reference: an expression index, or one that mixes plain
	// columns with expressions. A plain-column key inside it is recorded as
	// its bare column name, which is itself a valid SQL expression, so an
	// index's full key order always lives in one place instead of being
	// reconstructed by interleaving Columns with a second, position-keyed
	// list. Columns is left empty whenever Expressions is set, preserving
	// what Columns has always meant rather than letting it hold an
	// ambiguous subset of a mixed index's keys. Its zero value, nil,
	// means every key is a plain column, described by Columns as before.
	//
	// An IndexDef naming Expressions is describable but not yet
	// renderable: inspect records the expression text a live PostgreSQL,
	// MySQL, or SQLite expression index actually uses, and
	// TableDef.Validate accepts it, but render.CreateIndexes and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct an expression key.
	Expressions []string `json:",omitempty"`

	// Predicate is the WHERE-clause expression text of a partial index,
	// exactly as the server reports it, or empty for a non-partial index.
	// Its zero value, the empty string, means the index is not partial,
	// which is what every IndexDef and every checked-in generated file
	// written before this field existed has always meant.
	//
	// A non-empty Predicate is describable but not yet renderable: inspect
	// records a live PostgreSQL or SQLite partial index's predicate, and
	// TableDef.Validate accepts it, but render.CreateIndexes and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct a WHERE clause on an index.
	// MySQL has no partial-index concept, so this never comes from a MySQL
	// descriptor.
	Predicate string `json:",omitempty"`
}

// ExclusionElementDef describes one element of an ExclusionDef: a column or
// expression paired with the operator checked against the same element on
// every other row the constraint compares it to.
type ExclusionElementDef struct {
	// Expression is the element's key text, exactly as the server reports
	// it: a bare column name for a plain column element, or the element's
	// expression text otherwise. This is the same convention
	// IndexDef.Expressions uses for a mixed or expression index's keys.
	Expression string

	// Operator is the name of the operator this element is checked with,
	// exactly as the server reports it, such as "=" or "&&".
	Operator string
}

// ExclusionDef describes a PostgreSQL EXCLUDE constraint: a table-level
// constraint that forbids any two rows from having elements that all
// satisfy their paired operator against each other, generalizing UniqueDef
// beyond plain equality. Exclusion constraints are PostgreSQL-only; MySQL
// and SQLite have no equivalent concept, so this never comes from a MySQL
// or SQLite descriptor.
//
// An ExclusionDef is describable but not yet renderable: inspect records
// what a live PostgreSQL exclusion constraint actually declares instead of
// failing the whole table, as it used to, and TableDef.Validate accepts it,
// but render.CreateTable and the migrate diff-live path refuse to build DDL
// for one, because rasql does not yet know how to construct an EXCLUDE
// clause. There is no option-form constructor yet either: today an
// ExclusionDef only appears on a descriptor inspect produced.
type ExclusionDef struct {
	Name string

	// Method names the constraint's backing index access method, such as
	// "gist". Its zero value, the empty string, means the engine's own
	// default access method (btree), the same convention IndexMethod uses
	// on IndexDef.Method.
	Method IndexMethod `json:",omitempty"`

	// Elements lists the constraint's elements in declaration order. A
	// valid ExclusionDef always has at least one.
	Elements []ExclusionElementDef

	// Predicate is the WHERE-clause expression text of a partial
	// exclusion constraint, exactly as the server reports it, or empty
	// for a non-partial one. See IndexDef.Predicate for the same
	// convention on a partial index.
	Predicate string `json:",omitempty"`

	// Deferrable names when the constraint is checked. See Deferrability's
	// own doc for what its zero value means and what currently accepts it.
	// omitempty keeps a NOT DEFERRABLE ExclusionDef's JSON identical to a
	// clause-free one.
	Deferrable Deferrability `json:",omitempty"`
}

// ForeignKeyDef describes a foreign-key constraint.
type ForeignKeyDef struct {
	Name    string
	Columns []string

	// ReferencedSchema names the schema holding ReferencedTable. An empty
	// ReferencedSchema leaves the reference unqualified, which each server
	// then resolves by its own rule: SQLite in the referencing table's own
	// database, PostgreSQL through search_path. A dialect without
	// dialect.CapabilityQualifiedReference rejects a ReferencedSchema that
	// names any schema other than the referencing table's own.
	ReferencedSchema  string
	ReferencedTable   string
	ReferencedColumns []string

	// Match names the foreign key's MATCH clause. See MatchType's own doc
	// for what its zero value means and what currently accepts it.
	// omitempty keeps a MATCH SIMPLE ForeignKeyDef's JSON identical to what
	// it encoded before this field existed.
	Match MatchType `json:",omitempty"`

	OnDelete ReferenceAction
	OnUpdate ReferenceAction

	// Deferrable names when the foreign key's constraint is checked. See
	// Deferrability's own doc for what its zero value means and what
	// currently accepts it. omitempty keeps a NOT DEFERRABLE ForeignKeyDef's
	// JSON identical to what it encoded before this field existed.
	Deferrable Deferrability `json:",omitempty"`

	// NotValid records a foreign key declared NOT VALID: existing rows
	// were never checked against it. Its zero value, false, means the
	// foreign key was validated against existing rows, which is what
	// every ForeignKeyDef and every checked-in generated file written
	// before this field existed has always meant. MySQL has no NOT VALID
	// concept for foreign keys, so this never comes from a MySQL
	// descriptor.
	//
	// NotValid is describable but not yet renderable: inspect records
	// what a live PostgreSQL foreign key actually declares, and
	// TableDef.Validate accepts it, but render.CreateTable and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct a NOT VALID foreign key.
	NotValid bool `json:",omitempty"`

	// NotEnforced records a foreign key declared NOT ENFORCED, which
	// PostgreSQL 18+ supports. Its zero value, false, means the foreign
	// key is enforced, which is what every ForeignKeyDef and every
	// checked-in generated file written before this field existed has
	// always meant. MySQL has no NOT ENFORCED concept for foreign keys,
	// so this never comes from a MySQL descriptor.
	//
	// NotEnforced is describable but not yet renderable, on the same
	// terms as NotValid.
	NotEnforced bool `json:",omitempty"`
}

// TableDef describes a database table and its constraints.
type TableDef struct {
	// Schema names the namespace holding the table: a PostgreSQL schema, a
	// MySQL database, or a SQLite attached-database name. A renderer quotes it
	// as an identifier separate from Name and never interprets it further, so
	// rasql takes no position on what a namespace means to a server and never
	// creates one. An empty Schema leaves the table unqualified, which
	// resolves through the connection's own default and is what every
	// descriptor written before this field existed does.
	//
	// Qualification reaches DML, column references and DDL. A SELECT,
	// INSERT, UPDATE or DELETE built from this descriptor renders
	// "audit"."events" as its target, a column reached through the unaliased
	// table renders "audit"."events"."id", and render.CreateTable,
	// render.CreateIndexes and rasql.CreateTable render the table and its indexes
	// into the named namespace on every dialect that can express it. rasql
	// never creates, drops or connects to the namespace itself: an
	// application that needs "audit" to exist creates it with a reviewed
	// native migration, the same way every other piece of DDL this library
	// does not synthesize gets created. SQLite inspection preserves the
	// selected database name in Schema, so a qualified table returned by
	// inspection remains qualified when it is rendered. SQLite inspection
	// requires a retained connection when the descriptor addresses temp or
	// attached data.
	Schema string
	Name   string

	// RowName overrides the Go row type rasqlgen generates for the table.
	// The default, used when RowName is empty, is <Table>Row: a table named
	// users generates UsersRow. RowName names a Go type, not a SQL
	// identifier, so a non-empty value must be a valid, exported Go
	// identifier (an ASCII letter, digit or underscore, first rune an
	// uppercase letter, not a Go keyword) rather than legal under
	// ValidateIdentifier's SQL rule. RowName is a code-generation hint
	// only — no renderer, dialect, inspect, or migrate path reads it, and it
	// never appears in rendered SQL. inspect never sets it: a live database
	// has no opinion about Go names.
	RowName string

	Columns    []ColumnDef
	PrimaryKey []string

	// Strict marks a SQLite STRICT table: every column must declare one of
	// SQLite's strict type names, and a value that does not match a
	// column's type is rejected instead of stored under SQLite's usual
	// type-affinity rules. Its zero value, false, means the table is not
	// STRICT, which is what every TableDef and every checked-in generated
	// file written before this field existed has always meant. PostgreSQL
	// and MySQL have no STRICT concept, so this never comes from a
	// PostgreSQL or MySQL descriptor.
	//
	// A true Strict is describable but not yet renderable: inspect records
	// what a live SQLite table's own CREATE TABLE text declares, and
	// TableDef.Validate accepts it, but render.CreateTable and the migrate
	// diff-live path refuse to build DDL for one, because rasql does not
	// yet know how to construct a STRICT table.
	Strict bool `json:",omitempty"`

	// WithoutRowID marks a SQLite WITHOUT ROWID table, which stores rows
	// keyed directly by their primary key instead of by an implicit
	// rowid. Its zero value, false, means the table has the usual SQLite
	// rowid, which is what every TableDef and every checked-in generated
	// file written before this field existed has always meant. PostgreSQL
	// and MySQL have no WITHOUT ROWID concept, so this never comes from a
	// PostgreSQL or MySQL descriptor.
	//
	// WithoutRowID is describable but not yet renderable, on the same
	// terms as Strict.
	WithoutRowID bool `json:",omitempty"`

	// PrimaryKeyAutoincrement marks a SQLite primary key declared with the
	// AUTOINCREMENT keyword, which changes SQLite's rowid-allocation
	// algorithm to never reuse a rowid a deleted row once used. It belongs
	// to TableDef rather than to a column or to PrimaryKey's own string
	// list because AUTOINCREMENT is a property of the table's single
	// INTEGER PRIMARY KEY declaration, not of any one column definition.
	// Its zero value, false, means the primary key carries no
	// AUTOINCREMENT keyword, which is what every TableDef and every
	// checked-in generated file written before this field existed has
	// always meant. It is meaningless, and TableDef.Validate rejects it,
	// on a TableDef with an empty PrimaryKey. PostgreSQL and MySQL have no
	// equivalent SQLite AUTOINCREMENT concept (MySQL's own AUTO_INCREMENT
	// is a column-level option, unrelated to this field), so this never
	// comes from a PostgreSQL or MySQL descriptor.
	//
	// PrimaryKeyAutoincrement is describable but not yet renderable, on
	// the same terms as Strict.
	PrimaryKeyAutoincrement bool `json:",omitempty"`

	// PrimaryKeyOnConflict names a SQLite primary key's ON CONFLICT
	// resolution. See ConflictResolution's own doc for what its zero value
	// means and what currently accepts it. It is meaningless, and
	// TableDef.Validate rejects a non-default value, on a TableDef with an
	// empty PrimaryKey. omitempty keeps a clause-free TableDef's JSON
	// identical to what it encoded before this field existed.
	PrimaryKeyOnConflict ConflictResolution `json:",omitempty"`

	UniqueConstraints []UniqueDef
	Checks            []CheckDef

	// ExclusionConstraints lists the table's PostgreSQL EXCLUDE
	// constraints. Its zero value, nil, means the table has none, which is
	// what every TableDef and every checked-in generated file written
	// before this field existed has always meant. See ExclusionDef's own
	// doc for what recording one currently means for rendering.
	ExclusionConstraints []ExclusionDef `json:",omitempty"`

	Indexes       []IndexDef
	ForeignKeys   []ForeignKeyDef
	Relationships []RelationshipDef
}

// Qualified reports whether t names a schema.
func (t TableDef) Qualified() bool {
	return t.Schema != ""
}

// QualifiedName returns the table's name for display: "schema.name" when t
// names a schema and "name" otherwise. It is for error messages, log output
// and map keys only. It is never a SQL identifier: a renderer quotes Schema
// and Name as two identifiers, and dialect.QuoteIdentifier rejects the
// dotted string this returns.
func (t TableDef) QualifiedName() string {
	if t.Schema == "" {
		return t.Name
	}
	return t.Schema + "." + t.Name
}

// Clone returns a copy of t that does not share slices with t.
func (t TableDef) Clone() TableDef {
	clone := t
	clone.Columns = append([]ColumnDef(nil), t.Columns...)
	clone.PrimaryKey = append([]string(nil), t.PrimaryKey...)
	clone.UniqueConstraints = make([]UniqueDef, len(t.UniqueConstraints))
	for i, constraint := range t.UniqueConstraints {
		clone.UniqueConstraints[i] = constraint
		clone.UniqueConstraints[i].Columns = append([]string(nil), constraint.Columns...)
	}
	clone.Checks = append([]CheckDef(nil), t.Checks...)
	clone.ExclusionConstraints = make([]ExclusionDef, len(t.ExclusionConstraints))
	for i, exclusion := range t.ExclusionConstraints {
		clone.ExclusionConstraints[i] = exclusion
		clone.ExclusionConstraints[i].Elements = append([]ExclusionElementDef(nil), exclusion.Elements...)
	}
	clone.Indexes = make([]IndexDef, len(t.Indexes))
	for i, index := range t.Indexes {
		clone.Indexes[i] = index
		clone.Indexes[i].Columns = append([]string(nil), index.Columns...)
		clone.Indexes[i].Expressions = append([]string(nil), index.Expressions...)
	}
	clone.ForeignKeys = make([]ForeignKeyDef, len(t.ForeignKeys))
	for i, key := range t.ForeignKeys {
		clone.ForeignKeys[i] = key
		clone.ForeignKeys[i].Columns = append([]string(nil), key.Columns...)
		clone.ForeignKeys[i].ReferencedColumns = append([]string(nil), key.ReferencedColumns...)
	}
	clone.Relationships = make([]RelationshipDef, len(t.Relationships))
	for i, relationship := range t.Relationships {
		clone.Relationships[i] = relationship.Clone()
	}
	return clone
}

// Column returns the column named name.
func (t TableDef) Column(name string) (ColumnDef, bool) {
	for _, column := range t.Columns {
		if column.Name == name {
			return column, true
		}
	}
	return ColumnDef{}, false
}

// validateExportedGoIdentifier reports whether name is a valid, exported Go
// identifier. RowName names a Go type the generator emits, not a SQL
// identifier, so it is checked against Go's identifier rule (via
// go/token.IsIdentifier, which also rejects a Go keyword) plus Go's
// export rule, rather than against ValidateIdentifier's looser SQL rule.
func validateExportedGoIdentifier(name string) error {
	if !token.IsIdentifier(name) {
		return fmt.Errorf("must be a valid Go identifier")
	}
	first, _ := utf8.DecodeRuneInString(name)
	if !unicode.IsUpper(first) {
		return fmt.Errorf("must be an exported Go identifier")
	}
	return nil
}

// Validate reports whether t has a valid, internally consistent descriptor.
func (t TableDef) Validate() error {
	if t.Schema != "" {
		if err := ValidateIdentifier(t.Schema); err != nil {
			return validationError("table.schema", "%s", err)
		}
	}
	if err := ValidateIdentifier(t.Name); err != nil {
		return validationError("table", "%s", err)
	}
	if t.RowName != "" {
		if err := validateExportedGoIdentifier(t.RowName); err != nil {
			return validationError("table.row_name", "%s", err)
		}
	}
	if len(t.Columns) == 0 {
		return validationError("table", "must have at least one column")
	}

	columns := make(map[string]struct{}, len(t.Columns))
	for i, column := range t.Columns {
		path := fmt.Sprintf("columns[%d]", i)
		if err := ValidateIdentifier(column.Name); err != nil {
			return validationError(path+".name", "%s", err)
		}
		if !validColumnType(column.Type) {
			return validationError(path+".type", "unsupported column type %T", column.Type)
		}
		switch typed := column.Type.(type) {
		case DecimalType:
			if typed.Precision < 1 {
				return validationError(path+".type.precision", "decimal column must state a precision of at least 1")
			}
			scale, stated := typed.Scale.Value()
			if !stated {
				return validationError(path+".type.scale", "decimal column must state a scale: use schema.NewDecimalScale, which can state a scale of 0")
			}
			if scale < 0 {
				return validationError(path+".type.scale", "decimal scale must not be negative")
			}
			if scale > typed.Precision {
				return validationError(path+".type.scale", "decimal scale %d exceeds precision %d", scale, typed.Precision)
			}
		case TextType:
			width, stated := typed.Width.Value()
			if stated && width < 0 {
				return validationError(path+".type.width", "text width must not be negative")
			}
			if typed.Fixed && !stated {
				return validationError(path+".type.width", "fixed-width text column must state a width: use schema.Width, since bare CHAR means CHAR(1), not an unbounded column")
			}
		case IntegerType:
			if width, stated := typed.DisplayWidth.Value(); stated && width < 0 {
				return validationError(path+".type.display_width", "integer display width must not be negative")
			}
		}
		if (column.GeneratedExpression == "") != (column.GeneratedStorage == "") {
			return validationError(path+".generated_storage", "GeneratedExpression and GeneratedStorage must be stated together")
		}
		if !column.GeneratedStorage.valid() {
			return validationError(path+".generated_storage", "unsupported generated column storage %q", column.GeneratedStorage)
		}
		if column.GeneratedExpression != "" && column.Default != "" {
			return validationError(path+".default", "a generated column must not also state a Default")
		}
		if _, exists := columns[column.Name]; exists {
			return validationError(path+".name", "duplicates column %q", column.Name)
		}
		columns[column.Name] = struct{}{}
	}

	if err := validateColumnList("primary_key", t.PrimaryKey, columns, false); err != nil {
		return err
	}
	if !t.PrimaryKeyOnConflict.valid() {
		return validationError("table.primary_key_on_conflict", "unsupported primary key conflict resolution %q", t.PrimaryKeyOnConflict)
	}
	if len(t.PrimaryKey) == 0 {
		if t.PrimaryKeyAutoincrement {
			return validationError("table.primary_key_autoincrement", "must not be set without a primary key")
		}
		if t.PrimaryKeyOnConflict != "" {
			return validationError("table.primary_key_on_conflict", "must not be set without a primary key")
		}
	}
	constraintNames := make(map[string]string)
	if err := validateNamedColumnLists("unique_constraints", t.UniqueConstraints, columns, constraintNames); err != nil {
		return err
	}
	if err := validateChecks(t.Checks, constraintNames); err != nil {
		return err
	}
	if err := validateExclusionConstraints(t.ExclusionConstraints, constraintNames); err != nil {
		return err
	}
	if err := validateIndexes(t.Indexes, columns); err != nil {
		return err
	}
	if err := validateForeignKeys(t.ForeignKeys, columns, constraintNames); err != nil {
		return err
	}
	return validateRelationships(t.Relationships, t.ForeignKeys, columns)
}

func validateRelationships(relationships []RelationshipDef, foreignKeys []ForeignKeyDef, columns map[string]struct{}) error {
	for i, relationship := range relationships {
		path := fmt.Sprintf("relationships[%d]", i)
		if relationship.Name == "" {
			return validationError(path+".name", "must not be empty")
		}
		if err := ValidateIdentifier(relationship.Name); err != nil {
			return validationError(path+".name", "%s", err)
		}
		if relationship.Kind != RelationshipBelongsTo {
			return validationError(path+".kind", "unsupported relationship kind %q", relationship.Kind)
		}
		if err := validateColumnList(path+".columns", relationship.Columns, columns, true); err != nil {
			return err
		}
		if relationship.ReferencedSchema != "" {
			if err := ValidateIdentifier(relationship.ReferencedSchema); err != nil {
				return validationError(path+".referenced_schema", "%s", err)
			}
		}
		if err := ValidateIdentifier(relationship.ReferencedTable); err != nil {
			return validationError(path+".referenced_table", "%s", err)
		}
		if err := validateIdentifierList(path+".referenced_columns", relationship.ReferencedColumns, true); err != nil {
			return err
		}
		if len(relationship.Columns) != len(relationship.ReferencedColumns) {
			return validationError(path, "has %d local columns and %d referenced columns", len(relationship.Columns), len(relationship.ReferencedColumns))
		}
		matched := false
		for _, foreignKey := range foreignKeys {
			if foreignKey.ReferencedSchema == relationship.ReferencedSchema &&
				foreignKey.ReferencedTable == relationship.ReferencedTable &&
				slices.Equal(foreignKey.Columns, relationship.Columns) &&
				slices.Equal(foreignKey.ReferencedColumns, relationship.ReferencedColumns) {
				matched = true
				break
			}
		}
		if !matched {
			return validationError(path, "does not match a declared foreign key")
		}
	}
	return nil
}

// validateNamedColumnLists validates constraints and records each non-empty
// name in constraintNames, keyed by the name and mapping to the descriptor
// path that first used it. This lets callers detect a name reused across
// different kinds of constraints (unique, check, foreign key), since the
// renderer emits all of them into a single CREATE TABLE constraint namespace.
func validateNamedColumnLists(path string, constraints []UniqueDef, columns map[string]struct{}, constraintNames map[string]string) error {
	for i, constraint := range constraints {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if constraint.Name != "" {
			if err := ValidateIdentifier(constraint.Name); err != nil {
				return validationError(itemPath+".name", "%s", err)
			}
			if owner, exists := constraintNames[constraint.Name]; exists {
				return validationError(itemPath+".name", "duplicates constraint %q declared at %s", constraint.Name, owner)
			}
			constraintNames[constraint.Name] = itemPath
		}
		if err := validateColumnList(itemPath+".columns", constraint.Columns, columns, true); err != nil {
			return err
		}
		if !constraint.Deferrable.valid() {
			return validationError(itemPath+".deferrable", "unsupported unique constraint deferrability %q", constraint.Deferrable)
		}
		if !constraint.OnConflict.valid() {
			return validationError(itemPath+".on_conflict", "unsupported unique constraint conflict resolution %q", constraint.OnConflict)
		}
		if len(constraint.IncludeColumns) > 0 {
			seen := make(map[string]struct{}, len(constraint.Columns)+len(constraint.IncludeColumns))
			for _, name := range constraint.Columns {
				seen[name] = struct{}{}
			}
			for j, name := range constraint.IncludeColumns {
				includedPath := fmt.Sprintf("%s.include_columns[%d]", itemPath, j)
				if _, exists := columns[name]; !exists {
					return validationError(includedPath, "references unknown column %q", name)
				}
				if _, exists := seen[name]; exists {
					return validationError(includedPath, "duplicates column %q", name)
				}
				seen[name] = struct{}{}
			}
		}
	}
	return nil
}

func validateChecks(checks []CheckDef, constraintNames map[string]string) error {
	for i, check := range checks {
		path := fmt.Sprintf("checks[%d]", i)
		if check.Name != "" {
			if err := ValidateIdentifier(check.Name); err != nil {
				return validationError(path+".name", "%s", err)
			}
			if owner, exists := constraintNames[check.Name]; exists {
				return validationError(path+".name", "duplicates constraint %q declared at %s", check.Name, owner)
			}
			constraintNames[check.Name] = path
		}
		if check.Expression == "" {
			return validationError(path+".expression", "must not be empty")
		}
	}
	return nil
}

func validateExclusionConstraints(exclusions []ExclusionDef, constraintNames map[string]string) error {
	for i, exclusion := range exclusions {
		path := fmt.Sprintf("exclusion_constraints[%d]", i)
		if exclusion.Name != "" {
			if err := ValidateIdentifier(exclusion.Name); err != nil {
				return validationError(path+".name", "%s", err)
			}
			if owner, exists := constraintNames[exclusion.Name]; exists {
				return validationError(path+".name", "duplicates constraint %q declared at %s", exclusion.Name, owner)
			}
			constraintNames[exclusion.Name] = path
		}
		if len(exclusion.Elements) == 0 {
			return validationError(path+".elements", "must not be empty")
		}
		for j, element := range exclusion.Elements {
			elementPath := fmt.Sprintf("%s.elements[%d]", path, j)
			if element.Expression == "" {
				return validationError(elementPath+".expression", "must not be empty")
			}
			if element.Operator == "" {
				return validationError(elementPath+".operator", "must not be empty")
			}
		}
		if !exclusion.Deferrable.valid() {
			return validationError(path+".deferrable", "unsupported exclusion constraint deferrability %q", exclusion.Deferrable)
		}
	}
	return nil
}

func validateIndexes(indexes []IndexDef, columns map[string]struct{}) error {
	names := make(map[string]struct{}, len(indexes))
	for i, index := range indexes {
		path := fmt.Sprintf("indexes[%d]", i)
		if err := ValidateIdentifier(index.Name); err != nil {
			return validationError(path+".name", "%s", err)
		}
		if _, exists := names[index.Name]; exists {
			return validationError(path+".name", "duplicates index %q", index.Name)
		}
		names[index.Name] = struct{}{}
		if len(index.Expressions) == 0 {
			if err := validateColumnList(path+".columns", index.Columns, columns, true); err != nil {
				return err
			}
			continue
		}
		if len(index.Columns) > 0 {
			return validationError(path+".columns", "must be empty when Expressions describes the index's keys instead")
		}
		for j, expression := range index.Expressions {
			if expression == "" {
				return validationError(fmt.Sprintf("%s.expressions[%d]", path, j), "must not be empty")
			}
		}
	}
	return nil
}

func validateForeignKeys(keys []ForeignKeyDef, columns map[string]struct{}, constraintNames map[string]string) error {
	for i, key := range keys {
		path := fmt.Sprintf("foreign_keys[%d]", i)
		if key.Name != "" {
			if err := ValidateIdentifier(key.Name); err != nil {
				return validationError(path+".name", "%s", err)
			}
			if owner, exists := constraintNames[key.Name]; exists {
				return validationError(path+".name", "duplicates constraint %q declared at %s", key.Name, owner)
			}
			constraintNames[key.Name] = path
		}
		if err := validateColumnList(path+".columns", key.Columns, columns, true); err != nil {
			return err
		}
		if key.ReferencedSchema != "" {
			if err := ValidateIdentifier(key.ReferencedSchema); err != nil {
				return validationError(path+".referenced_schema", "%s", err)
			}
		}
		if err := ValidateIdentifier(key.ReferencedTable); err != nil {
			return validationError(path+".referenced_table", "%s", err)
		}
		if err := validateIdentifierList(path+".referenced_columns", key.ReferencedColumns, true); err != nil {
			return err
		}
		if len(key.Columns) != len(key.ReferencedColumns) {
			return validationError(path, "has %d local columns and %d referenced columns", len(key.Columns), len(key.ReferencedColumns))
		}
		if !key.OnDelete.valid() {
			return validationError(path+".on_delete", "unsupported reference action %q", key.OnDelete)
		}
		if !key.OnUpdate.valid() {
			return validationError(path+".on_update", "unsupported reference action %q", key.OnUpdate)
		}
		if !key.Match.valid() {
			return validationError(path+".match", "unsupported foreign key match type %q", key.Match)
		}
		if !key.Deferrable.valid() {
			return validationError(path+".deferrable", "unsupported foreign key deferrability %q", key.Deferrable)
		}
	}
	return nil
}

func validateColumnList(path string, names []string, columns map[string]struct{}, required bool) error {
	if required && len(names) == 0 {
		return validationError(path, "must not be empty")
	}
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if _, exists := columns[name]; !exists {
			return validationError(itemPath, "references unknown column %q", name)
		}
		if _, exists := seen[name]; exists {
			return validationError(itemPath, "duplicates column %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}

func validateIdentifierList(path string, names []string, required bool) error {
	if required && len(names) == 0 {
		return validationError(path, "must not be empty")
	}
	seen := make(map[string]struct{}, len(names))
	for i, name := range names {
		itemPath := fmt.Sprintf("%s[%d]", path, i)
		if err := ValidateIdentifier(name); err != nil {
			return validationError(itemPath, "%s", err)
		}
		if _, exists := seen[name]; exists {
			return validationError(itemPath, "duplicates column %q", name)
		}
		seen[name] = struct{}{}
	}
	return nil
}
