package rasql

import (
	"errors"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/internal/sqlscan"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

// TypedSource is a typed relation produced from a query result.
type TypedSource[R any] struct{ source Source }

func Derive[R any](q Query[R], alias string) (TypedSource[R], error) {
	if err := q.Validate(); err != nil {
		return TypedSource[R]{}, err
	}
	if err := schema.ValidateSimpleIdentifier(alias); err != nil {
		return TypedSource[R]{}, planError("invalid_source", "alias", err.Error())
	}
	result, err := compositionResultQuery(q)
	if err != nil {
		return TypedSource[R]{}, err
	}
	ref, err := query.Derived(result, alias)
	if err != nil {
		return TypedSource[R]{}, planError("invalid_source", "alias", err.Error())
	}
	return TypedSource[R]{source: Source{ref: ref}}, nil
}

func (d TypedSource[R]) Source() Source { return d.source }

type TypedCTE[R any] struct {
	name  string
	query Query[R]
	cte   query.CTE
}

type CTEPlan interface{ ctePlan() query.CTE }

func CTEOf[R any](name string, q Query[R]) (TypedCTE[R], error) {
	if err := schema.ValidateSimpleIdentifier(name); err != nil {
		return TypedCTE[R]{}, planError("invalid_cte", "name", err.Error())
	}
	if err := q.Validate(); err != nil {
		return TypedCTE[R]{}, err
	}
	result, err := compositionResultQuery(q)
	if err != nil {
		return TypedCTE[R]{}, err
	}
	cte, err := query.CommonTable(name, result)
	if err != nil {
		return TypedCTE[R]{}, planError("invalid_cte", "name", err.Error())
	}
	return TypedCTE[R]{name: name, query: q, cte: cte}, nil
}

func (c TypedCTE[R]) ctePlan() query.CTE { return c.cte }

func (c TypedCTE[R]) Source(alias string) (TypedSource[R], error) {
	if alias == "" {
		return TypedSource[R]{}, planError("invalid_source", "alias", "must not be empty")
	}
	ref, err := c.cte.Ref(alias)
	if err != nil {
		return TypedSource[R]{}, planError("invalid_source", "alias", err.Error())
	}
	return TypedSource[R]{source: Source{ref: ref}}, nil
}

func Combine[R any](left Query[R], op CompoundOperator, right Query[R]) (Query[R], error) {
	if err := rejectNativeComposition(left); err != nil {
		return Query[R]{}, err
	}
	if err := rejectNativeComposition(right); err != nil {
		return Query[R]{}, err
	}
	if err := left.Validate(); err != nil {
		return Query[R]{}, err
	}
	if err := right.Validate(); err != nil {
		return Query[R]{}, err
	}
	if !sameResultSchema(left.Schema(), right.Schema()) {
		return Query[R]{}, planError("schema_mismatch", "right", "compound operands have different result schemas")
	}
	l, err := compoundOperand(left)
	if err != nil {
		return Query[R]{}, err
	}
	r, err := compoundOperand(right)
	if err != nil {
		return Query[R]{}, err
	}
	body, err := query.CompoundQuery(l, op, r)
	if err != nil {
		return Query[R]{}, planError("invalid_compound", "operator", err.Error())
	}
	plan := QueryPlan{body: body, projection: cloneItems(left.projection.items)}
	return Query[R]{plan: plan, projection: left.projection}, nil
}

func compoundOperand[R any](q Query[R]) (query.ResultQuery, error) {
	result, err := resultQuery(q)
	if err != nil || (q.plan.limit == nil && q.plan.offset == nil) {
		return result, err
	}
	ref, err := query.Derived(result, "compound_operand")
	if err != nil {
		return query.ResultQuery{}, err
	}
	projections := make([]query.Projection, len(q.Schema().Columns()))
	for i, column := range q.Schema().Columns() {
		projections[i] = query.Project(ref.Column(column.Name)).As(column.Name)
	}
	body, err := query.NewSelect(ref, projections...)
	if err != nil {
		return query.ResultQuery{}, err
	}
	return query.ResultOf(body, q.Schema().Columns()...)
}

// With returns a copy of q carrying each CTE in ctes. It reports an error for a
// zero CTE plan, for a name another CTE on q already holds, and for a native
// query, which carries its own SQL and composes with nothing.
//
// No element of `ctes` may be nil.
func With[R any](q Query[R], ctes ...CTEPlan) (Query[R], error) {
	if err := rejectNativeComposition(q); err != nil {
		return Query[R]{}, err
	}
	if err := q.Validate(); err != nil {
		return Query[R]{}, err
	}
	copy := q
	copy.plan = clonePlan(q.plan)
	seen := make(map[string]struct{}, len(ctes))
	for i, plan := range ctes {
		if plan == nil {
			return Query[R]{}, planError("invalid_cte", fmt.Sprintf("ctes[%d]", i), "must not be nil")
		}
		cte := plan.ctePlan()
		if cte.Name() == "" || cte.Query().Body() == nil {
			return Query[R]{}, planError("invalid_cte", fmt.Sprintf("ctes[%d]", i), "must not be zero")
		}
		for _, existing := range copy.plan.ctes {
			if existing.Name() == cte.Name() {
				return Query[R]{}, planError("duplicate_cte", fmt.Sprintf("ctes[%d]", i), cte.Name())
			}
		}
		if _, ok := seen[cte.Name()]; ok {
			return Query[R]{}, planError("duplicate_cte", fmt.Sprintf("ctes[%d]", i), cte.Name())
		}
		seen[cte.Name()] = struct{}{}
		copy.plan.ctes = append(copy.plan.ctes, cte)
	}
	if _, err := resultQuery(copy); err != nil {
		return Query[R]{}, err
	}
	return copy, nil
}

func CountQuery[R any](q Query[R], includePaging bool) Query[int64] {
	if err := rejectNativeComposition(q); err != nil {
		return Query[int64]{plan: QueryPlan{err: err}, projection: Projection[int64]{}}
	}
	result, err := resultQuery(q)
	if err != nil {
		return Query[int64]{plan: QueryPlan{err: err}, projection: Projection[int64]{}}
	}
	ref, err := query.Derived(result, "count_source")
	if err != nil {
		return Query[int64]{}
	}
	item := Item("count", CountRows(), schema.IntegerType{}, "")
	projection, err := Scalar("count", CountRows(), schema.IntegerType{}, "")
	if err != nil {
		return Query[int64]{}
	}
	plan := QueryPlan{sources: []Source{{ref: ref}}, projection: []ProjectionItem{item}}
	if !includePaging {
		base := q
		base.plan = clonePlan(q.plan)
		base.plan.limit = nil
		base.plan.offset = nil
		result, err = resultQuery(base)
		if err != nil {
			return Query[int64]{plan: QueryPlan{err: err}, projection: Projection[int64]{}}
		}
		ref, err = query.Derived(result, "count_source")
		if err != nil {
			return Query[int64]{}
		}
		plan.sources[0] = Source{ref: ref}
	}
	return Query[int64]{plan: plan, projection: projection}
}

// Render lowers q to SQL text for dialect d without executing it against a
// database, the typed counterpart of the removed TypedSelectBuilder.Build.
// A reader composing a query wants to see the SQL it produces, and a test
// wants to assert that text; neither needs the live connection or the
// discovered engine profile Executor otherwise requires, so Render goes
// straight through the render package the way exec.RenderWrite does for a
// write statement.
//
// It rejects a native query and a mutation rather than guessing which SQL a
// reader meant to see: Render exists for the SELECT a typed Query composes,
// a native query is already rendered SQL text by construction, and
// RenderWrite already covers a write statement carrying its own RETURNING
// clause.
func Render[R any](q Query[R], d dialect.Dialect) (stmt.Statement, error) {
	if err := rejectNativeComposition(q); err != nil {
		return stmt.Statement{}, err
	}
	if q.plan.mutation != nil {
		return stmt.Statement{}, planError("unsupported_feature", "render", "a mutation query has no SELECT to render")
	}
	result, err := resultQuery(q)
	if err != nil {
		return stmt.Statement{}, err
	}
	rendered, err := render.Result(d, result)
	if err != nil {
		return stmt.Statement{}, err
	}
	compiled, err := unwrapBindTokens(rendered)
	if err != nil {
		return stmt.Statement{}, err
	}
	return compiled.Copy()
}

func rejectNativeComposition[R any](q Query[R]) error {
	if q.plan.native != nil {
		return planError("unsupported_feature", "native", "native plans cannot be composed")
	}
	return nil
}

func compositionResultQuery[R any](q Query[R]) (query.ResultQuery, error) {
	if q.plan.native == nil {
		return resultQuery(q)
	}
	native := q.plan.native
	body, err := query.NativeResultOf(native.engine, native.statement.Text(), native.statement.BoundArgs())
	if err != nil {
		if errors.Is(err, sqlscan.ErrNotSelect) {
			return query.ResultQuery{}, planError("unsupported_feature", "native", err.Error())
		}
		return query.ResultQuery{}, planerr.Wrap("invalid_query", "native.sql", err.Error(), err)
	}
	return query.ResultOf(body, q.Schema().Columns()...)
}

func sameResultSchema(left, right ResultSchema) bool {
	return schemaColumnsEqual(left.Columns(), right.Columns())
}

func schemaColumnsEqual(left, right []ResultColumn) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i].Name != right[i].Name || left[i].Nullable != right[i].Nullable || left[i].Codec != right[i].Codec || !reflect.DeepEqual(left[i].Type, right[i].Type) {
			return false
		}
	}
	return true
}

func resultQuery[R any](q Query[R]) (query.ResultQuery, error) {
	if err := q.Validate(); err != nil {
		return query.ResultQuery{}, err
	}
	body, err := queryBody(q.plan)
	if err != nil {
		return query.ResultQuery{}, err
	}
	columns := q.Schema().Columns()
	return query.ResultOf(body, columns...)
}

func queryBody(plan QueryPlan) (query.QueryBody, error) {
	if plan.body != nil {
		if len(plan.ctes) == 0 && len(plan.where) == 0 && len(plan.having) == 0 && len(plan.order) == 0 &&
			!plan.distinct && plan.limit == nil && plan.offset == nil && plan.partitionLimit == 0 && !plan.projected {
			return plan.body, nil
		}
		inner, err := query.ResultOf(plan.body, resultColumns(plan.projection)...)
		if err != nil {
			return nil, err
		}
		ref, err := query.Derived(inner, "compound_source")
		if err != nil {
			return nil, err
		}
		outer := plan
		outer.body = nil
		outer.sources = []Source{{ref: ref}}
		outer.joins = nil
		outer.group = nil
		outer.ctes = append([]query.CTE(nil), plan.ctes...)
		outer.projection = make([]ProjectionItem, len(plan.projection))
		for i, item := range plan.projection {
			if !plan.projected {
				item.expression = ref.Column(item.column.Name)
				item.source = ref.QualifiedName()
				item.bindErr = nil
			}
			outer.projection[i] = item
		}
		return queryBody(outer)
	}
	if len(plan.sources) == 0 {
		return nil, planError("invalid_source", "plan.sources", "must not be empty")
	}
	projections := make([]query.Projection, len(plan.projection))
	for i, item := range plan.projection {
		projections[i] = query.Project(item.expression).As(item.column.Name)
	}
	partitionAlias := "__rasql_partition_row"
	if plan.partitionLimit > 0 {
		partition := make([]query.Expression, len(plan.partition))
		for i, key := range plan.partition {
			partition[i] = key.node
		}
		windowOrder := make([]query.Order, len(plan.partitionOrder))
		for i, term := range plan.partitionOrder {
			windowOrder[i] = lowerOrder(term.node, term.descending, term.nulls)
		}
		rowNumber := query.OverWindow(query.Func("row_number"), query.Window(partition, windowOrder...))
		projections = append(projections, query.Project(rowNumber).As(partitionAlias))
	}
	groups := make([]query.Expression, len(plan.group))
	for i, group := range plan.group {
		groups[i] = group.node
	}
	correlations := make([]query.RelationSource, len(plan.correlations))
	for i, source := range plan.correlations {
		correlations[i] = source.ref
	}
	selectBody, err := query.NewCorrelatedJoinedSelect(plan.sources[0].ref, correlations, plan.joins, groups, projections...)
	if err != nil {
		return nil, err
	}
	if len(plan.ctes) > 0 {
		selectBody, err = selectBody.WithCTEs(plan.ctes...)
		if err != nil {
			return nil, err
		}
	}
	if len(plan.where) > 0 {
		nodes := make([]query.Expression, len(plan.where))
		for i, predicate := range plan.where {
			nodes[i] = predicate.node
		}
		selectBody, err = selectBody.WithWhere(graphCombinePredicates(nodes))
		if err != nil {
			return nil, err
		}
	}
	if len(plan.having) > 0 {
		nodes := make([]query.Expression, len(plan.having))
		for i, predicate := range plan.having {
			nodes[i] = predicate.node
		}
		selectBody, err = selectBody.WithHaving(graphCombinePredicates(nodes))
		if err != nil {
			return nil, err
		}
	}
	orders := make([]query.Order, len(plan.order))
	for i, order := range plan.order {
		if order.result != nil {
			orders[i] = lowerResultOrder(*order.result, order.descending)
			continue
		}
		orders[i] = lowerOrder(order.node, order.descending, order.nulls)
	}
	if len(orders) > 0 {
		selectBody, err = selectBody.WithOrder(orders...)
		if err != nil {
			return nil, err
		}
	}
	if plan.distinct {
		selectBody, err = selectBody.WithDistinct()
		if err != nil {
			return nil, err
		}
	}
	if plan.limit != nil {
		selectBody, err = selectBody.WithLimit(*plan.limit)
		if err != nil {
			return nil, err
		}
	}
	if plan.offset != nil {
		selectBody, err = selectBody.WithOffset(*plan.offset)
		if err != nil {
			return nil, err
		}
	}
	if plan.partitionLimit > 0 {
		innerColumns := make([]ResultColumn, len(plan.projection)+1)
		for i, item := range plan.projection {
			innerColumns[i] = item.column
		}
		innerColumns[len(plan.projection)] = ResultColumn{Name: partitionAlias, Type: schema.IntegerType{}}
		inner, err := query.ResultOf(selectBody, innerColumns...)
		if err != nil {
			return nil, err
		}
		ref, err := query.Derived(inner, "partition_source")
		if err != nil {
			return nil, err
		}
		outerProjections := make([]query.Projection, len(plan.projection))
		for i, item := range plan.projection {
			outerProjections[i] = query.Project(ref.Column(item.column.Name)).As(item.column.Name)
		}
		outer, err := query.NewSelect(ref, outerProjections...)
		if err != nil {
			return nil, err
		}
		outer, err = outer.WithWhere(query.LessThanOrEqual(ref.Column(partitionAlias), plan.partitionLimitValue.node))
		if err != nil {
			return nil, err
		}
		return outer, nil
	}
	return selectBody, nil
}

func graphCombinePredicates(expressions []query.Expression) query.Expression {
	if len(expressions) == 1 {
		return expressions[0]
	}
	return query.And(expressions...)
}

func lowerOrder(expression query.Expression, descending bool, nulls NullOrder) query.Order {
	placement := query.NullPlacementDefault
	switch nulls {
	case NullsFirst:
		placement = query.NullsFirst
	case NullsLast:
		placement = query.NullsLast
	}
	if placement != query.NullPlacementDefault {
		if descending {
			return query.DescNulls(expression, placement)
		}
		return query.AscNulls(expression, placement)
	}
	if descending {
		return query.Desc(expression)
	}
	return query.Asc(expression)
}

// lowerResultOrder rebuilds the same query.Projection the projection lowering
// above builds for this item, so the statement's own validation resolves the
// ordering to a result the statement really reports.
func lowerResultOrder(item ProjectionItem, descending bool) query.Order {
	projection := query.Project(item.expression).As(item.column.Name)
	if descending {
		return query.DescResult(projection)
	}
	return query.AscResult(projection)
}

func resultColumns(items []ProjectionItem) []ResultColumn {
	columns := make([]ResultColumn, len(items))
	for i, item := range items {
		columns[i] = item.column
	}
	return columns
}

// The compiled form lives in internal/bindplan alongside the tokens it
// unwraps. These names stay so the rest of this package reads as before.
type bindSlot = bindplan.Slot
type compiledQuery = bindplan.Compiled

func unwrapBindTokens(statement stmt.Statement) (compiledQuery, error) {
	return bindplan.Unwrap(statement)
}

func matchBaseOccurrences(base, paged compiledQuery) ([]int, error) {
	return bindplan.MatchBaseOccurrences(base, paged)
}

func compileQuery[R any](compiler *querycompile.Compiler, q Query[R]) (compiledQuery, error) {
	if compiler == nil {
		return compiledQuery{}, planError("invalid_compiler", "compiler", "must not be nil")
	}
	if err := q.Validate(); err != nil {
		return compiledQuery{}, mapCompileError(err)
	}
	var statement stmt.Statement
	var err error
	switch {
	case q.plan.mutation != nil:
		statement, err = compiler.Write(q.plan.mutation)
	case q.plan.native != nil:
		statement, err = compiler.Native(q.plan.native.statement)
	default:
		result, resultErr := resultQuery(q)
		if resultErr != nil {
			return compiledQuery{}, resultErr
		}
		statement, err = compiler.Select(result)
	}
	if err != nil {
		return compiledQuery{}, mapCompileError(err)
	}
	return unwrapBindTokens(statement)
}

func mapCompileError(err error) error {
	var planErr *PlanError
	if errors.As(err, &planErr) {
		return err
	}
	if errors.Is(err, sqlscan.ErrInvalidPlaceholder) {
		return planerr.Wrap("invalid_query", "native.sql", err.Error(), err)
	}
	if errors.Is(err, render.ErrNativeEngineMismatch) {
		return planerr.Wrap("engine_mismatch", "native.engine", err.Error(), err)
	}
	var validationErr *query.ValidationError
	if errors.As(err, &validationErr) {
		return planerr.Wrap("invalid_query", validationErr.Path, validationErr.Message, err)
	}
	var profileErr *engineprofile.ProfileError
	if errors.As(err, &profileErr) {
		code, path := "invalid_compiler", "compiler"
		if errors.Is(err, engineprofile.ErrUnsupportedFeature) {
			code = "unsupported_feature"
		}
		if errors.Is(err, engineprofile.ErrBindLimit) {
			code, path = "bind_limit", "args"
		}
		return planerr.Wrap(code, path, err.Error(), err)
	}
	return planerr.Wrap("unsupported_feature", "compiler", err.Error(), err)
}
