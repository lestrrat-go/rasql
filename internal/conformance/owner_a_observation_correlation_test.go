package conformance

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestOwnerAObservationCorrelation(t *testing.T) {
	records, events := correlatedObservationFixture()
	shuffled := []InvocationRecord{records[2], records[3], records[0], records[1]}
	observations, err := rasqlObservations("correlation", shuffled, events)
	require.NoError(t, err)
	require.Len(t, observations, 2)
	require.Equal(t, "SELECT first", observations[0].SQL)
	require.Equal(t, "SELECT second", observations[1].SQL)
	require.Equal(t, 0, observations[0].StatementIndex)
	require.Equal(t, 1, observations[1].StatementIndex)
}

func TestOwnerAObservationLifecycleMatrix(t *testing.T) {
	cases := map[string]func(*[]InvocationRecord, *[]EventRecord){
		"blank start ID": func(_ *[]InvocationRecord, events *[]EventRecord) {
			(*events)[0].Event.LogicalID = ""
		},
		"blank terminal ID": func(_ *[]InvocationRecord, events *[]EventRecord) {
			(*events)[1].Event.LogicalID = ""
		},
		"blank invocation ID": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].LogicalID = ""
		},
		"reused event ID": func(_ *[]InvocationRecord, events *[]EventRecord) {
			(*events)[2].Event.LogicalID = (*events)[0].Event.LogicalID
		},
		"missing start":   func(_ *[]InvocationRecord, events *[]EventRecord) { *events = (*events)[1:] },
		"duplicate start": func(_ *[]InvocationRecord, events *[]EventRecord) { *events = append(*events, (*events)[0]) },
		"duplicate terminal": func(_ *[]InvocationRecord, events *[]EventRecord) {
			*events = append(*events, (*events)[1])
		},
		"missing terminal": func(_ *[]InvocationRecord, events *[]EventRecord) { *events = (*events)[:len(*events)-1] },
		"terminal before start": func(_ *[]InvocationRecord, events *[]EventRecord) {
			(*events)[0], (*events)[1] = (*events)[1], (*events)[0]
		},
		"missing execution":   func(records *[]InvocationRecord, _ *[]EventRecord) { *records = (*records)[1:] },
		"missing consumption": func(records *[]InvocationRecord, _ *[]EventRecord) { *records = (*records)[:1] },
		"extra invocation": func(records *[]InvocationRecord, _ *[]EventRecord) {
			extra := (*records)[0]
			extra.Ordinal = 4
			*records = append(*records, extra)
		},
		"missing completion": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].Completions = nil
		},
		"duplicate completion": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].Completions = append((*records)[0].Completions, (*records)[0].Completions[0])
		},
		"duplicate invocation ordinal": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[1].Ordinal = (*records)[0].Ordinal
		},
		"extra completion": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].Completions = append((*records)[0].Completions, (*records)[0].Completions[0])
		},
		"reordered phase": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].Completions[0].Phase = "consumption"
		},
		"lost context": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].Completions[0].ContextMatched = false
		},
		"lost start context": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].ContextMatched = false
		},
		"execution/consumption SQL drift": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[1].SQL = "SELECT changed"
		},
		"execution/consumption nil-sensitive argument drift": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].Args = []any{int64(1)}
			(*records)[1].Args = []any{}
		},
		"invocation parent drift": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].ParentID = "other"
		},
		"invocation index drift": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].StatementIndex = 4
		},
		"parent drift":        func(_ *[]InvocationRecord, events *[]EventRecord) { (*events)[0].Event.ParentID = "other" },
		"terminal kind drift": func(_ *[]InvocationRecord, events *[]EventRecord) { (*events)[1].Event.Kind = rasql.EventScope },
		"index drift":         func(_ *[]InvocationRecord, events *[]EventRecord) { (*events)[0].Event.StatementIndex = 4 },
		"terminal parent drift": func(_ *[]InvocationRecord, events *[]EventRecord) {
			(*events)[1].Event.ParentID = "other"
		},
		"terminal index drift": func(_ *[]InvocationRecord, events *[]EventRecord) {
			(*events)[1].Event.StatementIndex = 4
		},
		"negative invocation ordinal": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].Ordinal = -1
		},
		"unknown invocation event kind": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].EventKind = 99
		},
		"invocation missing event": func(records *[]InvocationRecord, _ *[]EventRecord) {
			(*records)[0].LogicalID = "missing"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			records, events := correlatedObservationFixture()
			mutate(&records, &events)
			_, err := rasqlObservations("correlation", records, events)
			require.ErrorContains(t, err, lifecycleErrorSubstring(name))
		})
	}
}

func lifecycleErrorSubstring(name string) string {
	switch name {
	case "blank start ID", "blank terminal ID":
		return "rasql event has no logical identity"
	case "blank invocation ID":
		return "invocation 0 has no logical identity"
	case "reused event ID", "duplicate start":
		return `rasql event "first" has duplicate start`
	case "missing start", "terminal before start":
		return `rasql event "first" has terminal before start`
	case "duplicate terminal":
		return `rasql event "first" has duplicate terminal`
	case "missing terminal":
		return `rasql event "second" has no terminal`
	case "missing execution", "missing consumption":
		return `statement "first" query execution failure does not match terminal`
	case "extra invocation":
		return `statement "first" has invalid query invocation count 3`
	case "missing completion":
		return "invocation 0 has 0 completions"
	case "duplicate completion", "extra completion":
		return "invocation 0 has 2 completions"
	case "duplicate invocation ordinal":
		return "invocation ordinal 0 is duplicated"
	case "reordered phase":
		return `statement "first" query phase order or error differs`
	case "lost context", "lost start context":
		return "invocation 0 lost returned event context"
	case "execution/consumption SQL drift", "execution/consumption nil-sensitive argument drift":
		return `statement "first" invocation 1 operation differs`
	case "invocation parent drift", "invocation index drift":
		return `invocation 0 identity differs from event "first"`
	case "parent drift", "index drift", "terminal kind drift", "terminal parent drift", "terminal index drift":
		return `rasql event "first" terminal identity differs from start`
	case "negative invocation ordinal":
		return "invocation has negative ordinal -1"
	case "unknown invocation event kind":
		return "invocation 0 has unknown event kind 99"
	case "invocation missing event":
		return `invocation "missing" has no event start`
	default:
		return "unexpected lifecycle error"
	}
}

func TestOwnerAObservationErrorLifecycleMatrix(t *testing.T) {
	for name, shapes := range map[string][2][]any{
		"nil execution and empty consumption": {nil, []any{}},
		"empty execution and nil consumption": {[]any{}, nil},
	} {
		t.Run(name, func(t *testing.T) {
			records, events := correlatedObservationFixture()
			records = records[:2]
			events = events[:2]
			records[0].Args = shapes[0]
			records[1].Args = shapes[1]
			_, err := correlateRASQLObservations("correlation", records, events)
			require.ErrorContains(t, err, `statement "first" invocation 1 operation differs`)
		})
	}
	t.Run("successful execution without consumption is rejected", func(t *testing.T) {
		records, events := correlatedObservationFixture()
		_, err := rasqlObservations("correlation", records[:1], events[:2])
		require.ErrorContains(t, err, "execution failure")
	})
	t.Run("successful query", func(t *testing.T) {
		records, events := correlatedObservationFixture()
		set, err := correlateRASQLObservations("correlation", records[:2], events[:2])
		require.NoError(t, err)
		assertSingleCorrelatedStatement(t, set, "first", "parent", 0, "query", "consumption", 1, false, nil, []int{0, 1})
	})
	t.Run("query execution failure", func(t *testing.T) {
		records, events := correlatedObservationFixture()
		cause := errors.New("execution failed")
		records = records[:1]
		records[0].Completions[0].Err = cause
		events = events[:2]
		events[1].Event.Rows = 0
		events[1].Event.Err = cause
		set, err := correlateRASQLObservations("correlation", records, events)
		require.NoError(t, err)
		assertSingleCorrelatedStatement(t, set, "first", "parent", 0, "query", "execution", 0, false, cause, []int{0})
	})
	t.Run("query consumption failure after successful execution", func(t *testing.T) {
		records, events := correlatedObservationFixture()
		cause := errors.New("decode failed")
		records = records[:2]
		records[1].Completions[0].Err = cause
		events = events[:2]
		events[1].Event.Err = cause
		set, err := correlateRASQLObservations("correlation", records, events)
		require.NoError(t, err)
		assertSingleCorrelatedStatement(t, set, "first", "parent", 0, "query", "consumption", 1, false, cause, []int{0, 1})
		require.NoError(t, set.Events[0].Invocations[0].Completions[0].Err)
		require.ErrorIs(t, set.Events[0].Invocations[1].Completions[0].Err, cause)
	})
	t.Run("same message errors are not correlated", func(t *testing.T) {
		records, events := correlatedObservationFixture()
		records = records[:1]
		events = events[:2]
		records[0].Completions[0].Err = errors.New("same message")
		events[1].Event.Err = errors.New("same message")
		_, err := correlateRASQLObservations("correlation", records, events)
		require.ErrorContains(t, err, `statement "first" query execution failure does not match terminal`)
	})
	for _, tc := range []struct {
		name string
		err  error
	}{
		{name: "exec success"},
		{name: "exec failure", err: errors.New("exec failed")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			records, events := correlatedObservationFixture()
			record := records[0]
			record.Kind = "exec"
			record.Completions = []InvocationCompletion{{Phase: "execution", Rows: 0, EarlyClose: false, Err: tc.err, ContextMatched: true}}
			records = []InvocationRecord{record}
			events = events[:2]
			events[1].Event.Rows = 0
			events[1].Event.EarlyClose = false
			events[1].Event.Err = tc.err
			set, err := correlateRASQLObservations("correlation", records, events)
			require.NoError(t, err)
			assertSingleCorrelatedStatement(t, set, "first", "parent", 0, "exec", "execution", 0, false, tc.err, []int{0})
		})
	}
}

func assertSingleCorrelatedStatement(t *testing.T, set rasqlObservationSet, id, parent string, index int, kind, phase string, rows int64, early bool, wantErr error, ordinals []int) {
	t.Helper()
	require.Len(t, set.Statements, 1)
	observation := set.Statements[0]
	require.Equal(t, phase, observation.Phase)
	require.Equal(t, rows, observation.RowsConsumed)
	require.Equal(t, early, observation.EarlyClose)
	if wantErr == nil {
		require.NoError(t, observation.Err)
	} else {
		require.ErrorIs(t, observation.Err, wantErr)
	}
	require.Len(t, set.Events, 1)
	event := set.Events[0]
	require.Equal(t, id, event.LogicalID)
	require.Equal(t, parent, event.ParentID)
	require.Equal(t, index, event.StatementIndex)
	require.Equal(t, rasql.EventStatement, event.Kind)
	require.Equal(t, rasql.Event{LogicalID: id, ParentID: parent, Kind: rasql.EventStatement, Phase: rasql.EventStart, StatementIndex: index}, event.Start)
	require.Equal(t, rasql.Event{LogicalID: id, ParentID: parent, Kind: rasql.EventStatement, Phase: rasql.EventTerminal, StatementIndex: index, Rows: rows, EarlyClose: early, Err: wantErr}, event.Terminal)
	require.Len(t, event.Invocations, len(ordinals))
	for position, ordinal := range ordinals {
		invocation := event.Invocations[position]
		require.Equal(t, ordinal, invocation.Ordinal)
		require.Equal(t, id, invocation.LogicalID)
		require.Equal(t, parent, invocation.ParentID)
		require.Equal(t, rasql.EventStatement, invocation.EventKind)
		require.Equal(t, index, invocation.StatementIndex)
		require.Equal(t, kind, invocation.Kind)
		require.True(t, invocation.ContextMatched)
		require.Len(t, invocation.Completions, 1)
		completion := invocation.Completions[0]
		require.True(t, completion.ContextMatched)
		require.Equal(t, operationPhase(kind, position, len(ordinals)), completion.Phase)
		require.Equal(t, operationRows(phase, rows, position, len(ordinals)), completion.Rows)
		require.Equal(t, early, completion.EarlyClose)
		if position == len(ordinals)-1 || len(ordinals) == 1 {
			if wantErr == nil {
				require.NoError(t, completion.Err)
			} else {
				require.ErrorIs(t, completion.Err, wantErr)
			}
		} else {
			require.NoError(t, completion.Err)
		}
	}
}

func operationPhase(kind string, position, count int) string {
	if kind == "query" && count == 2 && position == 1 {
		return "consumption"
	}
	return "execution"
}

func operationRows(summary string, rows int64, position, count int) int64 {
	if summary == "consumption" && count == 2 && position == 0 {
		return 0
	}
	return rows
}

func TestOwnerAInvocationSnapshotPreservesNestedArguments(t *testing.T) {
	payload := []byte("original")
	rawPayload := sql.RawBytes("raw-original")
	directPayload := []byte("direct-original")
	recorder := InvocationRecorder{Records: []InvocationRecord{{Args: cloneInvocationArgs([]any{
		directPayload, rawPayload, sql.Named("payload", payload),
	}), Completions: []InvocationCompletion{}}}}
	payload[0] = 'X'
	rawPayload[0] = 'X'
	directPayload[0] = 'X'
	first := recorder.Snapshot()
	first[0].Args[0].([]byte)[0] = 'Y'
	first[0].Args[1].(sql.RawBytes)[0] = 'Y'
	first[0].Args[2].(sql.NamedArg).Value.([]byte)[0] = 'Y'
	first[0].Completions = append(first[0].Completions, InvocationCompletion{})
	second := recorder.Snapshot()
	require.Equal(t, []byte("direct-original"), second[0].Args[0])
	require.Equal(t, sql.RawBytes("raw-original"), second[0].Args[1])
	require.Equal(t, []byte("original"), second[0].Args[2].(sql.NamedArg).Value)
	require.NotNil(t, second[0].Completions)
	require.Empty(t, second[0].Completions)

	empty := InvocationRecorder{Records: []InvocationRecord{{Completions: []InvocationCompletion{}}}}
	require.NotNil(t, empty.Snapshot()[0].Completions)
}

func TestOwnerAInvocationSnapshotPreservesNilAndEmptyArgumentShapes(t *testing.T) {
	recorder := InvocationRecorder{Records: []InvocationRecord{
		{Args: cloneInvocationArgs(nil)}, {Args: cloneInvocationArgs([]any{})},
	}}
	first := recorder.Snapshot()
	require.Nil(t, first[0].Args)
	require.NotNil(t, first[1].Args)
	require.Empty(t, first[1].Args)
	first[0].Args = []any{"changed"}
	first[1].Args = nil
	second := recorder.Snapshot()
	require.Nil(t, second[0].Args)
	require.NotNil(t, second[1].Args)
	require.Empty(t, second[1].Args)
}

func TestOwnerANonstatementCorrelationRetainsScopeOrder(t *testing.T) {
	ids := []string{"begin", "savepoint", "rollback", "release", "commit"}
	events := make([]EventRecord, 0, len(ids)*2)
	for index := len(ids) - 1; index >= 0; index-- {
		events = append(events,
			EventRecord{Event: rasql.Event{LogicalID: ids[index], ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventStart}},
			EventRecord{Event: rasql.Event{LogicalID: ids[index], ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventTerminal}},
		)
		if index == len(ids)-1 {
			events = append(events,
				EventRecord{Event: rasql.Event{LogicalID: "unattached-z", ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventStart}},
				EventRecord{Event: rasql.Event{LogicalID: "unattached-z", ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventTerminal}},
			)
		}
		if index == 2 {
			events = append(events,
				EventRecord{Event: rasql.Event{LogicalID: "unattached-a", ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventStart}},
				EventRecord{Event: rasql.Event{LogicalID: "unattached-a", ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventTerminal}},
			)
		}
	}
	records := make([]InvocationRecord, 0, len(ids))
	for ordinal, id := range ids {
		records = append(records, InvocationRecord{Ordinal: ordinal, LogicalID: id, ParentID: "txn", EventKind: rasql.EventScope, StartCount: 1, ContextMatched: true, Kind: "exec", Completions: []InvocationCompletion{{Phase: "transaction", ContextMatched: true}}})
	}
	records = []InvocationRecord{records[4], records[2], records[0], records[3], records[1]}
	set, err := correlateRASQLObservations("scope", records, events)
	require.NoError(t, err)
	require.Len(t, set.Events, len(ids)+2)
	for ordinal, event := range set.Events[:len(ids)] {
		require.Equal(t, ids[ordinal], event.LogicalID)
		require.Equal(t, "txn", event.ParentID)
		require.Equal(t, rasql.EventScope, event.Kind)
		require.Equal(t, 0, event.StatementIndex)
		require.Equal(t, rasql.Event{LogicalID: ids[ordinal], ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventStart}, event.Start)
		require.Equal(t, rasql.Event{LogicalID: ids[ordinal], ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventTerminal}, event.Terminal)
		require.Len(t, event.Invocations, 1)
		require.Equal(t, ordinal, event.Invocations[0].Ordinal)
		require.Equal(t, ids[ordinal], event.Invocations[0].LogicalID)
		require.Equal(t, "txn", event.Invocations[0].ParentID)
		require.Equal(t, rasql.EventScope, event.Invocations[0].EventKind)
		require.Equal(t, 0, event.Invocations[0].StatementIndex)
	}
	for index, id := range []string{"unattached-z", "unattached-a"} {
		event := set.Events[len(ids)+index]
		require.Equal(t, id, event.LogicalID)
		require.Equal(t, "txn", event.ParentID)
		require.Equal(t, rasql.EventScope, event.Kind)
		require.Equal(t, 0, event.StatementIndex)
		require.Equal(t, rasql.Event{LogicalID: id, ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventStart}, event.Start)
		require.Equal(t, rasql.Event{LogicalID: id, ParentID: "txn", Kind: rasql.EventScope, Phase: rasql.EventTerminal}, event.Terminal)
		require.Empty(t, event.Invocations)
	}
}

func TestOwnerAActualObserversCorrelateIdentityAndOrdinals(t *testing.T) {
	state := &recordingDriverState{cols: []string{"id", "title"}, rows: [][]driver.Value{{int64(1), "task"}}}
	database := openRecordingDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	directPayload := []byte("direct-original")
	rawPayload := sql.RawBytes("raw-original")
	namedPayload := []byte("original")
	raw, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	invocations, events := &InvocationRecorder{}, &EventRecorder{}
	capturedContexts := make([]context.Context, 0, 2)
	capturedCompletions := make([]rasql.CompletionObserver, 0, 2)
	invocationObserver := rasql.InvocationObserverFunc(func(ctx context.Context, operation rasql.Operation) (context.Context, rasql.CompletionObserver) {
		capturedContext, capturedCompletion := invocations.Observer().Start(ctx, operation)
		capturedContexts = append(capturedContexts, capturedContext)
		capturedCompletions = append(capturedCompletions, capturedCompletion)
		return capturedContext, capturedCompletion
	})
	raw, err = raw.WithInvocationObservers(rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), invocationObserver)
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(raw, profile)
	require.NoError(t, err)
	markerSeen := false
	markerObserver := rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
		require.Equal(t, "caller", ctx.Value(ownerAObserverMarkerKey{}))
		markerSeen = true
		return events.Observer().Start(ctx, event)
	})
	executor, err = rasql.WithEventObservers(executor, rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), markerObserver)
	require.NoError(t, err)
	projection, err := rasql.NativeProjection[overdueRow](overdueDecoder{})
	require.NoError(t, err)
	query, err := rasql.Native(rasql.NativeStatement{
		Engine: "sqlite", SQL: "SELECT id, title WHERE ? = ? AND ? = ?",
		Args: []rasql.NativeArgument{
			{Value: directPayload}, {Value: rawPayload},
			{Value: sql.Named("payload", namedPayload)}, {Value: "ok"},
		},
	}, projection, rasql.Many)
	require.NoError(t, err)
	rows, err := rasql.All(context.WithValue(t.Context(), ownerAObserverMarkerKey{}, "caller"), executor, query)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	directPayload[0] = 'X'
	rawPayload[0] = 'X'
	namedPayload[0] = 'X'
	records, observedEvents := invocations.Snapshot(), events.Snapshot()
	require.Len(t, records, 2)
	require.Equal(t, 0, records[0].Ordinal)
	require.Equal(t, 1, records[1].Ordinal)
	require.Len(t, capturedContexts, 2)
	require.Len(t, capturedCompletions, 2)
	for index := range capturedContexts {
		require.NotNil(t, capturedContexts[index])
		require.NotNil(t, capturedCompletions[index])
	}
	require.Equal(t, records[0].LogicalID, records[1].LogicalID)
	require.Equal(t, []byte("direct-original"), records[0].Args[0])
	require.Equal(t, sql.RawBytes("raw-original"), records[0].Args[1])
	require.Equal(t, []byte("original"), records[0].Args[2].(sql.NamedArg).Value)
	require.True(t, records[0].ContextMatched)
	require.True(t, records[1].Completions[0].ContextMatched)
	require.Equal(t, records[0].Completions[0].Phase, records[0].Phase)
	require.Equal(t, records[0].Completions[0].Rows, records[0].Rows)
	require.Equal(t, records[0].Completions[0].EarlyClose, records[0].EarlyClose)
	require.Equal(t, records[0].Completions[0].Err, records[0].Err)
	for _, record := range records {
		require.Equal(t, records[0].LogicalID, record.LogicalID)
		require.Equal(t, records[0].ParentID, record.ParentID)
		require.Equal(t, rasql.EventStatement, record.EventKind)
		require.Equal(t, 0, record.StatementIndex)
		require.True(t, record.ContextMatched)
		require.Len(t, record.Completions, 1)
		require.True(t, record.Completions[0].ContextMatched)
	}
	require.Len(t, observedEvents, 2)
	require.Equal(t, rasql.EventStart, observedEvents[0].Event.Phase)
	require.Equal(t, rasql.EventTerminal, observedEvents[1].Event.Phase)
	set, err := correlateRASQLObservations("correlation", records, observedEvents)
	require.NoError(t, err)
	assertSingleCorrelatedStatement(t, set, records[0].LogicalID, records[0].ParentID, 0, "query", "consumption", 1, false, nil, []int{0, 1})
	first := invocations.Snapshot()
	first[0].Args[0].([]byte)[0] = 'Y'
	first[0].Args[1].(sql.RawBytes)[0] = 'Y'
	first[0].Args[2].(sql.NamedArg).Value.([]byte)[0] = 'Y'
	first[0].Completions[0].Phase = "changed"
	first[0].Completions[0].Rows = 99
	first[0].Completions[0].EarlyClose = true
	first[0].Completions[0].Err = errors.New("changed")
	first[0].Completions = append(first[0].Completions, InvocationCompletion{})
	second := invocations.Snapshot()
	require.Equal(t, []byte("direct-original"), second[0].Args[0])
	require.Equal(t, sql.RawBytes("raw-original"), second[0].Args[1])
	require.Equal(t, []byte("original"), second[0].Args[2].(sql.NamedArg).Value)
	require.Len(t, second[0].Completions, 1)
	require.Equal(t, "execution", second[0].Completions[0].Phase)
	require.Equal(t, int64(0), second[0].Completions[0].Rows)
	require.False(t, second[0].Completions[0].EarlyClose)
	require.NoError(t, second[0].Completions[0].Err)
	require.NotNil(t, capturedCompletions[0])
	secondError := errors.New("second completion")
	require.NoError(t, capturedCompletions[0].Complete(capturedContexts[0], rasql.Completion{
		Phase: rasql.ExecutionPhase, RowsRead: 9, EarlyClose: true, Err: secondError,
	}))
	third := invocations.Snapshot()
	require.Len(t, third[0].Completions, 2)
	require.Equal(t, InvocationCompletion{Phase: "execution", ContextMatched: true}, third[0].Completions[0])
	require.Equal(t, InvocationCompletion{
		Phase: "execution", Rows: 9, EarlyClose: true, Err: secondError, ContextMatched: true,
	}, third[0].Completions[1])
	require.Equal(t, third[0].Completions[0].Phase, third[0].Phase)
	require.Equal(t, third[0].Completions[0].Rows, third[0].Rows)
	require.Equal(t, third[0].Completions[0].EarlyClose, third[0].EarlyClose)
	require.Equal(t, third[0].Completions[0].Err, third[0].Err)
	require.True(t, markerSeen)

	// Operation.Args normalizes zero-argument starts to a nonnil empty slice.
	// The separate snapshot test covers the true nil and empty boundary shapes.
	shapeRecorder := &InvocationRecorder{}
	shapeRaw, err := rasql.New(database, dialect.SQLite())
	require.NoError(t, err)
	shapeRaw, err = shapeRaw.WithInvocationObservers(
		rasql.ExtensionErrorHandlerFunc(func(context.Context, rasql.ExtensionError) {}), shapeRecorder.Observer(),
	)
	require.NoError(t, err)
	shapeRows, err := shapeRaw.QueryRendered(t.Context(), stmt.New(sqltext.Text("SELECT id, title WHERE ? IS NULL"), nil))
	require.NoError(t, err)
	require.NoError(t, shapeRows.Close())
	shapeRows, err = shapeRaw.QueryRendered(t.Context(), stmt.New(sqltext.Text("SELECT id, title")))
	require.NoError(t, err)
	require.NoError(t, shapeRows.Close())
	shapeFirst := shapeRecorder.Snapshot()
	require.Len(t, shapeFirst, 2)
	require.Len(t, shapeFirst[0].Args, 1)
	require.Nil(t, shapeFirst[0].Args[0])
	require.NotNil(t, shapeFirst[1].Args)
	require.Empty(t, shapeFirst[1].Args)
	shapeFirst[0].Args[0] = "changed"
	shapeFirst[1].Args = nil
	shapeSecond := shapeRecorder.Snapshot()
	require.Len(t, shapeSecond[0].Args, 1)
	require.Nil(t, shapeSecond[0].Args[0])
	require.NotNil(t, shapeSecond[1].Args)
	require.Empty(t, shapeSecond[1].Args)
}

type ownerAObserverMarkerKey struct{}

func TestOwnerAObservedSQLContractMutation(t *testing.T) {
	base := "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at FROM tasks WHERE project_id = ? AND is_open = ? AND due_on IS NOT NULL AND due_on < ? ORDER BY id"
	mutations := map[string]string{
		"operator":      "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at FROM tasks WHERE project_id = ? AND is_open = ? AND due_on IS NOT NULL AND due_on > ? ORDER BY id",
		"projection":    "SELECT id, project_id, assignee_id, title, is_open, due_on FROM tasks WHERE project_id = ? AND is_open = ? AND due_on IS NOT NULL AND due_on < ? ORDER BY id",
		"bind position": "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at FROM tasks WHERE is_open = ? AND project_id = ? AND due_on IS NOT NULL AND due_on < ? ORDER BY id",
	}
	for _, profile := range []string{"sqlite-3.35", "postgresql-17", "mysql-8.4"} {
		for name, sqlText := range mutations {
			t.Run(profile+"/"+name, func(t *testing.T) {
				records, events := typedObservationFixture(base)
				records[0].SQL = sqlText
				records[1].SQL = sqlText
				_, err := rasqlObservations("typed_sql_report", records, events)
				require.Error(t, err)
			})
		}
	}
}

func TestOwnerAStrictParityMatrix(t *testing.T) {
	cases := map[string]func(*statementObservation){
		"role":         func(value *statementObservation) { value.Role = roleReport },
		"verification": func(value *statementObservation) { value.Verification = true },
		"parent":       func(value *statementObservation) { value.LogicalParent = "other" },
		"index":        func(value *statementObservation) { value.StatementIndex++ },
		"kind":         func(value *statementObservation) { value.Kind = "exec" },
		"arguments":    func(value *statementObservation) { value.Args = []any{int64(2)} },
		"rows":         func(value *statementObservation) { value.RowsConsumed++ },
		"early close":  func(value *statementObservation) { value.EarlyClose = true },
		"error class":  func(value *statementObservation) { value.Err = errors.New("constraint violation") },
		"row values":   func(value *statementObservation) { value.RowValues[0][0] = int64(2) },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			left := strictParityEvidence()
			right := strictParityEvidence()
			mutate(&right.Observations[0])
			require.Error(t, CompareWorkloadEvidence(name, left, right))
		})
	}
}

func TestOwnerAVerificationEvidence(t *testing.T) {
	measured := strictParityEvidence().Observations[0]
	verification := measured
	verification.Verification = true
	verification.Role = roleVerification
	verification.LogicalParent = "verify"
	verification.StatementIndex = 0
	verification.RowValues = [][]any{{int64(9)}}
	base := parityEvidence{
		ResultJSON: []byte(`{"id":1}`), Outcome: "ok", RowsReturned: 1, RowsConsumed: 2,
		MeasuredRowsConsumed: 1, VerificationRowsConsumed: 1,
		Observations: []statementObservation{measured, verification},
	}
	require.NoError(t, CompareWorkloadEvidence("verification", base, base))
	missing := base
	missing.Observations = []statementObservation{measured}
	missing.VerificationRowsConsumed = 0
	missing.RowsConsumed = 1
	require.Error(t, CompareWorkloadEvidence("verification", base, missing))
	changed := base
	changed.Observations = append([]statementObservation(nil), base.Observations...)
	changed.Observations[1].RowValues = cloneRows(base.Observations[1].RowValues)
	changed.Observations[1].RowValues[0][0] = int64(10)
	changed.Observations[1].SQL = "SELECT changed"
	require.NotEqual(t, base.Observations[1].SQL, changed.Observations[1].SQL)
	require.Error(t, CompareWorkloadEvidence("verification", base, changed))
}

func correlatedObservationFixture() ([]InvocationRecord, []EventRecord) {
	records := []InvocationRecord{
		correlatedInvocation("first", "parent", 0, "SELECT first", "execution"),
		correlatedInvocation("first", "parent", 0, "SELECT first", "consumption"),
		correlatedInvocation("second", "parent", 1, "SELECT second", "execution"),
		correlatedInvocation("second", "parent", 1, "SELECT second", "consumption"),
	}
	for index := range records {
		records[index].Ordinal = index
	}
	events := []EventRecord{
		{Event: rasql.Event{LogicalID: "first", ParentID: "parent", Kind: rasql.EventStatement, Phase: rasql.EventStart, StatementIndex: 0}},
		{Event: rasql.Event{LogicalID: "first", ParentID: "parent", Kind: rasql.EventStatement, Phase: rasql.EventTerminal, StatementIndex: 0, Rows: 1}},
		{Event: rasql.Event{LogicalID: "second", ParentID: "parent", Kind: rasql.EventStatement, Phase: rasql.EventStart, StatementIndex: 1}},
		{Event: rasql.Event{LogicalID: "second", ParentID: "parent", Kind: rasql.EventStatement, Phase: rasql.EventTerminal, StatementIndex: 1, Rows: 1}},
	}
	return records, events
}

func correlatedInvocation(id, parent string, index int, sqlText, phase string) InvocationRecord {
	rows := int64(1)
	if phase == "execution" {
		rows = 0
	}
	return InvocationRecord{
		LogicalID: id, ParentID: parent, EventKind: rasql.EventStatement, StatementIndex: index,
		StartCount: 1, ContextMatched: true, Kind: "query", SQL: sqlText, Phase: phase, Rows: rows,
		Completions: []InvocationCompletion{{Phase: phase, Rows: rows, ContextMatched: true}},
	}
}

func typedObservationFixture(sqlText string) ([]InvocationRecord, []EventRecord) {
	records := []InvocationRecord{
		correlatedInvocation("typed", "typed", 0, sqlText, "execution"),
		correlatedInvocation("typed", "typed", 0, sqlText, "consumption"),
	}
	records[0].Ordinal = 0
	records[1].Ordinal = 1
	records[1].Rows = 2
	records[1].Completions[0].Rows = 2
	return records, []EventRecord{
		{Event: rasql.Event{LogicalID: "typed", ParentID: "typed", Kind: rasql.EventStatement, Phase: rasql.EventStart, StatementIndex: 0}},
		{Event: rasql.Event{LogicalID: "typed", ParentID: "typed", Kind: rasql.EventStatement, Phase: rasql.EventTerminal, StatementIndex: 0, Rows: 2}},
	}
}
