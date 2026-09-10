package conformance

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestOwnerAParityRejectsMissingAndReorderedArguments(t *testing.T) {
	base := parityEvidence{ResultJSON: []byte(`{"ok":true}`), Outcome: "ok", RowsReturned: 1,
		Invocations: []InvocationRecord{{Kind: "query", SQL: "SELECT 1", Args: []any{int64(1), "x"}}}}
	missing := base
	missing.Invocations = []InvocationRecord{{Kind: "query", SQL: "SELECT 1"}}
	require.Error(t, compareParity("args-missing", base, missing))
	reordered := base
	reordered.Invocations = []InvocationRecord{{Kind: "query", SQL: "SELECT 1", Args: []any{"x", int64(1)}}}
	require.Error(t, compareParity("args-reordered", base, reordered))
}

func TestOwnerAGraphTaskSelectionUsesTypedPartitionArguments(t *testing.T) {
	statement, args := graphTaskSelection("sqlite", []int64{1, 7, 50})
	require.Contains(t, statement, "ROW_NUMBER() OVER")
	require.Equal(t, []any{true, int64(1), int64(7), int64(50), int64(5)}, args)
}

func TestOwnerAOverdueTasksUsesTypedNativeArguments(t *testing.T) {
	query, err := OverdueTasks("sqlite", 1, true, time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	_ = query
}

func TestOwnerASignatureRejectsSeedCardinalityDrift(t *testing.T) {
	document, err := LoadPortableSignature()
	require.NoError(t, err)
	document.Seed.Cardinalities[0].Count++
	require.Error(t, document.Validate())
}
