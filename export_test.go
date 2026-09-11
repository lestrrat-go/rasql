package rasql

import (
	"context"
	"iter"

	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/stmt"
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

// Q1CompilerFor wraps a compiler built directly from a profile and dialect,
// including pairings EngineProfile.Compiler refuses, such as a custom profile
// speaking a standard dialect. A test that checks what the compiler does with
// such a pairing has no other way to build one.
func Q1CompilerFor(c *querycompile.Compiler) Compiler { return Compiler{compiler: c} }

// Q1Prepared carries a prepared row sequence without naming the type that
// holds it, which stays unexported.
type Q1Prepared[R any] struct{ prepared preparedRows[R] }

// Q1PrepareRows prepares q against a compiled query the caller supplies, so a
// test can hand in a compiled form that no query would produce, such as one
// whose bind copier fails or whose codec is missing. Every public entry point
// compiles the query itself, leaving no way to inject one.
func Q1PrepareRows[R any](executor Executor, q Query[R], compiled bindplan.Compiled) (Q1Prepared[R], error) {
	prepared, err := prepareRows(executor, q, compiled)
	return Q1Prepared[R]{prepared: prepared}, err
}

// Q1Rows consumes what Q1PrepareRows produced.
func Q1Rows[R any](ctx context.Context, executor Executor, prepared Q1Prepared[R]) (iter.Seq2[R, error], error) {
	return rowsPrepared(ctx, executor, prepared.prepared)
}

// Q1PreparedPage carries a prepared page without naming the type that holds
// it, which stays unexported.
type Q1PreparedPage[R any] struct{ page preparedPage[R] }

// Q1PreparePageAfter plans a page without running it. PageAfter plans and runs
// in one call, so a test that wants to see the plan, or to watch each row
// arrive through the callback below, has no other way in.
func Q1PreparePageAfter[R any](executor Executor, q Query[R], spec PageSpec[R], policy PagePolicy, request PageRequest) (Q1PreparedPage[R], error) {
	page, err := preparePageAfter(executor, q, spec, policy, request)
	return Q1PreparedPage[R]{page: page}, err
}

// Q1ConsumePreparedPage runs a prepared page, reporting each row and whether
// it was kept. PageAfter returns only the finished page, so the per-row flag
// is visible nowhere else.
func Q1ConsumePreparedPage[R any](ctx context.Context, executor Executor, prepared Q1PreparedPage[R], callback func(R, bool) error) (Page[R], error) {
	return consumePreparedPage(ctx, executor, prepared.page, callback)
}

// Q1NativeStatement builds the statement a native query plan carries, with its
// bind tokens still in place. Native wraps the plan in a Query and hands back
// nothing that shows the tokens, so a test checking how a native argument was
// bound comes through here.
func Q1NativeStatement(statement NativeStatement) (stmt.Statement, error) {
	plan, err := newNativeQueryPlan(statement)
	if err != nil {
		return stmt.Statement{}, err
	}
	return plan.statement, nil
}

// Q1PlanSources returns the relations a query reads. Query.Plan hands back a
// QueryPlan whose sources stay unexported, because a caller composes queries
// from sources rather than taking them back out.
func Q1PlanSources(plan QueryPlan) []Source { return plan.sources }

// Q1SourceColumns returns the columns a source declares.
func Q1SourceColumns(source Source) []query.ResultColumn { return source.ref.Columns() }

// Q1ProjectionExpressions returns the expression behind each projected column,
// which is empty for a native projection because its columns come from the SQL
// rather than from anything this package built.
func Q1ProjectionExpressions[R any](projection Projection[R]) []query.Expression {
	result := make([]query.Expression, len(projection.items))
	for i, item := range projection.items {
		result[i] = item.expression
	}
	return result
}

// Q1Presence builds a presence without checking it, so a test can hand a
// decoder one that NewPresence would refuse and see where the refusal lands.
func Q1Presence(component string, columns ...string) Presence {
	return Presence{component: component, columns: columns}
}
