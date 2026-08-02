package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"github.com/lestrrat-go/rasql/query"
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
// or implement ColumnValuer. DefaultColumns omits named columns so the
// database applies their defaults.
func Insert[T any](ctx context.Context, client Client, table Table[T], value T, options ...InsertOption) (sql.Result, error) {
	defaults, err := insertDefaults(options)
	if err != nil {
		return nil, fmt.Errorf("rasql: configure INSERT: %w", err)
	}
	statement, err := typedInsert(table, value, defaults)
	if err != nil {
		return nil, fmt.Errorf("rasql: build INSERT: %w", err)
	}
	return client.Exec(ctx, statement)
}

// InsertOption configures Insert.
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

// Update encodes value's rasql-tagged fields and updates its table row.
// It matches primary-key fields and updates every non-primary-key column.
// Value must have one exported tagged field for every table column,
// or implement ColumnValuer.
func Update[T any](ctx context.Context, client Client, table Table[T], value T) (sql.Result, error) {
	statement, err := typedUpdate(table, value)
	if err != nil {
		return nil, fmt.Errorf("rasql: build UPDATE: %w", err)
	}
	return client.Exec(ctx, statement)
}

func typedInsert[T any](table Table[T], value T, defaultColumns map[string]struct{}) (query.Insert, error) {
	reference, fields, err := typedRowFields(table, value)
	if err != nil {
		return query.Insert{}, err
	}
	definition := reference.Definition()
	for name := range defaultColumns {
		if _, exists := definition.Column(name); !exists {
			return query.Insert{}, fmt.Errorf("table %q has no column %q selected for a database default", definition.Name, name)
		}
	}

	columns := make([]query.Column, 0, len(definition.Columns)-len(defaultColumns))
	values := make([]query.Expression, 0, len(definition.Columns)-len(defaultColumns))
	for _, definitionColumn := range definition.Columns {
		if _, useDefault := defaultColumns[definitionColumn.Name]; useDefault {
			continue
		}
		column, err := reference.Column(definitionColumn.Name)
		if err != nil {
			return query.Insert{}, err
		}
		columns = append(columns, column)
		values = append(values, query.Bind(fields[definitionColumn.Name]))
	}
	if len(columns) == 0 {
		return query.Insert{}, fmt.Errorf("table %q has no columns to insert after selecting database defaults", definition.Name)
	}
	return query.NewInsert(reference, columns, values)
}

func typedUpdate[T any](table Table[T], value T) (query.Update, error) {
	reference, fields, err := typedRowFields(table, value)
	if err != nil {
		return query.Update{}, err
	}
	definition := reference.Definition()
	if len(definition.PrimaryKey) == 0 {
		return query.Update{}, fmt.Errorf("table %q has no primary key", definition.Name)
	}

	primaryKeys := make(map[string]struct{}, len(definition.PrimaryKey))
	for _, name := range definition.PrimaryKey {
		primaryKeys[name] = struct{}{}
	}
	assignments := make([]query.Assignment, 0, len(definition.Columns)-len(primaryKeys))
	predicates := make([]query.Expression, 0, len(primaryKeys))
	for _, definitionColumn := range definition.Columns {
		column, err := reference.Column(definitionColumn.Name)
		if err != nil {
			return query.Update{}, err
		}
		field := fields[definitionColumn.Name]
		if _, primaryKey := primaryKeys[definitionColumn.Name]; primaryKey {
			predicates = append(predicates, query.Equal(column, query.Bind(field)))
			continue
		}
		assignments = append(assignments, query.Set(column, query.Bind(field)))
	}
	if len(assignments) == 0 {
		return query.Update{}, fmt.Errorf("table %q has no non-primary-key columns", definition.Name)
	}

	statement, err := query.NewUpdate(reference, assignments...)
	if err != nil {
		return query.Update{}, err
	}
	if len(predicates) == 1 {
		return statement.WithWhere(predicates[0])
	}
	return statement.WithWhere(query.And(predicates...))
}

func typedRowFields[T any](table Table[T], value T) (query.Table, map[string]any, error) {
	reference := table.QueryTable()
	definition := reference.Definition()
	if err := definition.Validate(); err != nil {
		return query.Table{}, nil, fmt.Errorf("table reference: %w", err)
	}

	record := reflect.ValueOf(value)
	for record.Kind() == reflect.Pointer {
		if record.IsNil() {
			return query.Table{}, nil, fmt.Errorf("row value must not be nil")
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
			return query.Table{}, nil, err
		}
		if !shadowed {
			fields := make(map[string]any, len(definition.Columns))
			for _, definitionColumn := range definition.Columns {
				columnValue, ok := valuer.ColumnValue(definitionColumn.Name)
				if !ok {
					return query.Table{}, nil, fmt.Errorf("row value %T supplies no value for column %q", value, definitionColumn.Name)
				}
				fields[definitionColumn.Name] = columnValue
			}
			return reference, fields, nil
		}
	}

	if record.Kind() != reflect.Struct {
		return query.Table{}, nil, fmt.Errorf("row value %T must be a struct", value)
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
			return query.Table{}, nil, fmt.Errorf("field %s has an empty rasql column name", field.Name)
		}
		if field.PkgPath != "" {
			return query.Table{}, nil, fmt.Errorf("field %s for column %q is not exported", field.Name, columnName)
		}
		if _, exists := fields[columnName]; exists {
			return query.Table{}, nil, fmt.Errorf("multiple fields are tagged for column %q", columnName)
		}
		if _, exists := definition.Column(columnName); !exists {
			return query.Table{}, nil, fmt.Errorf("field %s references unknown column %q", field.Name, columnName)
		}
		fields[columnName] = record.Field(index).Interface()
	}
	for _, definitionColumn := range definition.Columns {
		if _, ok := fields[definitionColumn.Name]; !ok {
			return query.Table{}, nil, fmt.Errorf("row value has no field tagged for column %q", definitionColumn.Name)
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
	declared, err := declaresMethod(recordType, "ColumnValue")
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
