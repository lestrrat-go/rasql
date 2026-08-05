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
	err := validateExpression(Function{name: FunctionSum, star: true}, map[string]struct{}{}, "projections[0].expression")
	require.Error(t, err)
	require.ErrorContains(t, err, "does not support *")
}
