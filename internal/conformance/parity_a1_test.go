package conformance

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParityRejectsUnfinishedLifecycleEvidence(t *testing.T) {
	base := parityEvidence{
		ResultJSON:           []byte(`{"id":1}`),
		Outcome:              "one",
		RowsReturned:         1,
		RowsConsumed:         1,
		MeasuredRowsConsumed: 1,
		Invocations:          []InvocationRecord{{Kind: "query", SQL: "SELECT 1", Phase: "consumption", Rows: 1}},
	}
	missingPhase := base
	missingPhase.Invocations = []InvocationRecord{{Kind: "query", SQL: "SELECT 1", Rows: 1}}
	require.ErrorContains(t, compareParity("missing-phase", missingPhase, base), "terminal phase")

	callError := errors.New("cancelled")
	failed := base
	failed.Invocations = []InvocationRecord{{Kind: "query", SQL: "SELECT 1", Phase: "consumption", Rows: 1, Err: callError}}
	require.ErrorContains(t, compareParity("error-completion", failed, base), "error completion")
}
