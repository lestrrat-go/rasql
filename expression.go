package rasql

import (
	"reflect"
	"sync/atomic"

	"github.com/lestrrat-go/rasql/query"
)

type Expr[T any] struct {
	node  query.Expression
	codec string
}
type NullExpr[T any] struct {
	node  query.Expression
	codec string
}
type Predicate struct{ node query.Expression }
type Column[Row, T any] struct {
	ref   query.ColumnRef
	codec string
}
type NullColumn[Row, T any] struct {
	ref   query.ColumnRef
	codec string
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
	return Expr[T]{node: query.Bind(bindToken{id: id, value: cloneBindValue(value)})}
}
func ValueWithCodec[T any](value T, codec string) (Expr[T], error) {
	if codec != "" && !codecPattern.MatchString(codec) {
		return Expr[T]{}, planError("invalid_schema", "codec", "malformed codec identifier")
	}
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	return Expr[T]{node: query.Bind(bindToken{id: id, value: cloneBindValue(value), codec: codec}), codec: codec}, nil
}
func EqualExpr[T comparable](left, right Expr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node)}
}
func EqualValue[T comparable](left Expr[T], right T) Predicate {
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	return Predicate{node: query.Equal(left.node, query.Bind(bindToken{id: id, value: cloneBindValue(right), codec: left.codec}))}
}
func EqualNullable[T comparable](left, right NullExpr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node)}
}
func EqualOptional[T comparable](left Expr[T], right NullExpr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node)}
}
func IsNull[T any](value NullExpr[T]) Predicate { return Predicate{node: query.IsNull(value.node)} }
func IsNotNull[T any](value NullExpr[T]) Predicate {
	return Predicate{node: query.IsNotNull(value.node)}
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

func (c Column[Row, T]) Expr() Expr[T]                          { return Expr[T]{node: c.ref, codec: c.codec} }
func (c NullColumn[Row, T]) NullExpr() NullExpr[T]              { return NullExpr[T]{node: c.ref, codec: c.codec} }
func NullColumnOf[Row, T any](ref ColumnRef) NullColumn[Row, T] { return NullColumn[Row, T]{ref: ref} }

type GroupKey struct{ node query.Expression }

func Group[T any](value Expr[T]) GroupKey         { return GroupKey{node: value.node} }
func GroupNull[T any](value NullExpr[T]) GroupKey { return GroupKey{node: value.node} }

type NullOrder uint8

const (
	NullOrderDefault NullOrder = iota
	NullsFirst
	NullsLast
)

type OrderTerm struct {
	node       query.Expression
	descending bool
	nulls      NullOrder
}

func AscExpr[T any](value Expr[T]) OrderTerm  { return OrderTerm{node: value.node} }
func DescExpr[T any](value Expr[T]) OrderTerm { return OrderTerm{node: value.node, descending: true} }
func AscNull[T any](value NullExpr[T], nulls NullOrder) OrderTerm {
	return OrderTerm{node: value.node, nulls: nulls}
}
func DescNull[T any](value NullExpr[T], nulls NullOrder) OrderTerm {
	return OrderTerm{node: value.node, descending: true, nulls: nulls}
}

func cloneBindValue[T any](value T) any {
	cloned := cloneReflectValue(reflect.ValueOf(value))
	if !cloned.IsValid() {
		return nil
	}
	return cloned.Interface()
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
	default:
		return value
	}
}
