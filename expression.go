package rasql

import (
	"sync/atomic"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
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

type compiledQuery struct {
	statement stmt.Statement
	bindSlots []bindSlot
}

func unwrapBindTokens(statement stmt.Statement) (compiledQuery, error) {
	arguments := statement.Args()
	slots := make([]bindSlot, len(arguments))
	for i, argument := range arguments {
		token, ok := argument.(bindToken)
		if !ok {
			continue
		}
		arguments[i] = token.value
		slots[i] = bindSlot{id: token.id, codec: token.codec}
	}
	return compiledQuery{statement: stmt.New(sqltext.Text(statement.SQL()), arguments...), bindSlots: slots}, nil
}

func matchBaseOccurrences(base, paged compiledQuery) ([]int, error) {
	result := make([]int, 0, len(base.bindSlots))
	next := 0
	for _, wanted := range base.bindSlots {
		if wanted.id == 0 {
			return nil, planError("uncertain_contract", "binds", "base bind has no logical ID")
		}
		found := false
		for next < len(paged.bindSlots) {
			index := next
			candidate := paged.bindSlots[next]
			next++
			if candidate == wanted {
				result = append(result, index)
				found = true
				break
			}
		}
		if !found {
			return nil, planError("uncertain_contract", "binds", "base bind occurrence is missing or reordered")
		}
	}
	return result, nil
}

func Value[T any](value T) Expr[T] {
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	return Expr[T]{node: query.Bind(bindToken{id: id, value: value})}
}
func ValueWithCodec[T any](value T, codec string) (Expr[T], error) {
	if codec != "" && !codecPattern.MatchString(codec) {
		return Expr[T]{}, planError("invalid_schema", "codec", "malformed codec identifier")
	}
	id := bindID(atomic.AddUint64(&nextBindID, 1))
	return Expr[T]{node: query.Bind(bindToken{id: id, value: value, codec: codec}), codec: codec}, nil
}
func EqualExpr[T comparable](left, right Expr[T]) Predicate {
	return Predicate{node: query.Equal(left.node, right.node)}
}
func EqualValue[T comparable](left Expr[T], right T) Predicate {
	return Predicate{node: query.Equal(left.node, Value(right).node)}
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
