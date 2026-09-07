package querycompile

import (
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/query"
)

func validateResultCapabilities(p engineprofile.Profile, result query.ResultQuery) error {
	return validateBodyCapabilities(p, result.Body())
}

func validateBodyCapabilities(p engineprofile.Profile, body query.QueryBody) error {
	switch body := body.(type) {
	case query.NativeResult:
		return nil
	case *query.NativeResult:
		if body == nil {
			return fmt.Errorf("native result body must not be nil")
		}
		return nil
	case query.Select:
		return validateSelectCapabilities(p, body)
	case *query.Select:
		if body != nil {
			return validateSelectCapabilities(p, *body)
		}
	case query.Compound:
		if err := validateResultCapabilities(p, body.Left()); err != nil {
			return err
		}
		return validateResultCapabilities(p, body.Right())
	case *query.Compound:
		if body != nil {
			if err := validateResultCapabilities(p, body.Left()); err != nil {
				return err
			}
			return validateResultCapabilities(p, body.Right())
		}
	}
	return nil
}

func validateSelectCapabilities(p engineprofile.Profile, s query.Select) error {
	if body := s.From().ResultBody(); body != nil {
		if err := validateBodyCapabilities(p, body); err != nil {
			return err
		}
	}
	for _, cte := range s.CTEs() {
		if cte.Query().Body() != nil {
			if err := validateBodyCapabilities(p, cte.Query().Body()); err != nil {
				return err
			}
		}
	}
	for _, projection := range s.Projections() {
		if err := validateExpressionCapabilities(p, projection.ProjectedExpression()); err != nil {
			return err
		}
	}
	if err := validateExpressionCapabilities(p, s.Where()); err != nil {
		return err
	}
	if err := validateExpressionList(p, s.GroupBy()); err != nil {
		return err
	}
	if err := validateExpressionCapabilities(p, s.Having()); err != nil {
		return err
	}
	for _, order := range s.OrderBy() {
		if order.NullPlacement() != query.NullPlacementDefault && !p.Capabilities.ExplicitNullOrdering {
			return unsupported(p, "explicit NULL ordering")
		}
		if err := validateExpressionCapabilities(p, order.Expression()); err != nil {
			return err
		}
	}
	for _, join := range s.Joins() {
		if body := join.Source().ResultBody(); body != nil {
			if err := validateBodyCapabilities(p, body); err != nil {
				return err
			}
		}
		if err := validateExpressionCapabilities(p, join.On()); err != nil {
			return err
		}
	}
	return nil
}

func validateExpressionList(p engineprofile.Profile, expressions []query.Expression) error {
	for _, expression := range expressions {
		if err := validateExpressionCapabilities(p, expression); err != nil {
			return err
		}
	}
	return nil
}

func validateExpressionCapabilities(p engineprofile.Profile, expression query.Expression) error {
	if expression == nil {
		return nil
	}
	v := reflect.ValueOf(expression)
	if v.Kind() == reflect.Pointer {
		if v.IsNil() {
			return nil
		}
		if value, ok := v.Elem().Interface().(query.Expression); ok {
			return validateExpressionCapabilities(p, value)
		}
	}
	switch expression := expression.(type) {
	case query.TrustedFragment:
		for _, part := range expression.Parts() {
			if hole, ok := part.(interface{ ValueExpression() query.Expression }); ok {
				if err := validateExpressionCapabilities(p, hole.ValueExpression()); err != nil {
					return err
				}
			}
		}
	case query.Over:
		if !p.Capabilities.WindowFunctions {
			return unsupported(p, "window functions")
		}
		if err := validateExpressionCapabilities(p, expression.Expression()); err != nil {
			return err
		}
		window := expression.Window()
		if err := validateExpressionList(p, window.Partition()); err != nil {
			return err
		}
		for _, order := range window.Order() {
			if order.NullPlacement() != query.NullPlacementDefault && !p.Capabilities.ExplicitNullOrdering {
				return unsupported(p, "explicit NULL ordering")
			}
			if err := validateExpressionCapabilities(p, order.Expression()); err != nil {
				return err
			}
		}
	case query.Binary:
		if err := validateExpressionCapabilities(p, expression.Left()); err != nil {
			return err
		}
		return validateExpressionCapabilities(p, expression.Right())
	case query.Logical:
		return validateExpressionList(p, expression.Expressions())
	case query.Not:
		return validateExpressionCapabilities(p, expression.Expression())
	case query.NullTest:
		return validateExpressionCapabilities(p, expression.Expression())
	case query.Membership:
		if err := validateExpressionCapabilities(p, expression.Expression()); err != nil {
			return err
		}
		if err := validateExpressionList(p, expression.Values()); err != nil {
			return err
		}
		if subquery, ok := expression.Subquery(); ok {
			return validateBodyCapabilities(p, subquery.Statement())
		}
	case query.Subquery:
		return validateSelectCapabilities(p, expression.Statement())
	case query.Existence:
		return validateSelectCapabilities(p, expression.Subquery().Statement())
	case query.Function:
		return validateExpressionList(p, expression.Arguments())
	case query.Filter:
		if err := validateExpressionCapabilities(p, expression.Aggregate()); err != nil {
			return err
		}
		return validateExpressionCapabilities(p, expression.Predicate())
	case query.Cast:
		return validateExpressionCapabilities(p, expression.Expression())
	case query.Case:
		if err := validateExpressionCapabilities(p, expression.Operand()); err != nil {
			return err
		}
		for _, branch := range expression.Branches() {
			if err := validateExpressionCapabilities(p, branch.Predicate()); err != nil {
				return err
			}
			if err := validateExpressionCapabilities(p, branch.Result()); err != nil {
				return err
			}
		}
		if fallback, ok := expression.Fallback(); ok {
			return validateExpressionCapabilities(p, fallback)
		}
	}
	return nil
}

func validateWriteCapabilities(p engineprofile.Profile, statement query.WriteStatement) error {
	if statement == nil {
		return fmt.Errorf("write statement must not be nil")
	}
	value := reflect.ValueOf(statement)
	if value.Kind() == reflect.Pointer && value.IsNil() {
		return fmt.Errorf("write statement must not be nil")
	}
	returning := statement.Returning()
	for _, projection := range returning {
		if err := validateExpressionCapabilities(p, projection.ProjectedExpression()); err != nil {
			return err
		}
	}
	if len(returning) > 0 {
		allowed := p.Capabilities.Returning == engineprofile.ReturningInsert
		if p.Capabilities.Returning == engineprofile.ReturningInsertUpdateDelete {
			allowed = true
		}
		if !allowed {
			return unsupported(p, "RETURNING")
		}
		switch statement.(type) {
		case query.Insert, *query.Insert, query.Upsert, *query.Upsert:
		default:
			if p.Capabilities.Returning != engineprofile.ReturningInsertUpdateDelete {
				return unsupported(p, "RETURNING on this write")
			}
		}
	}
	switch statement := statement.(type) {
	case query.Insert:
		if source, ok := statement.SelectSource(); ok {
			return validateResultCapabilities(p, source)
		}
		for _, row := range statement.Rows() {
			for _, expression := range row {
				if err := validateExpressionCapabilities(p, expression); err != nil {
					return err
				}
			}
		}
	case *query.Insert:
		if statement != nil {
			return validateWriteCapabilities(p, *statement)
		}
	case query.Update:
		for _, assignment := range statement.Assignments() {
			if err := validateExpressionCapabilities(p, assignment.Value()); err != nil {
				return err
			}
		}
		return validateExpressionCapabilities(p, statement.Where())
	case *query.Update:
		if statement != nil {
			return validateWriteCapabilities(p, *statement)
		}
	case query.Delete:
		return validateExpressionCapabilities(p, statement.Where())
	case *query.Delete:
		if statement != nil {
			return validateWriteCapabilities(p, *statement)
		}
	case query.Upsert:
		for _, assignment := range statement.Assignments() {
			if err := validateExpressionCapabilities(p, assignment.Value()); err != nil {
				return err
			}
		}
		if err := validateExpressionCapabilities(p, statement.ConflictWhere()); err != nil {
			return err
		}
		if err := validateExpressionCapabilities(p, statement.UpdateWhere()); err != nil {
			return err
		}
		return validateWriteCapabilities(p, statement.Insert())
	case *query.Upsert:
		if statement != nil {
			return validateWriteCapabilities(p, *statement)
		}
	}
	return nil
}

func unsupported(p engineprofile.Profile, feature string) error {
	return &engineprofile.ProfileError{Code: engineprofile.ErrUnsupportedFeature, Engine: p.Engine, Version: p.Version, Feature: feature, Detail: fmt.Sprintf("%s is not supported", feature)}
}
