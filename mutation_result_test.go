package rasql_test

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
)

func TestMutationResultStatesAndPreconditionSentinel(t *testing.T) {
	states := []rasql.InputOutcome{
		rasql.InputUnattempted,
		rasql.InputApplied,
		rasql.InputRolledBack,
		rasql.InputRejected,
		rasql.InputUnknown,
	}
	seen := make(map[rasql.InputOutcome]struct{}, len(states))
	for _, state := range states {
		_, duplicate := seen[state]
		require.False(t, duplicate)
		seen[state] = struct{}{}
	}
	require.ErrorIs(t, rasql.ErrPrecondition, rasql.ErrPrecondition)
	require.False(t, errors.Is(errors.New("other"), rasql.ErrPrecondition))
}
