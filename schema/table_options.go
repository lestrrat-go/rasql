package schema

// TableOption configures NewTable and MustTable. A column constructor such
// as Integer or Text and a constraint constructor such as PrimaryKey, Unique,
// Check, Index, or ForeignKey each return a TableOption, so every argument to
// NewTable shares this one type regardless of what it declares.
//
// NewTable applies every option to a tableBuilder before assembling the
// descriptor, collecting columns and constraints into separate lists rather
// than building the TableDef incrementally. That separation is what lets
// PrimaryKey("id") appear before Integer("id"): order among TableOptions
// never matters.
type TableOption interface {
	applyTable(*tableBuilder) error
}

// tableBuilder accumulates the pieces of a TableDef while NewTable applies
// options, so they can be assembled into the descriptor once every option
// has run.
type tableBuilder struct {
	schemaName        string
	columns           []ColumnDef
	primaryKey        []string
	primaryKeySet     bool
	uniqueConstraints []UniqueDef
	checks            []CheckDef
	indexes           []IndexDef
	foreignKeys       []ForeignKeyDef
	relationships     []RelationshipDef
}

// NewTable assembles a TableDef named name from opts. It collects the columns
// and constraints each option declares, in the order NewTable receives them
// rather than the order in which they configure the descriptor, so the
// options themselves may appear in any order. The assembled descriptor is
// then validated exactly as TableDef.Validate would validate a hand-built
// struct literal, and the first error encountered, from an option or from
// validation, is returned wrapped in no additional context: every error
// NewTable can return is already a *ValidationError.
func NewTable(name string, opts ...TableOption) (TableDef, error) {
	var builder tableBuilder
	for _, opt := range opts {
		if opt == nil {
			return TableDef{}, validationError("table", "option must not be nil")
		}
		if err := opt.applyTable(&builder); err != nil {
			return TableDef{}, err
		}
	}

	table := TableDef{
		Schema:            builder.schemaName,
		Name:              name,
		Columns:           builder.columns,
		PrimaryKey:        builder.primaryKey,
		UniqueConstraints: builder.uniqueConstraints,
		Checks:            builder.checks,
		Indexes:           builder.indexes,
		ForeignKeys:       builder.foreignKeys,
		Relationships:     builder.relationships,
	}
	if err := table.Validate(); err != nil {
		return TableDef{}, err
	}
	return table, nil
}

// MustTable is NewTable but panics instead of returning an error. It suits a
// table built once, at package initialization, from a fixed set of options,
// where a caller has no path to recover from a mistake in the descriptor and
// would otherwise have to check an error that can only mean a bug.
func MustTable(name string, opts ...TableOption) TableDef {
	table, err := NewTable(name, opts...)
	if err != nil {
		panic(err)
	}
	return table
}

// schemaTableOption sets the namespace a table is qualified with.
type schemaTableOption string

// InSchema qualifies a table built by NewTable or MustTable with a
// namespace, exactly like setting TableDef.Schema on a struct literal: a
// PostgreSQL schema, a MySQL database, or a SQLite attached-database name.
func InSchema(name string) TableOption {
	return schemaTableOption(name)
}

func (o schemaTableOption) applyTable(b *tableBuilder) error {
	b.schemaName = string(o)
	return nil
}

// primaryKeyTableOption declares the table's primary key.
type primaryKeyTableOption []string

// PrimaryKey declares the columns, from Columns, that uniquely identify each
// row. Calling PrimaryKey more than once for the same table is rejected,
// since two calls almost always name two different, conflicting keys rather
// than one the caller meant to build up incrementally; a composite key is
// declared with every one of its columns in a single call instead.
func PrimaryKey(columns ...string) TableOption {
	return primaryKeyTableOption(append([]string(nil), columns...))
}

func (o primaryKeyTableOption) applyTable(b *tableBuilder) error {
	if b.primaryKeySet {
		return validationError("primary_key", "declared more than once")
	}
	b.primaryKeySet = true
	b.primaryKey = []string(o)
	return nil
}

// uniqueTableOption declares one unique constraint.
type uniqueTableOption UniqueDef

// Unique declares that columns must be unique together, as an unnamed
// UniqueDef.
func Unique(columns ...string) TableOption {
	return uniqueTableOption(UniqueDef{Columns: append([]string(nil), columns...)})
}

// UniqueNamed declares that columns must be unique together, as a UniqueDef
// named name. Naming the constraint gives something else, such as a foreign
// key on another table, a name to reference it by.
func UniqueNamed(name string, columns ...string) TableOption {
	return uniqueTableOption(UniqueDef{Name: name, Columns: append([]string(nil), columns...)})
}

func (o uniqueTableOption) applyTable(b *tableBuilder) error {
	b.uniqueConstraints = append(b.uniqueConstraints, UniqueDef(o))
	return nil
}

// checkTableOption declares one check constraint.
type checkTableOption CheckDef

// Check declares an unnamed check constraint that expression must satisfy
// for every row.
func Check(expression string) TableOption {
	return checkTableOption(CheckDef{Expression: expression})
}

// CheckNamed declares a check constraint named name that expression must
// satisfy for every row. Naming the constraint gives something else a name
// to reference it by.
func CheckNamed(name, expression string) TableOption {
	return checkTableOption(CheckDef{Name: name, Expression: expression})
}

func (o checkTableOption) applyTable(b *tableBuilder) error {
	b.checks = append(b.checks, CheckDef(o))
	return nil
}

// indexTableOption declares one secondary index.
type indexTableOption IndexDef

// Index declares a secondary, non-unique index named name over columns.
func Index(name string, columns ...string) TableOption {
	return indexTableOption(IndexDef{Name: name, Columns: append([]string(nil), columns...)})
}

// UniqueIndex declares a secondary, unique index named name over columns.
// Unlike UniqueNamed, the index is not usable as a foreign-key target on
// every dialect; it exists to speed up lookups and enforce uniqueness
// through the index itself rather than through a table constraint.
func UniqueIndex(name string, columns ...string) TableOption {
	return indexTableOption(IndexDef{Name: name, Columns: append([]string(nil), columns...), Unique: true})
}

func (o indexTableOption) applyTable(b *tableBuilder) error {
	b.indexes = append(b.indexes, IndexDef(o))
	return nil
}
