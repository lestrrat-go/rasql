package schema

import (
	"encoding/json"
	"fmt"
	"go/token"
	"maps"
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

	// Hidden marks a column a SQLite virtual table module declares
	// hidden — excluded from SELECT * and from an INSERT that names no
	// column list, but still addressable by name, such as FTS5's own
	// table-name column used for MATCH filtering or its rank column. Its
	// zero value, false, means an ordinary column, which is what every
	// ColumnDef and every checked-in generated file written before this
	// field existed has always meant. TableDef.Validate rejects a true
	// Hidden on a table whose TableDef.VirtualTableModule is empty,
	// since a hidden column is a virtual-table-module concept. A plain
	// table, PostgreSQL, and MySQL have no equivalent concept, so this
	// never comes from a PostgreSQL or MySQL descriptor, or from an
	// ordinary SQLite table.
	Hidden bool `json:",omitempty"`
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
		Hidden              bool             `json:"Hidden,omitempty"`
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
		Hidden:              c.Hidden,
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
		Hidden              bool             `json:"Hidden,omitempty"`
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
		Hidden:              wire.Hidden,
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

	// Keys, when non-empty, is the full ordered list of per-key facts for
	// a SQLite unique constraint that orders at least one key DESC or
	// names a non-default collation on it — the same two facts
	// IndexDef.Keys attaches to a regular index's keys, reusing
	// IndexKeyDef rather than a second type with the same shape. When
	// Keys is set it replaces Columns as the source of the constraint's
	// key order, the same way IndexDef.Keys replaces IndexDef.Columns:
	// Columns is left empty, and each IndexKeyDef.Expression carries
	// that key's own column name (SQLite's own grammar prohibits an
	// expression inside a UNIQUE table constraint, so Expression is
	// always a bare column name here, never expression text).
	// IndexKeyDef.OperatorClass and .PrefixLength never come from a
	// SQLite descriptor, the same as they never come from a SQLite
	// IndexDef.Keys. Its zero value, nil, means every key is a plain
	// ascending column in the default collation, described by Columns
	// exactly as before this field existed. PostgreSQL and MySQL have no
	// per-key unique-constraint ordering or collation concept, so this
	// never comes from a PostgreSQL or MySQL descriptor.
	//
	// Unlike IndexDef.Keys, which reads a regular index's own collation
	// back from PRAGMA index_xinfo verbatim, inspect reads a UniqueDef's
	// Keys from the constraint's own parsed CREATE TABLE text, so an
	// unquoted collation name comes back however rasql's SQLite parser
	// folds it, the same folding every other identifier this package
	// reads from parsed DDL text already carries.
	//
	// A UniqueDef naming Keys is describable but not yet renderable:
	// inspect records what a live SQLite UNIQUE constraint's own keys
	// actually use, and TableDef.Validate accepts it, but
	// render.CreateTable and the migrate diff-live path refuse to build
	// DDL for one, because rasql does not yet know how to construct a
	// DESC key or a non-default collation inside a UNIQUE constraint.
	Keys []IndexKeyDef `json:",omitempty"`

	// Temporal marks a PostgreSQL 18+ temporal unique constraint, declared
	// with WITHOUT OVERLAPS on its last column. Its zero value, false,
	// means the constraint is an ordinary, non-temporal UNIQUE constraint,
	// which is what every UniqueDef written before this field existed has
	// always meant. MySQL and SQLite have no temporal-constraint concept,
	// so this never comes from a MySQL or SQLite descriptor.
	//
	// A true Temporal is describable but not yet renderable: inspect
	// records what a live PostgreSQL unique constraint actually declares,
	// and TableDef.Validate accepts it, but render.CreateTable and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct a WITHOUT OVERLAPS clause.
	Temporal bool `json:",omitempty"`

	// StorageParameters holds a named PostgreSQL unique constraint's
	// backing index's storage parameters — settings such as fillfactor
	// that a CREATE INDEX ... WITH (...) clause attaches to an index —
	// keyed and valued exactly as the server reports them. Its zero
	// value, nil, means the backing index carries no storage parameters,
	// PostgreSQL's own default, which is what every UniqueDef written
	// before this field existed has always meant. See
	// IndexDef.StorageParameters for the same fact on a plain index.
	//
	// A non-empty StorageParameters is describable but not yet
	// renderable, on the same terms as IndexDef.StorageParameters.
	StorageParameters map[string]string `json:",omitempty"`

	// Tablespace names the PostgreSQL tablespace holding a named unique
	// constraint's backing index, or the empty string for the database's
	// default tablespace. Its zero value, "", means the default
	// tablespace, which is what every UniqueDef written before this field
	// existed has always meant. See IndexDef.Tablespace for the same fact
	// on a plain index.
	//
	// A non-empty Tablespace is describable but not yet renderable, on
	// the same terms as IndexDef.Tablespace.
	Tablespace string `json:",omitempty"`

	// ReplicaIdentity marks a named unique constraint's backing index as
	// the PostgreSQL table's REPLICA IDENTITY USING INDEX for logical
	// replication, in place of the primary key. Its zero value, false,
	// means the backing index is not the replica identity, which is what
	// every UniqueDef written before this field existed has always
	// meant. See IndexDef.ReplicaIdentity for the same fact on a plain
	// index.
	//
	// A true ReplicaIdentity is describable but not yet renderable, on
	// the same terms as IndexDef.ReplicaIdentity.
	ReplicaIdentity bool `json:",omitempty"`

	// Collations records a named PostgreSQL unique constraint's backing
	// index's non-default per-column collation, keyed by column name and
	// valued by the collation exactly as the server reports it. A column
	// missing from the map uses its own default collation. Its zero
	// value, nil, means every column of the constraint uses its own
	// default collation, which is what every UniqueDef written before
	// this field existed has always meant. See IndexKeyDef.Collation for
	// the same fact on a plain index's key.
	//
	// A non-empty Collations is describable but not yet renderable:
	// inspect records a live PostgreSQL unique constraint's backing
	// index's per-column collations, and TableDef.Validate accepts them,
	// but render.CreateTable and the migrate diff-live path refuse to
	// build DDL for one, because rasql does not yet know how to
	// construct a non-default COLLATE clause on a unique constraint's
	// column.
	Collations map[string]string `json:",omitempty"`
}

// Clone returns a copy of u that shares no slice or map with u. Each field
// keeps the source's own nilness: a nil field clones to nil, and a
// stated-but-empty one clones to a non-nil empty container.
func (u UniqueDef) Clone() UniqueDef {
	u.Columns = slices.Clone(u.Columns)
	u.IncludeColumns = slices.Clone(u.IncludeColumns)
	u.Keys = slices.Clone(u.Keys)
	u.StorageParameters = maps.Clone(u.StorageParameters)
	u.Collations = maps.Clone(u.Collations)
	return u
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

	// IncludeColumns lists a PostgreSQL index's INCLUDE columns: columns
	// carried by the index for covering reads without taking part in its
	// key. Its zero value, nil, means the index has no INCLUDE clause,
	// which is what every IndexDef written before this field existed has
	// always meant.
	//
	// A non-empty IncludeColumns is describable but not yet renderable:
	// inspect records a live PostgreSQL index's included columns, and
	// TableDef.Validate accepts them, but render.CreateIndexes and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct an INCLUDE clause.
	IncludeColumns []string `json:",omitempty"`

	// Invisible marks a MySQL index the optimizer ignores for query
	// planning without dropping it: MySQL still maintains it on every
	// write and still enforces uniqueness through it. Its zero value,
	// false, means the index is visible, which is what every IndexDef
	// written before this field existed has always meant.
	//
	// A true Invisible is describable but not yet renderable:
	// inspect records a live MySQL index's visibility, and
	// TableDef.Validate accepts it, but render.CreateIndexes and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct an INVISIBLE index. PostgreSQL
	// and SQLite have no index-visibility concept, so this never comes
	// from a PostgreSQL or SQLite descriptor.
	Invisible bool `json:",omitempty"`

	// NotValid records a PostgreSQL index left behind by a failed
	// CREATE INDEX CONCURRENTLY (or similar concurrent operation):
	// PostgreSQL keeps the index's catalog entry but marks it unusable
	// for lookups or constraint enforcement. Its zero value, false,
	// means the index is valid and usable, which is what every IndexDef
	// written before this field existed has always meant. MySQL and
	// SQLite have no equivalent concept, so this never comes from a
	// MySQL or SQLite descriptor.
	//
	// A true NotValid is describable but not yet renderable: inspect
	// records what a live PostgreSQL index's own validity actually is,
	// and TableDef.Validate accepts it, but render.CreateIndexes and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct an invalid index.
	NotValid bool `json:",omitempty"`

	// StorageParameters holds a PostgreSQL index's storage parameters —
	// settings such as fillfactor that a CREATE INDEX ... WITH (...)
	// clause attaches to an index — keyed and valued exactly as the
	// server reports them. Its zero value, nil, means the index carries
	// no storage parameters, PostgreSQL's own default, which is what
	// every IndexDef written before this field existed has always
	// meant. MySQL and SQLite have no equivalent concept, so this never
	// comes from a MySQL or SQLite descriptor.
	//
	// A non-empty StorageParameters is describable but not yet
	// renderable: inspect records a live PostgreSQL index's storage
	// parameters, and TableDef.Validate accepts them, but
	// render.CreateIndexes and the migrate diff-live path refuse to
	// build DDL for one, because rasql does not yet know how to
	// construct a WITH (...) clause on an index.
	StorageParameters map[string]string `json:",omitempty"`

	// Tablespace names the PostgreSQL tablespace holding the index, or
	// the empty string for the database's default tablespace. Its zero
	// value, "", means the default tablespace, which is what every
	// IndexDef written before this field existed has always meant.
	// MySQL and SQLite have no equivalent concept, so this never comes
	// from a MySQL or SQLite descriptor.
	//
	// A non-empty Tablespace is describable but not yet renderable:
	// inspect records a live PostgreSQL index's tablespace, and
	// TableDef.Validate accepts it, but render.CreateIndexes and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct a TABLESPACE clause.
	Tablespace string `json:",omitempty"`

	// ReplicaIdentity marks the PostgreSQL index the table uses as its
	// REPLICA IDENTITY USING INDEX for logical replication, in place of
	// the primary key. Its zero value, false, means the index is not
	// the replica identity, which is what every IndexDef written before
	// this field existed has always meant. MySQL and SQLite have no
	// equivalent concept, so this never comes from a MySQL or SQLite
	// descriptor.
	//
	// A true ReplicaIdentity is describable but not yet renderable:
	// inspect records what a live PostgreSQL table's replica identity
	// index actually is, and TableDef.Validate accepts it, but
	// render.CreateIndexes and the migrate diff-live path refuse to
	// build DDL for one, because rasql does not yet know how to
	// construct a REPLICA IDENTITY USING INDEX declaration.
	ReplicaIdentity bool `json:",omitempty"`

	// Keys, when non-empty, is the full ordered list of per-key facts for
	// an index that has at least one key ordered DESC, using a
	// non-default collation or operator class, or (MySQL) indexed over a
	// column prefix — facts that are positional, one per key, and so fit
	// neither Columns nor Expressions, both flat lists with no room to
	// attach a second fact to one position. When Keys is set it replaces
	// both Columns and Expressions as the sole source of the index's key
	// order, the same way Expressions already replaces Columns for a
	// mixed or all-expression index: Columns and Expressions are both
	// left empty, and each IndexKeyDef.Expression carries that key's own
	// text, a bare column name for a plain-column key or expression text
	// otherwise, exactly what Expressions's elements already mean. Its
	// zero value, nil, means every key is a plain ascending column or
	// expression in the default collation and operator class with no
	// prefix, described by Columns or Expressions exactly as before this
	// field existed.
	//
	// An IndexDef naming Keys is describable but not yet renderable:
	// inspect records what a live PostgreSQL, MySQL, or SQLite index's
	// keys actually use, and TableDef.Validate accepts it, but
	// render.CreateIndexes and the migrate diff-live path refuse to build
	// DDL for one, because rasql does not yet know how to construct a
	// DESC key, a non-default collation or operator class, or a MySQL
	// prefix part.
	Keys []IndexKeyDef `json:",omitempty"`

	// NullsNotDistinct marks a PostgreSQL 15+ plain (non-constraint)
	// unique index declared NULLS NOT DISTINCT, under which two NULLs in
	// the indexed columns conflict with each other instead of coexisting.
	// Its zero value, false, means NULLS DISTINCT, the SQL default and
	// the only behavior every IndexDef written before this field existed
	// has always meant. See UniqueDef.NullsNotDistinct for the same fact
	// on a named unique constraint.
	//
	// A true NullsNotDistinct is describable but not yet renderable:
	// inspect records what a live PostgreSQL unique index actually
	// declares, and TableDef.Validate accepts it, but render.CreateIndexes
	// and the migrate diff-live path refuse to build DDL for one, because
	// rasql does not yet know how to construct a NULLS NOT DISTINCT
	// clause.
	NullsNotDistinct bool `json:",omitempty"`
}

// Clone returns a copy of i that shares no slice or map with i. Each field
// keeps the source's own nilness: a nil field clones to nil, and a
// stated-but-empty one clones to a non-nil empty container.
func (i IndexDef) Clone() IndexDef {
	i.Columns = slices.Clone(i.Columns)
	i.Expressions = slices.Clone(i.Expressions)
	i.IncludeColumns = slices.Clone(i.IncludeColumns)
	i.Keys = slices.Clone(i.Keys)
	i.StorageParameters = maps.Clone(i.StorageParameters)
	return i
}

// IndexKeyDef describes one key of an index beyond its expression text:
// order, per-key collation, operator class, and MySQL prefix length. It
// exists only so IndexDef.Keys can attach these facts to one key position;
// an index where every key is a plain ascending column or expression in the
// default collation and operator class with no prefix continues to
// describe its keys with IndexDef.Columns or IndexDef.Expressions alone,
// exactly as before this type existed. See IndexDef.Keys's own doc for when
// a per-key fact forces an index to use Keys instead.
type IndexKeyDef struct {
	// Expression is the key's own text: a bare column name for a
	// plain-column key, or expression text for an expression key. This is
	// the same convention IndexDef.Expressions's elements use: a plain
	// column is itself a valid SQL expression.
	Expression string

	// Descending marks a key ordered DESC rather than the engine default,
	// ASC. Its zero value, false, means ASC.
	Descending bool `json:",omitempty"`

	// Collation names a non-default per-key collation, or empty for the
	// key's own default collation. Meaningful for PostgreSQL and SQLite;
	// MySQL has no per-key collation concept, so this never comes from a
	// MySQL descriptor.
	Collation string `json:",omitempty"`

	// OperatorClass names a non-default PostgreSQL operator class for
	// this key, or empty for the default operator class. PostgreSQL only:
	// MySQL and SQLite have no operator class concept, so this never
	// comes from a MySQL or SQLite descriptor.
	OperatorClass string `json:",omitempty"`

	// PrefixLength is a MySQL index part's prefix length in characters
	// (or bytes, for a binary column) — the N in KEY (col(N)) — or zero
	// when the key indexes the whole column. MySQL only: PostgreSQL and
	// SQLite have no index-key-prefix concept, so this never comes from a
	// PostgreSQL or SQLite descriptor.
	PrefixLength int `json:",omitempty"`

	// NullsOrder names a non-default NULLS FIRST/NULLS LAST placement for
	// this key. See NullsOrder's own doc for what its zero value means.
	// PostgreSQL only: MySQL and SQLite have no independent nulls-
	// placement concept, so this never comes from a MySQL or SQLite
	// descriptor.
	NullsOrder NullsOrder `json:",omitempty"`
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

// Clone returns a copy of e that shares no slice with e. Elements keeps the
// source's own nilness: a nil Elements clones to nil, and a stated-but-empty
// one clones to a non-nil empty slice.
func (e ExclusionDef) Clone() ExclusionDef {
	e.Elements = slices.Clone(e.Elements)
	return e
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

	// Temporal marks a PostgreSQL 18+ temporal foreign key, declared with
	// PERIOD on both the referencing and referenced columns. Its zero
	// value, false, means the foreign key is an ordinary, non-temporal
	// FOREIGN KEY, which is what every ForeignKeyDef written before this
	// field existed has always meant. MySQL has no temporal-foreign-key
	// concept, so this never comes from a MySQL descriptor.
	//
	// A true Temporal is describable but not yet renderable: inspect
	// records what a live PostgreSQL foreign key actually declares, and
	// TableDef.Validate accepts it, but render.CreateTable and the
	// migrate diff-live path refuse to build DDL for one, because rasql
	// does not yet know how to construct a PERIOD foreign key.
	Temporal bool `json:",omitempty"`

	// DeleteSetColumns lists the subset of Columns a PostgreSQL 15+ ON
	// DELETE SET NULL (columns) or ON DELETE SET DEFAULT (columns) clause
	// names, in the order the server reports them. Its zero value, nil,
	// means OnDelete's SET NULL or SET DEFAULT action, if stated, applies
	// to every column in Columns, the SQL default and what every
	// ForeignKeyDef written before this field existed has always meant.
	// DeleteSetColumns is meaningless, and always nil, unless OnDelete is
	// SetNull or SetDefault. MySQL has no column-list concept for these
	// actions, so this never comes from a MySQL descriptor.
	//
	// A non-empty DeleteSetColumns is describable but not yet renderable:
	// inspect records what a live PostgreSQL foreign key actually
	// declares, and TableDef.Validate accepts it, but render.CreateTable
	// and the migrate diff-live path refuse to build DDL for one, because
	// rasql does not yet know how to construct an ON DELETE SET NULL/SET
	// DEFAULT column list.
	DeleteSetColumns []string `json:",omitempty"`
}

// Clone returns a copy of f that shares no slice with f. Each field keeps
// the source's own nilness: a nil field clones to nil, and a
// stated-but-empty one clones to a non-nil empty slice.
func (f ForeignKeyDef) Clone() ForeignKeyDef {
	f.Columns = slices.Clone(f.Columns)
	f.ReferencedColumns = slices.Clone(f.ReferencedColumns)
	f.DeleteSetColumns = slices.Clone(f.DeleteSetColumns)
	return f
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

	// VirtualTableModule names the module a SQLite CREATE VIRTUAL TABLE
	// declares with its USING clause, such as "fts5" or "rtree". Its
	// zero value, the empty string, means the table is an ordinary
	// table, not a virtual one, which is what every TableDef and every
	// checked-in generated file written before this field existed has
	// always meant. PostgreSQL and MySQL have no virtual-table concept,
	// so this never comes from a PostgreSQL or MySQL descriptor.
	//
	// A non-empty VirtualTableModule is describable but not yet
	// renderable: inspect records what a live SQLite virtual table's own
	// CREATE VIRTUAL TABLE definition declares, and TableDef.Validate
	// accepts it, but render.CreateTable and the migrate diff-live path
	// refuse to build DDL for one, because rasql does not yet know how
	// to construct a CREATE VIRTUAL TABLE statement. A virtual table has
	// no primary key, table option, unique constraint, check, index, or
	// foreign key that this package can independently verify — those are
	// the module's own business, not SQLite's table catalog — so
	// TableDef.Validate rejects any of those stated alongside a non-empty
	// VirtualTableModule.
	VirtualTableModule string `json:",omitempty"`

	// VirtualTableModuleArguments lists VirtualTableModule's own
	// arguments, exactly as its CREATE VIRTUAL TABLE definition wrote
	// them, one raw text span per argument in declaration order — a
	// module defines its own argument grammar (FTS5's own
	// column-definition-like arguments, for instance), so this package
	// does not parse into it. Its zero value, nil, means the module took
	// no arguments, or VirtualTableModule is itself empty. It is
	// meaningless, and TableDef.Validate rejects it, without a
	// VirtualTableModule.
	//
	// VirtualTableModuleArguments is describable but not yet renderable,
	// on the same terms as VirtualTableModule.
	VirtualTableModuleArguments []string `json:",omitempty"`

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

// Clone returns a copy of t that shares no slice, no map and no pointer with
// t, at any depth: mutating either side afterwards is invisible to the other.
// Every container keeps the source's own nilness, so a nil field clones to
// nil and a stated-but-empty one clones to a non-nil empty container, and a
// clone is therefore reflect.DeepEqual to its source.
//
// A descriptor type that owns a container of its own carries its own Clone
// method, and this method routes each such field through it, so a container
// field added to one of those types is copied here as soon as that type's
// own Clone copies it. ColumnDef, CheckDef, IndexKeyDef and
// ExclusionElementDef own no container, so their elements are copied by
// assignment and have no Clone method to route through. ColumnDef.Type is
// the one field an assignment does not settle, because a ColumnType is an
// interface that a pointer to a built-in type also satisfies; each column's
// Type is routed through cloneColumnType, which copies the pointed-to value
// of such a pointer.
func (t TableDef) Clone() TableDef {
	clone := t
	clone.Columns = cloneColumns(t.Columns)
	clone.PrimaryKey = slices.Clone(t.PrimaryKey)
	clone.VirtualTableModuleArguments = slices.Clone(t.VirtualTableModuleArguments)
	clone.UniqueConstraints = cloneEach(t.UniqueConstraints)
	clone.Checks = slices.Clone(t.Checks)
	clone.ExclusionConstraints = cloneEach(t.ExclusionConstraints)
	clone.Indexes = cloneEach(t.Indexes)
	clone.ForeignKeys = cloneEach(t.ForeignKeys)
	clone.Relationships = cloneEach(t.Relationships)
	return clone
}

// cloneColumns returns a copy of source in which no element shares anything
// with the source element it was copied from. A ColumnDef owns no container,
// so an assignment copies all of it but Type, whose interface value is
// routed through cloneColumnType. It preserves source's nilness exactly as
// slices.Clone does.
func cloneColumns(source []ColumnDef) []ColumnDef {
	clone := slices.Clone(source)
	for i := range clone {
		clone[i].Type = cloneColumnType(clone[i].Type)
	}
	return clone
}

// cloneable is satisfied by every descriptor type in this package that owns
// a container of its own and so carries its own Clone method.
type cloneable[T any] interface {
	Clone() T
}

// cloneEach returns a copy of source with every element replaced by that
// element's own clone. It preserves source's nilness exactly as slices.Clone
// does: a nil source returns nil, and a non-nil empty source returns a
// non-nil empty slice.
func cloneEach[T cloneable[T]](source []T) []T {
	clone := slices.Clone(source)
	for i, element := range clone {
		clone[i] = element.Clone()
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
		if column.Hidden && t.VirtualTableModule == "" {
			return validationError(path+".hidden", "must not be set without a VirtualTableModule")
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
	if t.VirtualTableModule == "" {
		if len(t.VirtualTableModuleArguments) > 0 {
			return validationError("table.virtual_table_module_arguments", "must not be set without a VirtualTableModule")
		}
	} else {
		if len(t.PrimaryKey) > 0 {
			return validationError("table.primary_key", "must be empty on a SQLite virtual table")
		}
		if t.Strict {
			return validationError("table.strict", "must not be set on a SQLite virtual table")
		}
		if t.WithoutRowID {
			return validationError("table.without_rowid", "must not be set on a SQLite virtual table")
		}
		if len(t.UniqueConstraints) > 0 {
			return validationError("table.unique_constraints", "must be empty on a SQLite virtual table")
		}
		if len(t.Checks) > 0 {
			return validationError("table.checks", "must be empty on a SQLite virtual table")
		}
		if len(t.ExclusionConstraints) > 0 {
			return validationError("table.exclusion_constraints", "must be empty on a SQLite virtual table")
		}
		if len(t.Indexes) > 0 {
			return validationError("table.indexes", "must be empty on a SQLite virtual table")
		}
		if len(t.ForeignKeys) > 0 {
			return validationError("table.foreign_keys", "must be empty on a SQLite virtual table")
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
		if len(constraint.Keys) > 0 {
			if len(constraint.Columns) > 0 {
				return validationError(itemPath+".columns", "must be empty when Keys describes the constraint's keys instead")
			}
			for j, key := range constraint.Keys {
				keyPath := fmt.Sprintf("%s.keys[%d]", itemPath, j)
				if key.Expression == "" {
					return validationError(keyPath+".expression", "must not be empty")
				}
				if key.PrefixLength < 0 {
					return validationError(keyPath+".prefix_length", "must not be negative")
				}
			}
		} else if err := validateColumnList(itemPath+".columns", constraint.Columns, columns, true); err != nil {
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
		for key := range constraint.StorageParameters {
			if key == "" {
				return validationError(itemPath+".storage_parameters", "must not have an empty key")
			}
		}
		if constraint.Tablespace != "" {
			if err := ValidateIdentifier(constraint.Tablespace); err != nil {
				return validationError(itemPath+".tablespace", "%s", err)
			}
		}
		if len(constraint.Collations) > 0 {
			constraintColumns := make(map[string]struct{}, len(constraint.Columns))
			for _, name := range constraint.Columns {
				constraintColumns[name] = struct{}{}
			}
			for name, collation := range constraint.Collations {
				if _, exists := constraintColumns[name]; !exists {
					return validationError(itemPath+".collations", "names column %q, which is not part of the constraint", name)
				}
				if collation == "" {
					return validationError(itemPath+".collations", "must not have an empty value for column %q", name)
				}
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
		switch {
		case len(index.Keys) > 0:
			if len(index.Columns) > 0 {
				return validationError(path+".columns", "must be empty when Keys describes the index's keys instead")
			}
			if len(index.Expressions) > 0 {
				return validationError(path+".expressions", "must be empty when Keys describes the index's keys instead")
			}
			for j, key := range index.Keys {
				keyPath := fmt.Sprintf("%s.keys[%d]", path, j)
				if key.Expression == "" {
					return validationError(keyPath+".expression", "must not be empty")
				}
				if key.PrefixLength < 0 {
					return validationError(keyPath+".prefix_length", "must not be negative")
				}
				if !key.NullsOrder.valid() {
					return validationError(keyPath+".nulls_order", "unsupported nulls order %q", key.NullsOrder)
				}
			}
		case len(index.Expressions) > 0:
			if len(index.Columns) > 0 {
				return validationError(path+".columns", "must be empty when Expressions describes the index's keys instead")
			}
			for j, expression := range index.Expressions {
				if expression == "" {
					return validationError(fmt.Sprintf("%s.expressions[%d]", path, j), "must not be empty")
				}
			}
		default:
			if err := validateColumnList(path+".columns", index.Columns, columns, true); err != nil {
				return err
			}
		}
		if len(index.IncludeColumns) > 0 {
			seen := make(map[string]struct{}, len(index.Columns)+len(index.IncludeColumns))
			for _, name := range index.Columns {
				seen[name] = struct{}{}
			}
			for j, name := range index.IncludeColumns {
				includedPath := fmt.Sprintf("%s.include_columns[%d]", path, j)
				if _, exists := columns[name]; !exists {
					return validationError(includedPath, "references unknown column %q", name)
				}
				if _, exists := seen[name]; exists {
					return validationError(includedPath, "duplicates column %q", name)
				}
				seen[name] = struct{}{}
			}
		}
		for key := range index.StorageParameters {
			if key == "" {
				return validationError(path+".storage_parameters", "must not have an empty key")
			}
		}
		if index.Tablespace != "" {
			if err := ValidateIdentifier(index.Tablespace); err != nil {
				return validationError(path+".tablespace", "%s", err)
			}
		}
		if index.ReplicaIdentity && !index.Unique {
			return validationError(path+".replica_identity", "must not be set on a non-unique index: PostgreSQL requires a unique index to serve as REPLICA IDENTITY USING INDEX")
		}
		if index.NullsNotDistinct && !index.Unique {
			return validationError(path+".nulls_not_distinct", "must not be set on a non-unique index: NULLS NOT DISTINCT only applies to a unique index")
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
		if len(key.DeleteSetColumns) > 0 {
			if key.OnDelete != SetNull && key.OnDelete != SetDefault {
				return validationError(path+".delete_set_columns", "must not be set unless OnDelete is SetNull or SetDefault")
			}
			referencing := make(map[string]struct{}, len(key.Columns))
			for _, name := range key.Columns {
				referencing[name] = struct{}{}
			}
			seen := make(map[string]struct{}, len(key.DeleteSetColumns))
			for j, name := range key.DeleteSetColumns {
				itemPath := fmt.Sprintf("%s.delete_set_columns[%d]", path, j)
				if _, exists := referencing[name]; !exists {
					return validationError(itemPath, "names column %q, which is not part of the foreign key", name)
				}
				if _, exists := seen[name]; exists {
					return validationError(itemPath, "duplicates column %q", name)
				}
				seen[name] = struct{}{}
			}
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
