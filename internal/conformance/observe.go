package conformance

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"

	"github.com/lestrrat-go/rasql"
)

type observationIdentity struct {
	LogicalID      string
	ParentID       string
	StatementIndex int
	Kind           rasql.EventKind
}

type observationIdentityKey struct{}
type cancellationStatementKey struct{}
type cancellationTraceKey struct{}

type InvocationCompletion struct {
	Phase          string
	Rows           int64
	EarlyClose     bool
	Err            error
	ContextMatched bool
}

type InvocationRecord struct {
	Ordinal        int
	LogicalID      string
	ParentID       string
	EventKind      rasql.EventKind
	StatementIndex int
	StartCount     int
	Completions    []InvocationCompletion
	ContextMatched bool
	SQL            string
	Args           []any
	Kind           string
	Phase          string
	Rows           int64
	EarlyClose     bool
	Err            error
}
type InvocationRecorder struct {
	mu      sync.Mutex
	Records []InvocationRecord
}

func (r *InvocationRecorder) Observer() rasql.InvocationObserver {
	return rasql.InvocationObserverFunc(func(ctx context.Context, operation rasql.Operation) (context.Context, rasql.CompletionObserver) {
		identity, hasIdentity := ctx.Value(observationIdentityKey{}).(observationIdentity)
		record := InvocationRecord{
			LogicalID: identity.LogicalID, ParentID: identity.ParentID,
			EventKind:      identity.Kind,
			StatementIndex: identity.StatementIndex, StartCount: 1, ContextMatched: hasIdentity,
			SQL: operation.SQL(), Args: cloneInvocationArgs(operation.Args()), Kind: operation.Kind().String(),
		}
		r.mu.Lock()
		record.Ordinal = len(r.Records)
		r.Records = append(r.Records, record)
		index := len(r.Records) - 1
		r.mu.Unlock()
		return ctx, rasql.CompletionObserverFunc(func(ctx context.Context, completion rasql.Completion) error {
			completionIdentity, matched := ctx.Value(observationIdentityKey{}).(observationIdentity)
			r.mu.Lock()
			record := &r.Records[index]
			phase := phaseName(completion.Phase)
			record.Completions = append(record.Completions, InvocationCompletion{
				Phase: phase, Rows: completion.RowsRead, EarlyClose: completion.EarlyClose,
				Err: completion.Err, ContextMatched: matched && reflect.DeepEqual(identity, completionIdentity),
			})
			if len(record.Completions) == 1 {
				record.Phase = phase
				record.Rows = completion.RowsRead
				record.EarlyClose = completion.EarlyClose
				record.Err = completion.Err
			}
			r.mu.Unlock()
			return nil
		})
	})
}
func (r *InvocationRecorder) Snapshot() []InvocationRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]InvocationRecord, len(r.Records))
	copy(out, r.Records)
	for i := range out {
		out[i].Args = cloneInvocationArgs(out[i].Args)
		if out[i].Completions != nil {
			out[i].Completions = make([]InvocationCompletion, len(out[i].Completions))
			copy(out[i].Completions, r.Records[i].Completions)
		}
	}
	return out
}
func phaseName(phase rasql.Phase) string {
	switch phase {
	case rasql.ExecutionPhase:
		return "execution"
	case rasql.ConsumptionPhase:
		return "consumption"
	case rasql.TransactionPhase:
		return "transaction"
	default:
		return "unknown"
	}
}

type EventRecord struct{ Event rasql.Event }
type cancellationTrace struct {
	Started        []int
	Target         int
	TargetCanceled bool
	GraphCanceled  bool
}
type EventRecorder struct {
	mu      sync.Mutex
	Records []EventRecord
}

func (r *EventRecorder) Observer() rasql.EventObserver {
	return rasql.EventObserverFunc(func(ctx context.Context, event rasql.Event) (context.Context, rasql.EventCompletion) {
		r.mu.Lock()
		r.Records = append(r.Records, EventRecord{Event: event})
		r.mu.Unlock()
		if event.Phase != rasql.EventStart {
			return ctx, rasql.EventCompletionFunc(func(ctx context.Context, terminal rasql.Event) error {
				r.mu.Lock()
				r.Records = append(r.Records, EventRecord{Event: terminal})
				r.mu.Unlock()
				return nil
			})
		}
		trace, tracing := ctx.Value(cancellationTraceKey{}).(*cancellationTrace)
		if tracing && event.Kind == rasql.EventStatement {
			trace.Started = append(trace.Started, event.StatementIndex)
		}
		identity := observationIdentity{LogicalID: event.LogicalID, ParentID: event.ParentID, StatementIndex: event.StatementIndex, Kind: event.Kind}
		callCtx := context.WithValue(ctx, observationIdentityKey{}, identity)
		if target, ok := ctx.Value(cancellationStatementKey{}).(int); ok && event.Kind == rasql.EventStatement && event.StatementIndex == target {
			canceledContext, cancel := context.WithCancel(callCtx)
			cancel()
			callCtx = canceledContext
		}
		return callCtx, rasql.EventCompletionFunc(func(ctx context.Context, terminal rasql.Event) error {
			if tracing && terminal.Kind == rasql.EventStatement && terminal.StatementIndex == trace.Target {
				trace.TargetCanceled = errors.Is(terminal.Err, context.Canceled)
			}
			if tracing && terminal.Kind == rasql.EventGraph {
				trace.GraphCanceled = errors.Is(terminal.Err, context.Canceled)
			}
			r.mu.Lock()
			r.Records = append(r.Records, EventRecord{Event: terminal})
			r.mu.Unlock()
			return nil
		})
	})
}

func cloneInvocationArgs(args []any) []any {
	if args == nil {
		return nil
	}
	result := make([]any, len(args))
	for index, value := range args {
		result[index] = cloneInvocationArg(value)
	}
	return result
}

func cloneInvocationArg(value any) any {
	switch typed := value.(type) {
	case []byte:
		if typed == nil {
			return []byte(nil)
		}
		copyValue := make([]byte, len(typed))
		copy(copyValue, typed)
		return copyValue
	case sql.RawBytes:
		if typed == nil {
			return sql.RawBytes(nil)
		}
		copyValue := make(sql.RawBytes, len(typed))
		copy(copyValue, typed)
		return copyValue
	case sql.NamedArg:
		typed.Value = cloneInvocationArg(typed.Value)
		return typed
	case []any:
		return cloneInvocationArgs(typed)
	default:
		return value
	}
}

func (r InvocationRecord) validate() error {
	if r.Ordinal < 0 {
		return fmt.Errorf("invocation has negative ordinal %d", r.Ordinal)
	}
	if r.StartCount != 1 {
		return fmt.Errorf("invocation %d has start count %d", r.Ordinal, r.StartCount)
	}
	if r.LogicalID == "" {
		return fmt.Errorf("invocation %d has no logical identity (%s %q)", r.Ordinal, r.Kind, r.SQL)
	}
	if !r.ContextMatched {
		return fmt.Errorf("invocation %d lost returned event context", r.Ordinal)
	}
	if r.EventKind != rasql.EventScope && r.EventKind != rasql.EventStatement && r.EventKind != rasql.EventMutationBatch && r.EventKind != rasql.EventGraph {
		return fmt.Errorf("invocation %d has unknown event kind %d", r.Ordinal, r.EventKind)
	}
	if len(r.Completions) != 1 {
		return fmt.Errorf("invocation %d has %d completions", r.Ordinal, len(r.Completions))
	}
	if !r.Completions[0].ContextMatched {
		return fmt.Errorf("invocation %d lost returned event context", r.Ordinal)
	}
	return nil
}
func (r *EventRecorder) Snapshot() []EventRecord {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]EventRecord(nil), r.Records...)
}
