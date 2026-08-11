package ast_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/internal/ast"
	"github.com/stretchr/testify/require"
)

func TestEqualWithQuotedPreservesQuoteMetadata(t *testing.T) {
	left := struct {
		Name   string
		Quoted bool
	}{Name: "Members", Quoted: true}
	right := struct {
		Name   string
		Quoted bool
	}{Name: "Members"}

	require.True(t, ast.Equal(left, right))
	require.False(t, ast.EqualWithQuoted(left, right))
}
