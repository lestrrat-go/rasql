package rasql

import (
	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/query"
)

// This file compiles only under `go test`, so what it exports is reachable
// from the external test package and from nowhere else. Reach for it last.
// An observable a caller already has is better, a public API that earns its
// place for users is next, and moving the code into internal/ is next again.
// What is left here could not be had any of those ways.
//
// Expr carries no methods on purpose, because its type parameter is what stops
// a caller mixing up column types, and handing out the expression inside would
// let anyone go around that. So a test that needs to see a bound value, or the
// bind metadata a compiled query holds, has to come through here.

// Q1BindArgument returns the value bound into expression.
func Q1BindArgument[T any](expression Expr[T]) any {
	value, ok := expression.node.(query.Value)
	if !ok {
		return nil
	}
	argument := value.Argument()
	if token, ok := argument.(bindToken); ok {
		return token.Value
	}
	return argument
}

// Q1BindToken returns the whole bind token expression carries, which a test
// reads to check that two binds of the same value share one identity.
func Q1BindToken[T any](expression Expr[T]) bindplan.Token {
	value, ok := expression.node.(query.Value)
	if !ok {
		return bindplan.Token{}
	}
	token, _ := value.Argument().(bindplan.Token)
	return token
}

// Q1CompileQuery returns the compiled form of q, including the bind slots and
// copiers that CompileQuery keeps to itself. A test uses it to check that a
// compiled query's arguments, slots and copiers stay in step across query
// shapes, which is not visible from the rendered statement alone.
func Q1CompileQuery[R any](c Compiler, q Query[R]) (bindplan.Compiled, error) {
	return compileQuery(c.compiler, q)
}

// Q1PartitionLimitToken returns the bind token a partition limit carries. A
// test reads it to check the limit binds once and keeps one identity across
// repeated compiles, which the rendered statement alone does not show.
func Q1PartitionLimitToken[R any](q Query[R]) (bindplan.Token, bool) {
	node, ok := q.plan.partitionLimitValue.node.(interface{ Argument() any })
	if !ok {
		return bindplan.Token{}, false
	}
	token, ok := node.Argument().(bindplan.Token)
	return token, ok
}

// Q1WithPartitionLimit applies the per-parent limit a graph edge applies to a
// child query. It stays unexported in the package because it shapes a query
// mid-load rather than being something a caller composes, so a test that needs
// such a query has to build one through here.
func Q1WithPartitionLimit[R any](q Query[R], partition []GroupKey, order []OrderTerm, limit int) (Query[R], error) {
	return q.withPartitionLimit(partition, order, limit)
}
