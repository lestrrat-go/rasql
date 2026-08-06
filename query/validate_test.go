package query

import (
	"testing"

	"github.com/stretchr/testify/require"
)

// TestFunctionValidationRejectsStarOnNonCountFunctions covers the star branch
// of the Function case in validateExpression. No exported constructor can
// build this value: only CountAll sets star, and it always pairs star with
// FunctionCount. That makes this the "no other way around it" case for an
// in-package test, unlike the rest of the Function suite in select_test.go.
func TestFunctionValidationRejectsStarOnNonCountFunctions(t *testing.T) {
	_, err := validateExpression(Function{name: FunctionSum, star: true}, projectionContext(map[string]struct{}{}), "projections[0].expression")
	require.Error(t, err)
	require.ErrorContains(t, err, "does not support *")
}

// TestFunctionValidationRejectsStarOnScalarFunctions covers the same star
// branch for a curated scalar name and for the Func escape hatch. No exported
// constructor can build either value: Call takes no star flag, and Func's
// arguments always populate arguments rather than star.
func TestFunctionValidationRejectsStarOnScalarFunctions(t *testing.T) {
	_, err := validateExpression(Function{name: FunctionLower, star: true}, projectionContext(map[string]struct{}{}), "projections[0].expression")
	require.Error(t, err)
	require.ErrorContains(t, err, "does not support *")

	_, err = validateExpression(Function{name: "jsonb_path_query", star: true, unchecked: true}, projectionContext(map[string]struct{}{}), "projections[0].expression")
	require.Error(t, err)
	require.ErrorContains(t, err, "does not support *")
}

// TestFunctionValidationMessageNamesHavingAndGroupedOrderBy pins the rewritten
// misplaced-aggregate message so a future edit cannot drop the HAVING mention
// or narrow the ORDER BY wording back to "whose projections all aggregate" now
// that a statement can group explicitly.
func TestFunctionValidationMessageNamesHavingAndGroupedOrderBy(t *testing.T) {
	_, err := validateExpression(Call(FunctionCount, Value{}), clauseContext(map[string]struct{}{}, "a WHERE clause"), "where")
	require.Error(t, err)
	require.ErrorContains(t, err, "is only valid in a SELECT projection, in a HAVING clause, or in the ORDER BY clause of a statement that groups")
}
