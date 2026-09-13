package schema

// ForeignKeyOption configures the ForeignKey and ForeignKeyOn constructors.
type ForeignKeyOption interface {
	applyForeignKey(*foreignKeyBuilder) error
}

// foreignKeyBuilder accumulates a ForeignKeyDef while ForeignKeyOptions run,
// so ForeignKeyOn can assemble the descriptor once every option has applied.
type foreignKeyBuilder struct {
	key ForeignKeyDef
}

// foreignKeyTableOption carries either a built foreignKeyBuilder or the
// first error a ForeignKeyOption reported, the same deferred-error shape
// columnTableOption uses for column options.
type foreignKeyTableOption struct {
	builder foreignKeyBuilder
	err     error
}

func (o foreignKeyTableOption) applyTable(b *tableBuilder) error {
	if o.err != nil {
		return o.err
	}
	b.foreignKeys = append(b.foreignKeys, o.builder.key)
	return nil
}

// ForeignKey declares a foreign key over the single local column, configured
// by opts.
func ForeignKey(column string, opts ...ForeignKeyOption) TableOption {
	return ForeignKeyOn([]string{column}, opts...)
}

// ForeignKeyOn declares a foreign key over columns, configured by opts. It is
// the composite counterpart to ForeignKey, sharing its builder and its
// option set: References or ReferencesIn must list one referenced column per
// entry in columns.
func ForeignKeyOn(columns []string, opts ...ForeignKeyOption) TableOption {
	builder := foreignKeyBuilder{key: ForeignKeyDef{Columns: append([]string(nil), columns...)}}
	for _, opt := range opts {
		if opt == nil {
			return foreignKeyTableOption{err: validationError("foreign_keys", "option for columns %v must not be nil", columns)}
		}
		if err := opt.applyForeignKey(&builder); err != nil {
			return foreignKeyTableOption{err: err}
		}
	}
	return foreignKeyTableOption{builder: builder}
}

// namedForeignKeyOption names the constraint.
type namedForeignKeyOption string

// Named states the foreign key's constraint name.
func Named(name string) ForeignKeyOption {
	return namedForeignKeyOption(name)
}

func (o namedForeignKeyOption) applyForeignKey(b *foreignKeyBuilder) error {
	b.key.Name = string(o)
	return nil
}

// referencesForeignKeyOption states what a foreign key points at.
type referencesForeignKeyOption struct {
	schemaName string
	table      string
	columns    []string
}

// References states the table and columns a foreign key points at.
func References(table string, columns ...string) ForeignKeyOption {
	return referencesForeignKeyOption{table: table, columns: append([]string(nil), columns...)}
}

// ReferencesIn states the schema-qualified table and columns a foreign key
// points at, exactly like References but naming the schema, database, or
// attached-database holding the referenced table.
func ReferencesIn(schemaName, table string, columns ...string) ForeignKeyOption {
	return referencesForeignKeyOption{schemaName: schemaName, table: table, columns: append([]string(nil), columns...)}
}

func (o referencesForeignKeyOption) applyForeignKey(b *foreignKeyBuilder) error {
	b.key.ReferencedSchema = o.schemaName
	b.key.ReferencedTable = o.table
	b.key.ReferencedColumns = o.columns
	return nil
}

// onDeleteForeignKeyOption states a foreign key's ON DELETE action.
type onDeleteForeignKeyOption ReferenceAction

// OnDelete states the action taken when the referenced row is deleted.
func OnDelete(action ReferenceAction) ForeignKeyOption {
	return onDeleteForeignKeyOption(action)
}

func (o onDeleteForeignKeyOption) applyForeignKey(b *foreignKeyBuilder) error {
	b.key.OnDelete = ReferenceAction(o)
	return nil
}

// onUpdateForeignKeyOption states a foreign key's ON UPDATE action.
type onUpdateForeignKeyOption ReferenceAction

// OnUpdate states the action taken when the referenced row's key changes.
func OnUpdate(action ReferenceAction) ForeignKeyOption {
	return onUpdateForeignKeyOption(action)
}

func (o onUpdateForeignKeyOption) applyForeignKey(b *foreignKeyBuilder) error {
	b.key.OnUpdate = ReferenceAction(o)
	return nil
}
