package rasql

import "github.com/lestrrat-go/rasql/query"

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
