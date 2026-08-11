package schema

// TableOption configures NewTable and MustTable. A column constructor such
// as Integer or Text and a constraint constructor such as PrimaryKey, Unique,
// Check, Index, or ForeignKey each return a TableOption, so every argument to
// NewTable shares this one type regardless of what it declares.
//
// NewTable applies every option to a tableBuilder before assembling the
// descriptor, collecting columns and constraints into separate lists rather
// than building the Table incrementally. That separation is what lets
// PrimaryKey("id") appear before Integer("id"): order among TableOptions
// never matters.
type TableOption interface {
	applyTable(*tableBuilder) error
}

// tableBuilder accumulates the pieces of a Table while NewTable applies
// options, so they can be assembled into the descriptor once every option
// has run.
type tableBuilder struct {
	schemaName        string
	columns           []Column
	primaryKey        []string
	primaryKeySet     bool
	uniqueConstraints []UniqueConstraint
	checks            []CheckConstraint
	indexes           []IndexDef
	foreignKeys       []ForeignKeyDef
	relationships     []Relationship
}

// NewTable assembles a Table named name from opts. It collects the columns
// and constraints each option declares, in the order NewTable receives them
// rather than the order in which they configure the descriptor, so the
// options themselves may appear in any order. The assembled descriptor is
// then validated exactly as Table.Validate would validate a hand-built
// struct literal, and the first error encountered, from an option or from
// validation, is returned wrapped in no additional context: every error
// NewTable can return is already a *ValidationError.
func NewTable(name string, opts ...TableOption) (Table, error) {
	var builder tableBuilder
	for _, opt := range opts {
		if opt == nil {
			return Table{}, validationError("table", "option must not be nil")
		}
		if err := opt.applyTable(&builder); err != nil {
			return Table{}, err
		}
	}

	table := Table{
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
		return Table{}, err
	}
	return table, nil
}

// MustTable is NewTable but panics instead of returning an error. It suits a
// table built once, at package initialization, from a fixed set of options,
// where a caller has no path to recover from a mistake in the descriptor and
// would otherwise have to check an error that can only mean a bug.
func MustTable(name string, opts ...TableOption) Table {
	table, err := NewTable(name, opts...)
	if err != nil {
		panic(err)
	}
	return table
}

// schemaTableOption sets the namespace a table is qualified with.
type schemaTableOption string

// InSchema qualifies a table built by NewTable or MustTable with a
// namespace, exactly like setting Table.Schema on a struct literal: a
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
type uniqueTableOption UniqueConstraint

// Unique declares that columns must be unique together, as an unnamed
// UniqueConstraint. A named constraint, needed only when something else must
// reference the constraint by name, is still declared with a struct literal:
// schema.Table{UniqueConstraints: []schema.UniqueConstraint{{Name: ..., Columns: ...}}}.
func Unique(columns ...string) TableOption {
	return uniqueTableOption(UniqueConstraint{Columns: append([]string(nil), columns...)})
}

func (o uniqueTableOption) applyTable(b *tableBuilder) error {
	b.uniqueConstraints = append(b.uniqueConstraints, UniqueConstraint(o))
	return nil
}

// checkTableOption declares one check constraint.
type checkTableOption string

// Check declares an unnamed check constraint that expression must satisfy
// for every row. A named check constraint is declared with a struct literal.
func Check(expression string) TableOption {
	return checkTableOption(expression)
}

func (o checkTableOption) applyTable(b *tableBuilder) error {
	b.checks = append(b.checks, CheckConstraint{Expression: string(o)})
	return nil
}

// indexTableOption declares one secondary index.
type indexTableOption IndexDef

// Index declares a secondary, non-unique index named name over columns. An
// index that must report Unique, or that needs no name a caller supplies, is
// declared with a struct literal: schema.Table{Indexes: []schema.IndexDef{...}}.
func Index(name string, columns ...string) TableOption {
	return indexTableOption(IndexDef{Name: name, Columns: append([]string(nil), columns...)})
}

func (o indexTableOption) applyTable(b *tableBuilder) error {
	b.indexes = append(b.indexes, IndexDef(o))
	return nil
}
