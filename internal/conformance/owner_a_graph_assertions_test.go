package conformance

import (
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/stretchr/testify/require"
)

func TestOwnerAGraphBindChunkSizeUsesFixedBindsAndKeyWidth(t *testing.T) {
	cases := []struct {
		name                  string
		maxBind, fixed, width int
		want                  int
	}{
		{name: "sqlite members", maxBind: 999, fixed: 1, width: 1, want: 998},
		{name: "wide key", maxBind: 17, fixed: 3, width: 2, want: 7},
		{name: "no room", maxBind: 2, fixed: 2, width: 1, want: 0},
		{name: "invalid width", maxBind: 999, fixed: 1, width: 0, want: 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, graphBindChunkSize(tc.maxBind, tc.fixed, tc.width))
		})
	}
}

func TestOwnerAGraphProfileBindLimitsAreEngineSpecific(t *testing.T) {
	require.Equal(t, 999, graphProfileMaxBind("sqlite"))
	require.Equal(t, 65535, graphProfileMaxBind("postgresql"))
	require.Equal(t, 65535, graphProfileMaxBind("mysql"))
}

func TestOwnerARasqlGraphObservationRequiresOrderedStageEvents(t *testing.T) {
	records := []InvocationRecord{
		correlatedInvocation("one", "graph", 0, "SELECT 1", "execution"),
		correlatedInvocation("one", "graph", 0, "SELECT 1", "consumption"),
		correlatedInvocation("two", "graph", 1, "SELECT 2", "execution"),
		correlatedInvocation("two", "graph", 1, "SELECT 2", "consumption"),
	}
	for index := range records {
		records[index].Ordinal = index
	}
	events := []EventRecord{
		{Event: rasql.Event{LogicalID: "one", ParentID: "graph", Kind: rasql.EventStatement, Phase: rasql.EventStart, StatementIndex: 0}},
		{Event: rasql.Event{LogicalID: "one", ParentID: "graph", Kind: rasql.EventStatement, Phase: rasql.EventTerminal, StatementIndex: 0, Rows: 1}},
		{Event: rasql.Event{LogicalID: "two", ParentID: "graph", Kind: rasql.EventStatement, Phase: rasql.EventStart, StatementIndex: 1}},
		{Event: rasql.Event{LogicalID: "two", ParentID: "graph", Kind: rasql.EventStatement, Phase: rasql.EventTerminal, StatementIndex: 1, Rows: 1}},
	}
	observations, err := rasqlObservations("graph_500_parent_limit", records, events)
	require.NoError(t, err)
	require.Len(t, observations, 2)

	bad := append([]EventRecord(nil), events...)
	bad[2].Event.StatementIndex = 2
	_, err = rasqlObservations("graph_500_parent_limit", records, bad)
	require.Error(t, err)

	bad = append([]EventRecord(nil), events...)
	bad[0].Event.ParentID = ""
	_, err = rasqlObservations("graph_500_parent_limit", records, bad)
	require.Error(t, err)
}
