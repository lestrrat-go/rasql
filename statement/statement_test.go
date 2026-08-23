package statement_test

import (
	"testing"

	"github.com/lestrrat-go/rasql/statement"
	"github.com/stretchr/testify/require"
)

// TestStatementBoundArgsAliasesStorage pins the contract that separates
// BoundArgs from Args: BoundArgs hands out the statement's own arg slice, so
// a caller that writes into it changes what the statement holds, while Args
// hands out a copy that a later write leaves untouched.
func TestStatementBoundArgsAliasesStorage(t *testing.T) {
	stmt := statement.New("SELECT * FROM users WHERE id = ?", 1)

	boundArgs := stmt.BoundArgs()
	boundArgs[0] = 99
	require.Equal(t, []any{99}, stmt.BoundArgs(), "BoundArgs must alias the statement's storage")

	copied := stmt.Args()
	copied[0] = 7
	require.Equal(t, []any{99}, stmt.BoundArgs(), "Args must return a copy that leaves the statement's storage untouched")
}

// TestStatementSQLReturnsRenderedText pins that SQL returns the text passed
// to New unchanged.
func TestStatementSQLReturnsRenderedText(t *testing.T) {
	stmt := statement.New("SELECT 1")
	require.Equal(t, "SELECT 1", stmt.SQL())
}
