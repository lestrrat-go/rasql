package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"reflect"
	"strings"

	"github.com/lestrrat-go/rasql/query"
)

// Insert encodes value's rasql-tagged fields and writes it to table.
// Value must have one exported tagged field for every table column.
func Insert[T any](ctx context.Context, client Client, table Table[T], value T) (sql.Result, error) {
	statement, err := typedInsert(table, value)
	if err != nil {
		return nil, fmt.Errorf("rasql: build INSERT: %w", err)
	}
	return client.Exec(ctx, statement)
}

// Update encodes value's rasql-tagged fields and updates its table row.
// It matches primary-key fields and updates every non-primary-key column.
// Value must have one exported tagged field for every table column.
func Update[T any](ctx context.Context, client Client, table Table[T], value T) (sql.Result, error) {
	statement, err := typedUpdate(table, value)
	if err != nil {
		return nil, fmt.Errorf("rasql: build UPDATE: %w", err)
	}
	return client.Exec(ctx, statement)
}

func typedInsert[T any](table Table[T], value T) (query.Insert, error) {
	reference, fields, err := typedRowFields(table, value)
	if err != nil {
		return query.Insert{}, err
	}
	definition := reference.Definition()
	columns := make([]query.Column, len(definition.Columns))
	values := make([]query.Expression, len(definition.Columns))
	for index, definitionColumn := range definition.Columns {
		field := fields[definitionColumn.Name]
		column, err := reference.Column(definitionColumn.Name)
		if err != nil {
			return query.Insert{}, err
		}
		columns[index] = column
		values[index] = query.Bind(field.Interface())
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
			predicates = append(predicates, query.Equal(column, query.Bind(field.Interface())))
			continue
		}
		assignments = append(assignments, query.Set(column, query.Bind(field.Interface())))
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

func typedRowFields[T any](table Table[T], value T) (query.Table, map[string]reflect.Value, error) {
	reference := table.Ref()
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
	if record.Kind() != reflect.Struct {
		return query.Table{}, nil, fmt.Errorf("row value %T must be a struct", value)
	}

	fields := make(map[string]reflect.Value, record.NumField())
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
		fields[columnName] = record.Field(index)
	}
	for _, definitionColumn := range definition.Columns {
		if _, ok := fields[definitionColumn.Name]; !ok {
			return query.Table{}, nil, fmt.Errorf("row value has no field tagged for column %q", definitionColumn.Name)
		}
	}
	return reference, fields, nil
}
