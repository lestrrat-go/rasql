package conformance

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParityGateRejectsEachEvidenceDifference(t *testing.T) {
	base := parityEvidence{ResultJSON: []byte(`[{"id":1}]`), Outcome: "committed", RowsReturned: 1, RowsConsumed: 1,
		MeasuredRowsConsumed: 1,
		Invocations:          []InvocationRecord{{SQL: "SELECT 1", Kind: "query", Args: []any{int64(1)}, Phase: "consumption", Rows: 1}}}
	for _, test := range []struct {
		name   string
		mutate func(parityEvidence) parityEvidence
	}{
		{name: "value", mutate: func(value parityEvidence) parityEvidence { value.ResultJSON = []byte(`[{"id":2}]`); return value }},
		{name: "outcome", mutate: func(value parityEvidence) parityEvidence { value.Outcome = "rolled_back"; return value }},
		{name: "rows", mutate: func(value parityEvidence) parityEvidence { value.RowsConsumed = 2; return value }},
		{name: "statement", mutate: func(value parityEvidence) parityEvidence {
			value.Invocations = append(value.Invocations, InvocationRecord{SQL: "SELECT 2", Kind: "query", Phase: "consumption", Rows: 1})
			return value
		}},
		{name: "order", mutate: func(value parityEvidence) parityEvidence {
			value.Invocations = append([]InvocationRecord(nil), value.Invocations...)
			value.Invocations[0].Args = []any{int64(2)}
			return value
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			left := test.mutate(base)
			require.Error(t, compareParity(test.name, left, base))
		})
	}
}

func TestParityEvidenceDigestUsesLengthFramedSQL(t *testing.T) {
	first := parityEvidence{Invocations: []InvocationRecord{{SQL: "ab", Kind: "query"}, {SQL: "c", Kind: "query"}}}
	second := parityEvidence{Invocations: []InvocationRecord{{SQL: "a", Kind: "query"}, {SQL: "bc", Kind: "query"}}}
	require.NotEqual(t, first.SQLDigest(), second.SQLDigest())
}

func TestCanonicalWorkloadsCoverRequiredSemanticMatrix(t *testing.T) {
	want := map[string]bool{
		"single_row_read": true, "nullable_join_report": true, "typed_sql_report": true,
		"taskboard_graph_page": true, "create_patch": true, "bulk_write": true,
		"rollback": true, "bulk_rollback": true, "early_exit": true,
		"cancellation": true, "graph_500_parent_limit": true,
	}
	got := make(map[string]bool)
	for _, workload := range canonicalWorkloads() {
		got[workload.Name] = true
	}
	require.Equal(t, want, got)
}
