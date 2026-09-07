package rasql

import (
	"database/sql"
	"reflect"
	"sync/atomic"
	"time"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

type Expr[T any] struct {
	node    query.Expression
	codec   string
	source  string
	bindErr error
}
type BindSnapshotter[T any] interface{ SnapshotBind() (T, error) }
type NullExpr[T any] struct {
	node    query.Expression
	codec   string
	source  string
	bindErr error
}
type Predicate struct {
	node    query.Expression
	source  string
	bindErr error
}
type Column[Row, T any] struct {
	ref   query.ColumnRef
	codec string
}
type NullColumn[Row, T any] struct {
	ref   query.ColumnRef
	codec string
}

func BindColumn[Row, T any](relation TypedRelation[Row], name, codec string) (Column[Row, T], error) {
	return bindColumn[Row, T](relation.source, name, codec, false)
}
func BindNullColumn[Row, T any](relation TypedRelation[Row], name, codec string) (NullColumn[Row, T], error) {
	if err := validateBoundColumn(relation.source, name, codec); err != nil {
		return NullColumn[Row, T]{}, err
	}
	for _, column := range relation.source.ref.Columns() {
		if column.Name == name {
			if !column.Nullable {
				return NullColumn[Row, T]{}, planError("invalid_source", "column", "nullability does not match handle")
			}
			return NullColumn[Row, T]{ref: relation.source.ref.Column(name), codec: codec}, nil
		}
	}
	return NullColumn[Row, T]{}, planError("invalid_source", "column", "column is not a member of source")
}
func BindOptionalColumn[Row, T any](relation OptionalRelation[Row], name, codec string) (NullColumn[Row, T], error) {
	if relation.source.ref.QualifiedName() == "" {
		return NullColumn[Row, T]{}, planError("invalid_source", "relation", "source is zero")
	}
	if err := validateBoundColumn(relation.source, name, codec); err != nil {
		return NullColumn[Row, T]{}, err
	}
	return NullColumn[Row, T]{ref: relation.source.ref.Column(name), codec: codec}, nil
}
func bindColumn[Row, T any](relation Source, name, codec string, nullable bool) (Column[Row, T], error) {
	if err := validateBoundColumn(relation, name, codec); err != nil {
		return Column[Row, T]{}, err
	}
	for _, column := range relation.ref.Columns() {
		if column.Name == name {
			if column.Nullable != nullable {
				return Column[Row, T]{}, planError("invalid_source", "column", "nullability does not match handle")
			}
			return Column[Row, T]{ref: relation.ref.Column(name), codec: codec}, nil
		}
	}
	return Column[Row, T]{}, planError("invalid_source", "column", "column is not a member of source")
}
func validateBoundColumn(relation Source, name, codec string) error {
	if relation.ref.QualifiedName() == "" {
		return planError("invalid_source", "relation", "source is zero")
	}
	if err := schema.ValidateIdentifier(name); err != nil {
		return planError("invalid_source", "column", err.Error())
	}
	if codec != "" && !codecPattern.MatchString(codec) {
		return planError("invalid_schema", "codec", "malformed codec identifier")
	}
	for _, column := range relation.ref.Columns() {
		if column.Name == name {
			if err := schema.ValidateColumnType(column.Type); err != nil {
				return planError("invalid_schema", "column.type", err.Error())
			}
			return nil
		}
	}
	return planError("invalid_source", "column", "column is not a member of source")
}

var nextBindID uint64

type bindID uint64
type bindToken struct {
	id    bindID
	value any
	codec string
}
type bindSlot struct {
	id    bindID
	codec string
}

func Value[T any](value T) Expr[T] {
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	snapshot, err := snapshotBind(value)
	return Expr[T]{node: query.Bind(bindToken{id: id, value: snapshot}), bindErr: err}
}
func ValueWithCodec[T any](value T, codec string) (Expr[T], error) {
	if codec != "" && !codecPattern.MatchString(codec) {
		return Expr[T]{}, planError("invalid_schema", "codec", "malformed codec identifier")
	}
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	snapshot, err := snapshotBind(value)
	if err != nil {
		return Expr[T]{}, err
	}
	return Expr[T]{node: query.Bind(bindToken{id: id, value: snapshot, codec: codec}), codec: codec}, nil
}
func EqualExpr[T comparable](left, right Expr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node), source: left.source}
}
func EqualValue[T comparable](left Expr[T], right T) Predicate {
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	snapshot, err := snapshotBind(right)
	return Predicate{node: query.Equal(left.node, query.Bind(bindToken{id: id, value: snapshot, codec: left.codec})), source: left.source, bindErr: err}
}
func EqualNullable[T comparable](left, right NullExpr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node), source: left.source}
}
func EqualOptional[T comparable](left Expr[T], right NullExpr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node), source: left.source}
}
func IsNull[T any](value NullExpr[T]) Predicate {
	return Predicate{node: query.IsNull(value.node), source: value.source}
}
func IsNotNull[T any](value NullExpr[T]) Predicate {
	return Predicate{node: query.IsNotNull(value.node), source: value.source}
}
func And(predicates ...Predicate) Predicate {
	nodes := make([]query.Expression, len(predicates))
	for i, p := range predicates {
		nodes[i] = p.node
	}
	return Predicate{node: query.And(nodes...)}
}
func Or(predicates ...Predicate) Predicate {
	nodes := make([]query.Expression, len(predicates))
	for i, p := range predicates {
		nodes[i] = p.node
	}
	return Predicate{node: query.Or(nodes...)}
}
func Not(predicate Predicate) Predicate { return Predicate{node: query.Negate(predicate.node)} }

func (c Column[Row, T]) Expr() Expr[T] {
	return Expr[T]{node: c.ref, codec: c.codec, source: c.ref.Source().QualifiedName()}
}
func (c NullColumn[Row, T]) NullExpr() NullExpr[T] {
	return NullExpr[T]{node: c.ref, codec: c.codec, source: c.ref.Source().QualifiedName()}
}

type GroupKey struct {
	node   query.Expression
	source string
}

func Group[T any](value Expr[T]) GroupKey { return GroupKey{node: value.node, source: value.source} }
func GroupNull[T any](value NullExpr[T]) GroupKey {
	return GroupKey{node: value.node, source: value.source}
}

type NullOrder uint8

const (
	NullOrderDefault NullOrder = iota
	NullsFirst
	NullsLast
)

type OrderTerm struct {
	node       query.Expression
	source     string
	descending bool
	nulls      NullOrder
}

func AscExpr[T any](value Expr[T]) OrderTerm {
	return OrderTerm{node: value.node, source: value.source}
}
func DescExpr[T any](value Expr[T]) OrderTerm {
	return OrderTerm{node: value.node, source: value.source, descending: true}
}
func AscNull[T any](value NullExpr[T], nulls NullOrder) OrderTerm {
	return OrderTerm{node: value.node, source: value.source, nulls: nulls}
}
func DescNull[T any](value NullExpr[T], nulls NullOrder) OrderTerm {
	return OrderTerm{node: value.node, source: value.source, descending: true, nulls: nulls}
}

func CountRows() Expr[int64]                     { return Expr[int64]{node: query.CountAll()} }
func CountExpr[T any](value Expr[T]) Expr[int64] { return Expr[int64]{node: query.Count(value.node)} }
func CountNullExpr[T any](value NullExpr[T]) Expr[int64] {
	return Expr[int64]{node: query.Count(value.node)}
}
func MinExpr[T any](value Expr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Min(value.node), codec: value.codec, source: value.source}
}
func MinNullExpr[T any](value NullExpr[T]) NullExpr[T] {
	return NullExpr[T]{node: query.Min(value.node), codec: value.codec, source: value.source}
}

func cloneBindValue[T any](value T) any {
	cloned := cloneReflectValue(reflect.ValueOf(value))
	if !cloned.IsValid() {
		return nil
	}
	return cloned.Interface()
}

func snapshotBind[T any](value T) (any, error) {
	if snapshotter, ok := any(value).(BindSnapshotter[T]); ok {
		return snapshotter.SnapshotBind()
	}
	return snapshotReflectValue(reflect.ValueOf(value), make(map[uintptr]bool))
}
func snapshotReflectValue(value reflect.Value, active map[uintptr]bool) (any, error) {
	if !value.IsValid() {
		return nil, nil
	}
	if value.Type() == reflect.TypeOf(time.Time{}) {
		return value.Interface(), nil
	}
	if value.Kind() == reflect.Interface {
		if value.IsNil() {
			return nil, nil
		}
		return snapshotReflectValue(value.Elem(), active)
	}
	if value.Kind() == reflect.Pointer || value.Kind() == reflect.Map || value.Kind() == reflect.Slice {
		if value.IsNil() {
			return reflect.Zero(value.Type()).Interface(), nil
		}
		key := value.Pointer()
		if active[key] {
			return nil, planError("unsnapshotable_bind", "bind", "cycle detected")
		}
		active[key] = true
		defer delete(active, key)
	}
	if value.Kind() == reflect.Func || value.Kind() == reflect.Chan || value.Kind() == reflect.UnsafePointer {
		return nil, planError("unsnapshotable_bind", "bind", "mutable value is unsupported")
	}
	switch value.Kind() {
	case reflect.Pointer:
		cloned, err := snapshotReflectValue(value.Elem(), active)
		if err != nil {
			return nil, err
		}
		p := reflect.New(value.Type().Elem())
		p.Elem().Set(reflect.ValueOf(cloned))
		return p.Interface(), nil
	case reflect.Slice:
		result := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			v, err := snapshotReflectValue(value.Index(i), active)
			if err != nil {
				return nil, err
			}
			result.Index(i).Set(reflect.ValueOf(v))
		}
		return result.Interface(), nil
	case reflect.Array:
		result := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			v, err := snapshotReflectValue(value.Index(i), active)
			if err != nil {
				return nil, err
			}
			result.Index(i).Set(reflect.ValueOf(v))
		}
		return result.Interface(), nil
	case reflect.Map:
		result := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			if iter.Key().Kind() == reflect.Pointer || iter.Key().Kind() == reflect.Map || iter.Key().Kind() == reflect.Slice {
				return nil, planError("unsnapshotable_bind", "bind", "mutable map key")
			}
			v, err := snapshotReflectValue(iter.Value(), active)
			if err != nil {
				return nil, err
			}
			result.SetMapIndex(iter.Key(), reflect.ValueOf(v))
		}
		return result.Interface(), nil
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(sql.NamedArg{}) {
			arg := value.Interface().(sql.NamedArg)
			v, err := snapshotReflectValue(reflect.ValueOf(arg.Value), active)
			if err != nil {
				return nil, err
			}
			arg.Value = v
			return arg, nil
		}
		result := reflect.New(value.Type()).Elem()
		result.Set(value)
		for i := 0; i < value.NumField(); i++ {
			field := value.Field(i)
			if value.Type().Field(i).PkgPath != "" {
				return nil, planError("unsnapshotable_bind", "bind", "unexported field")
			}
			v, err := snapshotReflectValue(field, active)
			if err != nil {
				return nil, err
			}
			result.Field(i).Set(reflect.ValueOf(v))
		}
		return result.Interface(), nil
	default:
		return value.Interface(), nil
	}
}
func cloneReflectValue(value reflect.Value) reflect.Value {
	if !value.IsValid() {
		return value
	}
	switch value.Kind() {
	case reflect.Pointer:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.New(value.Type().Elem())
		copy.Elem().Set(cloneReflectValue(value.Elem()))
		return copy
	case reflect.Interface:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := cloneReflectValue(value.Elem())
		result := reflect.New(value.Type()).Elem()
		result.Set(copy)
		return result
	case reflect.Slice:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
		for i := 0; i < value.Len(); i++ {
			copy.Index(i).Set(cloneReflectValue(value.Index(i)))
		}
		return copy
	case reflect.Map:
		if value.IsNil() {
			return reflect.Zero(value.Type())
		}
		copy := reflect.MakeMapWithSize(value.Type(), value.Len())
		iter := value.MapRange()
		for iter.Next() {
			copy.SetMapIndex(cloneReflectValue(iter.Key()), cloneReflectValue(iter.Value()))
		}
		return copy
	case reflect.Array:
		copy := reflect.New(value.Type()).Elem()
		for i := 0; i < value.Len(); i++ {
			copy.Index(i).Set(cloneReflectValue(value.Index(i)))
		}
		return copy
	case reflect.Struct:
		if value.Type() == reflect.TypeOf(sql.NamedArg{}) {
			argument := value.Interface().(sql.NamedArg)
			argument.Value = cloneBindValue(argument.Value)
			return reflect.ValueOf(argument)
		}
		copy := reflect.New(value.Type()).Elem()
		for i := 0; i < value.NumField(); i++ {
			if copy.Field(i).CanSet() && value.Field(i).CanInterface() {
				copy.Field(i).Set(cloneReflectValue(value.Field(i)))
				continue
			}
			copy.Field(i).Set(value.Field(i))
		}
		return copy
	default:
		return value
	}
}
