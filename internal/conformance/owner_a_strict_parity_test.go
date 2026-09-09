package conformance

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
)

func TestOwnerAStrictParityRejectsStatementMetadataDrift(t *testing.T) {
	base := strictParityEvidence()
	cases := map[string]func(*statementObservation){
		"role":        func(value *statementObservation) { value.Role = roleReport },
		"parent":      func(value *statementObservation) { value.LogicalParent = "other" },
		"index":       func(value *statementObservation) { value.StatementIndex++ },
		"predicate":   func(value *statementObservation) { value.SQL = "SELECT id FROM projects WHERE id = ? AND is_open = ?" },
		"phase":       func(value *statementObservation) { value.Phase = "execution" },
		"rows":        func(value *statementObservation) { value.RowsConsumed++ },
		"early-close": func(value *statementObservation) { value.EarlyClose = true },
		"error-class": func(value *statementObservation) { value.Err = errors.New("constraint violation") },
		"affected": func(value *statementObservation) {
			value.RowsAffectedValid = true
			value.RowsAffected++
		},
		"row-values": func(value *statementObservation) { value.RowValues[0][0] = int64(2) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			changed := strictParityEvidence()
			mutate(&changed.Observations[0])
			require.Error(t, CompareWorkloadEvidence(name, base, changed))
		})
	}
}

func TestOwnerARasqlObservationRequiresEventPairs(t *testing.T) {
	records := []InvocationRecord{{SQL: "SELECT id FROM projects WHERE id = ?", Args: []any{int64(1)}, Kind: "query", Phase: "consumption", Rows: 1}}
	start := rasql.Event{LogicalID: "stmt-1", Kind: rasql.EventStatement, Phase: rasql.EventStart}
	terminal := start
	terminal.Phase = rasql.EventTerminal
	terminal.Rows = 1
	_, err := rasqlObservations("single_row_read", records, []EventRecord{{Event: start}, {Event: terminal}})
	require.Error(t, err)
	_, err = rasqlObservations("single_row_read", records, []EventRecord{{Event: start}})
	require.Error(t, err)
}

func strictParityEvidence() parityEvidence {
	return parityEvidence{
		ResultJSON: []byte(`{"id":1}`), Outcome: "ok", RowsReturned: 1, RowsConsumed: 1,
		MeasuredRowsConsumed: 1,
		Observations: []statementObservation{{
			Role: roleRead, LogicalParent: "read", StatementIndex: 0, SQL: "SELECT id FROM projects WHERE id = ?",
			Args: []any{int64(1)}, Kind: "query", Phase: "consumption", Started: true, Completed: true,
			CompletionCount: 1, RowsConsumed: 1, RowsAffected: 1, RowsAffectedValid: true,
			RowValues: [][]any{{int64(1)}},
		}},
	}
}
