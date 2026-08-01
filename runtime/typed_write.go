package runtime

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
		return nil, fmt.Errorf("runtime: build INSERT: %w", err)
	}
	return client.Exec(ctx, statement)
}

func typedInsert[T any](table Table[T], value T) (query.Insert, error) {
	reference := table.Ref()
	definition := reference.Table()
	if err := definition.Validate(); err != nil {
		return query.Insert{}, fmt.Errorf("table reference: %w", err)
	}

	record := reflect.ValueOf(value)
	for record.Kind() == reflect.Pointer {
		if record.IsNil() {
			return query.Insert{}, fmt.Errorf("row value must not be nil")
		}
		record = record.Elem()
	}
	if record.Kind() != reflect.Struct {
		return query.Insert{}, fmt.Errorf("row value %T must be a struct", value)
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
			return query.Insert{}, fmt.Errorf("field %s has an empty rasql column name", field.Name)
		}
		if field.PkgPath != "" {
			return query.Insert{}, fmt.Errorf("field %s for column %q is not exported", field.Name, columnName)
		}
		if _, exists := fields[columnName]; exists {
			return query.Insert{}, fmt.Errorf("multiple fields are tagged for column %q", columnName)
		}
		if _, exists := definition.Column(columnName); !exists {
			return query.Insert{}, fmt.Errorf("field %s references unknown column %q", field.Name, columnName)
		}
		fields[columnName] = record.Field(index)
	}

	columns := make([]query.Column, len(definition.Columns))
	values := make([]query.Expression, len(definition.Columns))
	for index, definitionColumn := range definition.Columns {
		field, ok := fields[definitionColumn.Name]
		if !ok {
			return query.Insert{}, fmt.Errorf("row value has no field tagged for column %q", definitionColumn.Name)
		}
		column, err := reference.Column(definitionColumn.Name)
		if err != nil {
			return query.Insert{}, err
		}
		columns[index] = column
		values[index] = query.Bind(field.Interface())
	}
	return query.NewInsert(reference, columns, values)
}
