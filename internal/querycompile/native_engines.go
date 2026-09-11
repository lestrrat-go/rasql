package querycompile

import (
	"reflect"

	"github.com/lestrrat-go/rasql/query"
)

// NativeEngines returns the native engine names embedded anywhere in a result
// body, including derived sources, CTEs, joins, and subquery expressions.
func NativeEngines(result query.ResultQuery) []string {
	var engines []string
	collectBody(result.Body(), &engines)
	return engines
}

func collectBody(body query.QueryBody, engines *[]string) {
	switch body := body.(type) {
	case query.NativeResult:
		*engines = append(*engines, body.Engine())
	case *query.NativeResult:
		if body != nil {
			*engines = append(*engines, body.Engine())
		}
	case query.Select:
		collectSelect(body, engines)
	case *query.Select:
		if body != nil {
			collectSelect(*body, engines)
		}
	case query.Compound:
		collectBody(body.Left().Body(), engines)
		collectBody(body.Right().Body(), engines)
	case *query.Compound:
		if body != nil {
			collectBody(body.Left().Body(), engines)
			collectBody(body.Right().Body(), engines)
		}
	}
}

func collectSelect(statement query.Select, engines *[]string) {
	if body := statement.From().ResultBody(); body != nil {
		collectBody(body, engines)
	}
	for _, join := range statement.Joins() {
		if body := join.Source().ResultBody(); body != nil {
			collectBody(body, engines)
		}
		collectExpression(join.On(), engines)
	}
	for _, cte := range statement.CTEs() {
		collectBody(cte.Query().Body(), engines)
	}
	for _, projection := range statement.Projections() {
		collectExpression(projection.ProjectedExpression(), engines)
	}
	collectExpression(statement.Where(), engines)
	for _, expression := range statement.GroupBy() {
		collectExpression(expression, engines)
	}
	collectExpression(statement.Having(), engines)
	for _, order := range statement.OrderBy() {
		collectExpression(order.Expression(), engines)
	}
}

func collectExpression(expression query.Expression, engines *[]string) {
	if expression == nil {
		return
	}
	value := reflect.ValueOf(expression)
	if value.Kind() == reflect.Pointer {
		if node, ok := value.Elem().Interface().(query.Expression); ok {
			collectExpression(node, engines)
			return
		}
	}
	switch expression := expression.(type) {
	case query.TrustedFragment:
		for _, part := range expression.Parts() {
			if hole, ok := part.(interface{ ValueExpression() query.Expression }); ok {
				collectExpression(hole.ValueExpression(), engines)
			}
		}
	case query.Over:
		collectExpression(expression.Expression(), engines)
		window := expression.Window()
		for _, child := range window.Partition() {
			collectExpression(child, engines)
		}
		for _, order := range window.Order() {
			collectExpression(order.Expression(), engines)
		}
	case query.Binary:
		collectExpression(expression.Left(), engines)
		collectExpression(expression.Right(), engines)
	case query.Logical:
		for _, child := range expression.Expressions() {
			collectExpression(child, engines)
		}
	case query.Not:
		collectExpression(expression.Expression(), engines)
	case query.NullTest:
		collectExpression(expression.Expression(), engines)
	case query.Membership:
		collectExpression(expression.Expression(), engines)
		for _, child := range expression.Values() {
			collectExpression(child, engines)
		}
		if subquery, ok := expression.Subquery(); ok {
			collectBody(subquery.Statement(), engines)
		}
	case query.Subquery:
		collectSelect(expression.Statement(), engines)
	case query.Existence:
		collectSelect(expression.Subquery().Statement(), engines)
	case query.Function:
		for _, child := range expression.Arguments() {
			collectExpression(child, engines)
		}
	case query.Filter:
		collectExpression(expression.Aggregate(), engines)
		collectExpression(expression.Predicate(), engines)
	case query.Cast:
		collectExpression(expression.Expression(), engines)
	case query.Case:
		collectExpression(expression.Operand(), engines)
		for _, branch := range expression.Branches() {
			collectExpression(branch.Predicate(), engines)
			collectExpression(branch.Result(), engines)
		}
		if fallback, ok := expression.Fallback(); ok {
			collectExpression(fallback, engines)
		}
	}
}
