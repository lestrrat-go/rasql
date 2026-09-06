package stmt_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestStatementArgsIsolatesByteArguments(t *testing.T) {
	s := stmt.New("SELECT ?", []byte("abc"), sql.Named("payload", []byte("abc")))

	first := s.Args()
	first[0].([]byte)[0] = 'x'
	named := first[1].(sql.NamedArg)
	named.Value.([]byte)[0] = 'x'
	require.Equal(t, "payload", named.Name)
	require.Equal(t, []byte("abc"), s.BoundArgs()[0])
	require.Equal(t, sql.Named("payload", []byte("abc")), s.BoundArgs()[1])

	second := s.Args()
	second[0].([]byte)[0] = 'y'
	secondNamed := second[1].(sql.NamedArg)
	secondNamed.Value.([]byte)[0] = 'y'
	require.Equal(t, []byte("xbc"), first[0])
	require.Equal(t, []byte("xbc"), first[1].(sql.NamedArg).Value)
	require.Equal(t, []byte("abc"), s.BoundArgs()[0])
	require.Equal(t, sql.Named("payload", []byte("abc")), s.BoundArgs()[1])
}

// TestStatementBoundArgsAliasesStorage pins the contract that separates
// BoundArgs from Args: BoundArgs hands out the statement's own arg slice, so
// a caller that writes into it changes what the statement holds, while Args
// hands out a copy that a later write leaves untouched.
func TestStatementBoundArgsAliasesStorage(t *testing.T) {
	s := stmt.New("SELECT * FROM users WHERE id = ?", 1)

	boundArgs := s.BoundArgs()
	boundArgs[0] = 99
	require.Equal(t, []any{99}, s.BoundArgs(), "BoundArgs must alias the statement's storage")

	copied := s.Args()
	copied[0] = 7
	require.Equal(t, []any{99}, s.BoundArgs(), "Args must return a copy that leaves the statement's storage untouched")
}

// TestStatementSQLReturnsRenderedText pins that SQL returns the text passed
// to New unchanged.
func TestStatementSQLReturnsRenderedText(t *testing.T) {
	s := stmt.New("SELECT 1")
	require.Equal(t, "SELECT 1", s.SQL())
}
