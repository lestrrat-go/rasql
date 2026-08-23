package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"github.com/lestrrat-go/rasql/exec"
	"github.com/lestrrat-go/rasql/internal/method"
	"github.com/lestrrat-go/rasql/internal/nilcheck"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// ColumnValuer is implemented by row types that supply their own column values.
// Insert and Update prefer it over struct tags, so a generated row type carries
// its own mapping instead of restating it as a tag.
//
// One shape is mapped by its tags even though it satisfies ColumnValuer: a
// struct that embeds a ColumnValuer, tags fields of its own, and declares no
// ColumnValue. Go promotes the embedded ColumnValue to the outer struct, where
// it knows nothing about the fields declared around it, so the tags win.
// Declaring ColumnValue on the outer type maps such a struct by method again,
// whether or not its tags stay.
type ColumnValuer interface {
	ColumnValue(name string) (any, bool)
}

// Insert encodes value's rasql-tagged fields and writes it to table.
// Value must have one exported tagged field for every table column,
// or implement ColumnValuer.
func Insert[T any](ctx context.Context, db DB, table Table[T], value T) (sql.Result, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	return InsertWithOptions(ctx, db, table, value)
}

// InsertWithOptions encodes value's rasql-tagged fields and writes it to table.
// Value must have one exported tagged field for every table column,
// or implement ColumnValuer. DefaultColumns omits named columns so the
// database applies their defaults.
func InsertWithOptions[T any](ctx context.Context, db DB, table Table[T], value T, options ...InsertOption) (sql.Result, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	defaults, err := insertDefaults(options)
	if err != nil {
		return nil, fmt.Errorf("rasql: configure INSERT: %w", err)
	}
	statement, err := typedInsert(table, value, defaults)
	if err != nil {
		return nil, fmt.Errorf("rasql: build INSERT: %w", err)
	}
	return Exec(ctx, db, statement)
}

// InsertMany encodes values' rasql-tagged fields and writes them as one
// parameterized INSERT statement. Every value must have one exported tagged
// field for every table column, or implement ColumnValuer.
func InsertMany[T any](ctx context.Context, db DB, table Table[T], values []T) (sql.Result, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	return InsertManyWithOptions(ctx, db, table, values)
}

// InsertManyWithOptions is InsertMany with the same database-default options
// as InsertWithOptions. The selected default columns are omitted from every
// row, while every unselected column remains a bound value. When every column
// uses a database default, it executes one default-values INSERT per row because
// the supported dialects do not share a multi-row default-values syntax.
func InsertManyWithOptions[T any](ctx context.Context, db DB, table Table[T], values []T, options ...InsertOption) (sql.Result, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	defaults, err := insertDefaults(options)
	if err != nil {
		return nil, fmt.Errorf("rasql: configure INSERT: %w", err)
	}
	statement, err := typedInsertMany(table, values, defaults)
	if err != nil {
		return nil, fmt.Errorf("rasql: build INSERT: %w", err)
	}
	if statement.UsesDefaultValues() && len(values) > 1 {
		return execDefaultInserts(ctx, db, statement, len(values))
	}
	return Exec(ctx, db, statement)
}

// InsertOption configures InsertWithOptions.
type InsertOption interface {
	applyInsert(*insertConfig) error
}

type insertConfig struct {
	defaultColumns map[string]struct{}
}

type defaultColumnsOption []string

// DefaultColumns omits columns from an INSERT so the database applies their
// defaults. Every selected column must belong to the target table. A selected
// column's Go value is ignored, while unselected zero values are still bound.
func DefaultColumns(columns ...string) InsertOption {
	return defaultColumnsOption(append([]string(nil), columns...))
}

func (columns defaultColumnsOption) applyInsert(config *insertConfig) error {
	for _, name := range columns {
		if _, exists := config.defaultColumns[name]; exists {
			return fmt.Errorf("column %q is selected more than once for a database default", name)
		}
		config.defaultColumns[name] = struct{}{}
	}
	return nil
}

func insertDefaults(options []InsertOption) (map[string]struct{}, error) {
	config := insertConfig{defaultColumns: make(map[string]struct{})}
	for _, option := range options {
		if option == nil {
			return nil, fmt.Errorf("insert option must not be nil")
		}
		if err := option.applyInsert(&config); err != nil {
			return nil, err
		}
	}
	return config.defaultColumns, nil
}

// completeRow is the mapping a row type states when it both scans a fixed
// column order and maps result columns by name. rasqlgen writes that pair for
// every row type it emits, so the pair stands in for the generated contract
// without a marker method on the generated type. A row type that maps part of a
// table states it by declaring [DestinationScanner] alone, and typed writes
// leave its RETURNING projections alone.
type completeRow interface {
	Scanner
	DestinationScanner
}

func validateTypedWriteReturning[T any](statement query.WriteStatement) error {
	if nilcheck.Is(statement) || len(statement.Returning()) == 0 {
		return nil
	}
	var result T
	if _, ok := any(&result).(completeRow); !ok {
		return nil
	}

	target, ok := writeTargetTable(statement)
	if !ok {
		return nil
	}
	returned := make(map[string]struct{}, len(statement.Returning()))
	for _, projection := range statement.Returning() {
		name := projection.ResultAlias()
		if name == "" {
			column, ok := projection.ProjectedExpression().(query.ColumnRef)
			if !ok {
				continue
			}
			name = column.Name()
		}
		returned[name] = struct{}{}
	}
	for _, column := range target.Definition().Columns {
		if _, ok := returned[column.Name]; !ok {
			return fmt.Errorf("rasql: RETURNING projections omit generated row column %q", column.Name)
		}
	}
	return nil
}

func writeTargetTable(statement query.WriteStatement) (query.TableRef, bool) {
	switch statement := statement.(type) {
	case query.Insert:
		return statement.Into(), true
	case *query.Insert:
		if statement == nil {
			return query.TableRef{}, false
		}
		return statement.Into(), true
	case query.Update:
		return statement.Table(), true
	case *query.Update:
		if statement == nil {
			return query.TableRef{}, false
		}
		return statement.Table(), true
	case query.Delete:
		return statement.From(), true
	case *query.Delete:
		if statement == nil {
			return query.TableRef{}, false
		}
		return statement.From(), true
	case query.Upsert:
		return statement.Insert().Into(), true
	case *query.Upsert:
		if statement == nil {
			return query.TableRef{}, false
		}
		return statement.Insert().Into(), true
	default:
		return query.TableRef{}, false
	}
}

// QueryWriteAll runs statement through QueryWrite and decodes every
// returned row as T. The RETURNING projections must cover the columns T maps.
func QueryWriteAll[T any](ctx context.Context, db DB, statement query.WriteStatement) ([]T, error) {
	if err := validateTypedWriteReturning[T](statement); err != nil {
		return nil, err
	}
	rendered, err := exec.RenderWrite(db, statement)
	if err != nil {
		return nil, err
	}
	return collectAll(scanTypedRendered[T](ctx, db, rendered), 0)
}

// QueryWriteOne runs statement through QueryWrite and decodes exactly one
// returned row as T.
// It returns [ErrNoRows] when RETURNING produced no rows and [ErrMultipleRows]
// when it produced more than one, the same sentinels
// [TypedSelectBuilder.One] reports.
func QueryWriteOne[T any](ctx context.Context, db DB, statement query.WriteStatement) (T, error) {
	var zero T
	if err := validateTypedWriteReturning[T](statement); err != nil {
		return zero, err
	}
	rendered, err := exec.RenderWrite(db, statement)
	if err != nil {
		return zero, err
	}
	return exactlyOne(scanTypedRendered[T](ctx, db, rendered))
}

// Update encodes value's rasql-tagged fields and updates its table row.
// It matches primary-key fields and updates every non-primary-key column.
// Value must have one exported tagged field for every table column,
// or implement ColumnValuer.
func Update[T any](ctx context.Context, db DB, table Table[T], value T) (sql.Result, error) {
	return UpdateWithOptions(ctx, db, table, value)
}

// UpdateWithOptions encodes a typed update and executes it. Without options it
// has the same primary-key behavior as Update. UpdateColumns selects a subset
// of non-primary-key columns and permits a partial row value. UpdateWhere
// supplies a predicate, which makes the operation a bulk update; when it is
// supplied without UpdateColumns, all non-primary-key columns are assigned.
// Values are always passed to the query builder as bound parameters.
func UpdateWithOptions[T any](ctx context.Context, db DB, table Table[T], value T, options ...UpdateOption) (sql.Result, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	config, err := updateOptions(options)
	if err != nil {
		return nil, fmt.Errorf("rasql: configure UPDATE: %w", err)
	}
	statement, err := typedUpdateWithOptions(table, value, config)
	if err != nil {
		return nil, fmt.Errorf("rasql: build UPDATE: %w", err)
	}
	return Exec(ctx, db, statement)
}

// UpdateMany applies one set of typed values to every row matched by the
// explicit UpdateWhere option. It rejects calls without UpdateWhere so a bulk
// operation cannot silently fall back to a primary-key update.
func UpdateMany[T any](ctx context.Context, db DB, table Table[T], value T, options ...UpdateOption) (sql.Result, error) {
	if err := db.Validate(); err != nil {
		return nil, err
	}
	config, err := updateOptions(options)
	if err != nil {
		return nil, fmt.Errorf("rasql: configure UPDATE: %w", err)
	}
	if !config.whereSet {
		return nil, fmt.Errorf("rasql: build UPDATE: bulk update requires an explicit UpdateWhere predicate")
	}
	statement, err := typedUpdateWithOptions(table, value, config)
	if err != nil {
		return nil, fmt.Errorf("rasql: build UPDATE: %w", err)
	}
	return Exec(ctx, db, statement)
}

// UpdateOption configures UpdateWithOptions.
type UpdateOption interface {
	applyUpdate(*updateConfig) error
}

type updateConfig struct {
	columns    []string
	columnsSet bool
	where      query.Expression
	whereSet   bool
}

type updateColumnsOption []string

// UpdateColumns selects the non-primary-key columns assigned by
// UpdateWithOptions. It permits a row value that maps only those columns and,
// when UpdateWhere is absent, the table's primary-key columns.
func UpdateColumns(columns ...string) UpdateOption {
	return updateColumnsOption(append([]string(nil), columns...))
}

func (columns updateColumnsOption) applyUpdate(config *updateConfig) error {
	if config.columnsSet {
		return fmt.Errorf("update columns are selected more than once")
	}
	if len(columns) == 0 {
		return fmt.Errorf("update columns must not be empty")
	}
	config.columns = append([]string(nil), columns...)
	config.columnsSet = true
	return nil
}

type updateWhereOption struct {
	expression query.Expression
}

// UpdateWhere supplies the predicate for UpdateWithOptions. A predicate is
// required for a bulk update because this typed helper never creates an
// unconditional UPDATE.
func UpdateWhere(expression query.Expression) UpdateOption {
	return updateWhereOption{expression: expression}
}

func (option updateWhereOption) applyUpdate(config *updateConfig) error {
	if config.whereSet {
		return fmt.Errorf("update predicate is selected more than once")
	}
	if nilcheck.Is(option.expression) {
		return fmt.Errorf("update predicate must not be nil")
	}
	config.where = option.expression
	config.whereSet = true
	return nil
}

func updateOptions(options []UpdateOption) (updateConfig, error) {
	var config updateConfig
	for _, option := range options {
		if nilcheck.Is(option) {
			return updateConfig{}, fmt.Errorf("update option must not be nil")
		}
		if err := option.applyUpdate(&config); err != nil {
			return updateConfig{}, err
		}
	}
	return config, nil
}

func typedInsertMany[T any](table Table[T], values []T, defaultColumns map[string]struct{}) (query.Insert, error) {
	if len(values) == 0 {
		return query.Insert{}, fmt.Errorf("rows must not be empty")
	}

	reference, fields, err := typedRowFields(table, values[0])
	if err != nil {
		return query.Insert{}, err
	}
	definition := reference.Definition()
	for name := range defaultColumns {
		if _, exists := definition.Column(name); !exists {
			return query.Insert{}, fmt.Errorf("table %q has no column %q selected for a database default", definition.QualifiedName(), name)
		}
	}
	columns := make([]query.ColumnRef, 0, len(definition.Columns)-len(defaultColumns))
	for _, definitionColumn := range definition.Columns {
		if _, useDefault := defaultColumns[definitionColumn.Name]; useDefault {
			continue
		}
		// A generated column cannot be written to at all: the database
		// computes it itself, and rejects a statement that names it
		// explicitly. It is excluded from the default column list the same
		// way a caller-selected DefaultColumns entry is, rather than only
		// on request, because there is no statement a generated column
		// could ever legitimately appear in here.
		if definitionColumn.GeneratedExpression != "" {
			continue
		}
		// An ALWAYS identity column cannot be written to either: PostgreSQL
		// rejects an explicit value for one ("cannot insert a non-DEFAULT
		// value"). A BY DEFAULT identity column stays in the list, since it
		// accepts an explicit value; a caller who wants the sequence to
		// supply one names it in DefaultColumns instead, the same way they
		// already do for a BIGSERIAL column.
		if definitionColumn.Identity == schema.IdentityAlways {
			continue
		}
		columns = append(columns, reference.Column(definitionColumn.Name))
	}
	if len(columns) == 0 {
		for i := 1; i < len(values); i++ {
			if _, _, err := typedRowFields(table, values[i]); err != nil {
				return query.Insert{}, fmt.Errorf("row %d: %w", i, err)
			}
		}
		return query.NewDefaultInsert(reference)
	}

	rows := make([][]any, len(values))
	rows[0] = typedInsertValues(columns, fields)
	for i := 1; i < len(values); i++ {
		_, fields, err = typedRowFields(table, values[i])
		if err != nil {
			return query.Insert{}, fmt.Errorf("row %d: %w", i, err)
		}
		rows[i] = typedInsertValues(columns, fields)
	}
	return query.NewInsertRows(reference, columns, rows)
}

func typedInsertValues(columns []query.ColumnRef, fields map[string]any) []any {
	values := make([]any, 0, len(columns))
	for _, column := range columns {
		values = append(values, fields[column.Name()])
	}
	return values
}

type defaultInsertResults []sql.Result

func (r defaultInsertResults) LastInsertId() (int64, error) {
	return r[len(r)-1].LastInsertId()
}

func (r defaultInsertResults) RowsAffected() (int64, error) {
	var total int64
	for _, result := range r {
		rows, err := result.RowsAffected()
		if err != nil {
			return 0, err
		}
		total += rows
	}
	return total, nil
}

func execDefaultInserts(ctx context.Context, db DB, statement query.Insert, count int) (sql.Result, error) {
	results := make(defaultInsertResults, count)
	for i := range count {
		result, err := Exec(ctx, db, statement)
		if err != nil {
			return nil, fmt.Errorf("rasql: execute default INSERT row %d: %w", i, err)
		}
		results[i] = result
	}
	return results, nil
}

func typedInsert[T any](table Table[T], value T, defaultColumns map[string]struct{}) (query.Insert, error) {
	return typedInsertMany(table, []T{value}, defaultColumns)
}

func typedUpdateWithOptions[T any](table Table[T], value T, config updateConfig) (query.Update, error) {
	reference, err := typedTableReference(table)
	if err != nil {
		return query.Update{}, err
	}
	definition := reference.Definition()
	primaryKeys := make(map[string]struct{}, len(definition.PrimaryKey))
	for _, name := range definition.PrimaryKey {
		primaryKeys[name] = struct{}{}
	}

	selected := config.columns
	if config.columnsSet {
		seen := make(map[string]struct{}, len(selected))
		for _, name := range selected {
			if _, exists := seen[name]; exists {
				return query.Update{}, fmt.Errorf("update column %q is selected more than once", name)
			}
			seen[name] = struct{}{}
			selectedColumn, exists := definition.Column(name)
			if !exists {
				return query.Update{}, fmt.Errorf("table %q has no column %q selected for update", definition.QualifiedName(), name)
			}
			if _, isPrimaryKey := primaryKeys[name]; isPrimaryKey {
				return query.Update{}, fmt.Errorf("column %q is a primary key and cannot be updated", name)
			}
			// A generated column cannot be written to at all, so naming
			// one explicitly is refused the same way naming a primary key
			// is, rather than left to fail against the database once the
			// statement reaches it.
			if selectedColumn.GeneratedExpression != "" {
				return query.Update{}, fmt.Errorf("column %q is generated and cannot be updated", name)
			}
			// An ALWAYS identity column is refused the same way: PostgreSQL
			// rejects an UPDATE naming one ("can only be updated to
			// DEFAULT"). A BY DEFAULT identity column is left untouched
			// here, since it is updatable like any other column.
			if selectedColumn.Identity == schema.IdentityAlways {
				return query.Update{}, fmt.Errorf("column %q is an ALWAYS identity column and cannot be updated", name)
			}
		}
	} else {
		selected = make([]string, 0, len(definition.Columns))
		for _, column := range definition.Columns {
			if _, isPrimaryKey := primaryKeys[column.Name]; isPrimaryKey {
				continue
			}
			// See the matching comment in typedInsertMany: a generated
			// column never belongs in a write statement, so it is excluded
			// from the default selection the same way a primary key is.
			if column.GeneratedExpression != "" {
				continue
			}
			// See the matching comment above: an ALWAYS identity column is
			// skipped from the default assignment list the same way.
			if column.Identity == schema.IdentityAlways {
				continue
			}
			selected = append(selected, column.Name)
		}
	}

	var fields map[string]any
	if config.columnsSet {
		required := append([]string(nil), selected...)
		if !config.whereSet {
			if len(primaryKeys) == 0 {
				return query.Update{}, fmt.Errorf("table %q has no primary key", definition.QualifiedName())
			}
			required = append(required, definition.PrimaryKey...)
		}
		_, fields, err = typedRowFieldsForColumns(table, value, required)
	} else {
		_, fields, err = typedRowFields(table, value)
	}
	if err != nil {
		return query.Update{}, err
	}

	assignments := make([]query.Assignment, 0, len(selected))
	for _, name := range selected {
		column := reference.Column(name)
		assignments = append(assignments, query.Set(column, fields[name]))
	}
	if len(assignments) == 0 {
		return query.Update{}, fmt.Errorf("table %q has no non-primary-key columns", definition.QualifiedName())
	}

	statement, err := query.NewUpdate(reference, assignments...)
	if err != nil {
		return query.Update{}, err
	}
	if config.whereSet {
		return statement.WithWhere(config.where)
	}
	if len(primaryKeys) == 0 {
		return query.Update{}, fmt.Errorf("table %q has no primary key", definition.QualifiedName())
	}

	predicates := make([]query.Expression, 0, len(definition.PrimaryKey))
	for _, name := range definition.PrimaryKey {
		column := reference.Column(name)
		predicates = append(predicates, query.Equal(column, fields[name]))
	}
	if len(predicates) == 1 {
		return statement.WithWhere(predicates[0])
	}
	return statement.WithWhere(query.And(predicates...))
}

func typedTableReference[T any](table Table[T]) (query.TableRef, error) {
	if isNilTable(table) {
		return query.TableRef{}, fmt.Errorf("table must not be nil")
	}
	reference := table.Ref()
	definition := reference.Definition()
	if err := definition.Validate(); err != nil {
		return query.TableRef{}, fmt.Errorf("table reference: %w", err)
	}
	return reference, nil
}

func typedRowFields[T any](table Table[T], value T) (query.TableRef, map[string]any, error) {
	reference, err := typedTableReference(table)
	if err != nil {
		return query.TableRef{}, nil, err
	}
	definition := reference.Definition()

	record := reflect.ValueOf(value)
	for record.Kind() == reflect.Pointer {
		if record.IsNil() {
			return query.TableRef{}, nil, fmt.Errorf("row value must not be nil")
		}
		record = record.Elem()
	}

	// A row type that states its own mapping needs no tags, so it is asked for
	// each column of the definition and nothing is read by reflection. A
	// ColumnValue promoted from an embedded field is not that statement when the
	// row type tags fields of its own, so the tags are read instead.
	// A ColumnValue the row type declares itself is always that statement.
	if valuer, ok := columnValuer(value); ok {
		shadowed, err := tagsShadowEmbeddedValuer(record)
		if err != nil {
			return query.TableRef{}, nil, err
		}
		if !shadowed {
			fields := make(map[string]any, len(definition.Columns))
			for _, definitionColumn := range definition.Columns {
				columnValue, ok := valuer.ColumnValue(definitionColumn.Name)
				if !ok {
					return query.TableRef{}, nil, fmt.Errorf("row value %T supplies no value for column %q", value, definitionColumn.Name)
				}
				fields[definitionColumn.Name] = columnValue
			}
			return reference, fields, nil
		}
	}

	if record.Kind() != reflect.Struct {
		return query.TableRef{}, nil, fmt.Errorf("row value %T must be a struct", value)
	}

	fields := make(map[string]any, record.NumField())
	recordType := record.Type()
	for index := range record.NumField() {
		field := recordType.Field(index)
		columnName, ok := field.Tag.Lookup("rasql")
		if !ok || columnName == "-" {
			continue
		}
		columnName, _, _ = strings.Cut(columnName, ",")
		if columnName == "" {
			return query.TableRef{}, nil, fmt.Errorf("field %s has an empty rasql column name", field.Name)
		}
		if field.PkgPath != "" {
			return query.TableRef{}, nil, fmt.Errorf("field %s for column %q is not exported", field.Name, columnName)
		}
		if _, exists := fields[columnName]; exists {
			return query.TableRef{}, nil, fmt.Errorf("multiple fields are tagged for column %q", columnName)
		}
		if _, exists := definition.Column(columnName); !exists {
			return query.TableRef{}, nil, fmt.Errorf("field %s references unknown column %q", field.Name, columnName)
		}
		fields[columnName] = record.Field(index).Interface()
	}
	for _, definitionColumn := range definition.Columns {
		if _, ok := fields[definitionColumn.Name]; !ok {
			return query.TableRef{}, nil, fmt.Errorf("row value has no field tagged for column %q", definitionColumn.Name)
		}
	}
	return reference, fields, nil
}

func typedRowFieldsForColumns[T any](table Table[T], value T, columns []string) (query.TableRef, map[string]any, error) {
	reference, err := typedTableReference(table)
	if err != nil {
		return query.TableRef{}, nil, err
	}
	definition := reference.Definition()
	required := make(map[string]struct{}, len(columns))
	for _, name := range columns {
		if _, exists := definition.Column(name); !exists {
			return query.TableRef{}, nil, fmt.Errorf("table %q has no column %q selected for update", definition.QualifiedName(), name)
		}
		required[name] = struct{}{}
	}

	record := reflect.ValueOf(value)
	for record.Kind() == reflect.Pointer {
		if record.IsNil() {
			return query.TableRef{}, nil, fmt.Errorf("row value must not be nil")
		}
		record = record.Elem()
	}
	if valuer, ok := columnValuer(value); ok {
		shadowed, err := tagsShadowEmbeddedValuer(record)
		if err != nil {
			return query.TableRef{}, nil, err
		}
		if !shadowed {
			fields := make(map[string]any, len(required))
			for name := range required {
				columnValue, ok := valuer.ColumnValue(name)
				if !ok {
					return query.TableRef{}, nil, fmt.Errorf("row value %T supplies no value for column %q", value, name)
				}
				fields[name] = columnValue
			}
			return reference, fields, nil
		}
	}

	if record.Kind() != reflect.Struct {
		return query.TableRef{}, nil, fmt.Errorf("row value %T must be a struct", value)
	}

	fields := make(map[string]any, len(required))
	seen := make(map[string]struct{}, record.NumField())
	recordType := record.Type()
	for index := range record.NumField() {
		field := recordType.Field(index)
		columnName, ok := field.Tag.Lookup("rasql")
		if !ok || columnName == "-" {
			continue
		}
		columnName, _, _ = strings.Cut(columnName, ",")
		if columnName == "" {
			return query.TableRef{}, nil, fmt.Errorf("field %s has an empty rasql column name", field.Name)
		}
		if field.PkgPath != "" {
			return query.TableRef{}, nil, fmt.Errorf("field %s for column %q is not exported", field.Name, columnName)
		}
		if _, exists := seen[columnName]; exists {
			return query.TableRef{}, nil, fmt.Errorf("multiple fields are tagged for column %q", columnName)
		}
		seen[columnName] = struct{}{}
		if _, exists := definition.Column(columnName); !exists {
			return query.TableRef{}, nil, fmt.Errorf("field %s references unknown column %q", field.Name, columnName)
		}
		if _, needed := required[columnName]; needed {
			fields[columnName] = record.Field(index).Interface()
		}
	}
	for name := range required {
		if _, ok := fields[name]; !ok {
			return query.TableRef{}, nil, fmt.Errorf("row value has no field tagged for column %q", name)
		}
	}
	return reference, fields, nil
}

// columnValuer reports whether value or its address supplies its own column
// values. Taking the address as well means a value receiver and a pointer
// receiver both match.
func columnValuer[T any](value T) (ColumnValuer, bool) {
	if valuer, ok := any(value).(ColumnValuer); ok {
		return valuer, true
	}
	valuer, ok := any(&value).(ColumnValuer)
	return valuer, ok
}

var columnValuerType = reflect.TypeFor[ColumnValuer]()

// tagsShadowEmbeddedValuer reports whether record embeds a ColumnValuer, tags
// fields of its own, and declares no ColumnValue of its own. An interface
// assertion cannot tell a promoted ColumnValue from one the row type declares,
// and a promoted one maps only the embedded fields, so such a row type is mapped
// by its tags. A row type that declares ColumnValue itself keeps the
// ColumnValuer path, because that method is the mapping it states and Go
// dispatches to it.
func tagsShadowEmbeddedValuer(record reflect.Value) (bool, error) {
	if record.Kind() != reflect.Struct {
		return false, nil
	}
	recordType := record.Type()
	embedded := false
	tagged := false
	for index := range recordType.NumField() {
		field := recordType.Field(index)
		if columnName, ok := field.Tag.Lookup("rasql"); ok && columnName != "-" {
			tagged = true
		}
		if field.Anonymous && implementsColumnValuer(field.Type) {
			embedded = true
		}
	}
	if !embedded || !tagged {
		return false, nil
	}
	declared, err := method.Declared(recordType, "ColumnValue")
	if err != nil {
		return false, err
	}
	return !declared, nil
}

// implementsColumnValuer reports whether fieldType or its pointer supplies its
// own column values, so an embedded value and an embedded pointer both count.
func implementsColumnValuer(fieldType reflect.Type) bool {
	if fieldType.Implements(columnValuerType) {
		return true
	}
	return reflect.PointerTo(fieldType).Implements(columnValuerType)
}
