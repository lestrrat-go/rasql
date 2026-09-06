package postgresql

import (
	"reflect"

	pgquery "github.com/lestrrat-go/rasql-pg/query"
)

func cloneTableDefinition(table tableDefinition) tableDefinition {
	return tableDefinition{source: table.source, statement: cloneCreateTableStatement(table.statement), identities: cloneIdentityModes(table.identities), foreignKeys: cloneForeignKeyActions(table.foreignKeys)}
}

func cloneCreateTableStatement(statement *pgquery.CreateTableStatement) *pgquery.CreateTableStatement {
	if statement == nil {
		return nil
	}
	return deepClone(reflect.ValueOf(statement)).Interface().(*pgquery.CreateTableStatement)
}

func deepClone(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		cloned := deepClone(value.Elem())
		result := reflect.New(value.Type()).Elem()
		result.Set(cloned)
		return result
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.New(value.Type().Elem())
		result.Elem().Set(deepClone(value.Elem()))
		return result
	case reflect.Struct:
		result := reflect.New(value.Type()).Elem()
		for index := 0; index < value.NumField(); index++ {
			if result.Field(index).CanSet() {
				result.Field(index).Set(deepClone(value.Field(index)))
			}
		}
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for index := 0; index < value.Len(); index++ {
			result.Index(index).Set(deepClone(value.Index(index)))
		}
		return result
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			result.SetMapIndex(deepClone(iter.Key()), deepClone(iter.Value()))
		}
		return result
	default:
		return value
	}
}

func equalIdentityFacts(left, right map[string]identityMode) bool {
	return reflect.DeepEqual(left, right)
}
func equalForeignKeyActions(left, right map[foreignKeyKey]foreignKeyActions) bool {
	return reflect.DeepEqual(left, right)
}

var _ = equalIdentityFacts
var _ = equalForeignKeyActions
