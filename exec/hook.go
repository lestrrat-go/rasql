package exec

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/lestrrat-go/rasql/internal/nilcheck"
	"github.com/lestrrat-go/rasql/stmt"
)

// OperationKind identifies the database/sql method an Operation wraps.
type OperationKind uint8

const (
	// QueryOperation identifies a statement executed through QueryContext.
	QueryOperation OperationKind = iota
	// ExecOperation identifies a statement executed through ExecContext.
	ExecOperation
	// BeginOperation identifies a transaction begin.
	BeginOperation
	// CommitOperation identifies a transaction commit.
	CommitOperation
	// RollbackOperation identifies a transaction rollback.
	RollbackOperation
)

// String returns the short name used for the operation kind.
func (k OperationKind) String() string {
	switch k {
	case QueryOperation:
		return "query"
	case ExecOperation:
		return "exec"
	case BeginOperation:
		return "begin"
	case CommitOperation:
		return "commit"
	case RollbackOperation:
		return "rollback"
	default:
		return "unknown"
	}
}

// Operation is the immutable, rendered statement passed to a Hook.
//
// Args returns a copy, so a hook can inspect bound values without changing
// what reaches database/sql. Hooks cannot replace the SQL or its arguments.
type Operation struct {
	kind OperationKind
	stmt stmt.Statement
}

// Kind reports whether the operation is a query or an exec.
func (o Operation) Kind() OperationKind {
	return o.kind
}

// SQL returns the bound SQL text exactly as it will be sent to database/sql.
func (o Operation) SQL() string {
	return o.stmt.SQL()
}

// Args returns a copy of the bound arguments in placeholder order.
func (o Operation) Args() []any {
	return o.stmt.Args()
}

// Hook observes and optionally rejects rendered database operations.
//
// Before methods run in registration order. After methods run in reverse
// registration order and receive the execution or hook error, if any. A
// non-nil error from Before prevents database/sql from being called. A
// non-nil error from After is returned to the caller and joined with any
// earlier error.
type Hook interface {
	Before(context.Context, Operation) error
	// After is retained for compatibility. New observation code should use
	// Observer, because this method's error shares the execution result channel.
	After(context.Context, Operation, error) error
}

// HookFunc adapts functions into a Hook. Either function may be nil.
type HookFunc struct {
	BeforeFunc func(context.Context, Operation) error
	AfterFunc  func(context.Context, Operation, error) error
}

// Before implements Hook.
func (h HookFunc) Before(ctx context.Context, operation Operation) error {
	if h.BeforeFunc == nil {
		return nil
	}
	return h.BeforeFunc(ctx, operation)
}

// After implements Hook.
func (h HookFunc) After(ctx context.Context, operation Operation, err error) error {
	if h.AfterFunc == nil {
		return nil
	}
	return h.AfterFunc(ctx, operation, err)
}

// ExtensionError reports failures in hooks or observers that ran after the
// database operation. The operation result remains available when execution
// succeeded.
type ExtensionError struct {
	Operation Operation
	Errors    []error
	succeeded bool
}

// Error implements error.
func (e *ExtensionError) Error() string {
	if e == nil {
		return "<nil>"
	}
	return fmt.Sprintf("rasql: %s extension error: %v", e.Operation.Kind(), errors.Join(e.Errors...))
}

// Unwrap exposes every extension error to errors.Is and errors.As.
func (e *ExtensionError) Unwrap() []error {
	if e == nil {
		return nil
	}
	return e.Errors
}

// ExecutionSucceeded reports whether the driver operation succeeded.
func (e *ExtensionError) ExecutionSucceeded() bool {
	return e != nil && e.succeeded
}

// ExtensionErrorHandler receives extension failures independently from the
// operation's driver result.
type ExtensionErrorHandler interface {
	HandleExtensionError(context.Context, ExtensionError)
}

// ExtensionErrorHandlerFunc adapts a function into an ExtensionErrorHandler.
type ExtensionErrorHandlerFunc func(context.Context, ExtensionError)

// HandleExtensionError implements ExtensionErrorHandler.
func (f ExtensionErrorHandlerFunc) HandleExtensionError(ctx context.Context, err ExtensionError) {
	if f != nil {
		f(ctx, err)
	}
}

// Observer receives the driver error after an operation. Its own errors are
// extension failures and do not alter the operation result.
type Observer interface {
	Observe(context.Context, Operation, error) error
}

// ObserverFunc adapts a function into an Observer.
type ObserverFunc func(context.Context, Operation, error) error

// Observe implements Observer.
func (f ObserverFunc) Observe(ctx context.Context, operation Operation, err error) error {
	if f == nil {
		return nil
	}
	return f(ctx, operation, err)
}

// Phase identifies the lifecycle phase represented by a Completion.
type Phase uint8

const (
	// ExecutionPhase reports the driver call.
	ExecutionPhase Phase = iota
	// ConsumptionPhase reports row iteration and decoding.
	ConsumptionPhase
	// TransactionPhase reports begin, commit, or rollback.
	TransactionPhase
)

// Completion describes one completed operation lifecycle phase.
type Completion struct {
	Operation  Operation
	Phase      Phase
	Started    time.Time
	Finished   time.Time
	Err        error
	RowsRead   int64
	EarlyClose bool
}

// InvocationObserver starts an independent lifecycle observation session.
type InvocationObserver interface {
	Start(context.Context, Operation) (context.Context, CompletionObserver)
}

// CompletionObserver receives the terminal event for an invocation session.
type CompletionObserver interface {
	Complete(context.Context, Completion) error
}

// InvocationObserverFunc adapts a function into an InvocationObserver.
type InvocationObserverFunc func(context.Context, Operation) (context.Context, CompletionObserver)

// Start implements InvocationObserver.
func (f InvocationObserverFunc) Start(ctx context.Context, operation Operation) (context.Context, CompletionObserver) {
	if f == nil {
		return ctx, nil
	}
	return f(ctx, operation)
}

// CompletionObserverFunc adapts a function into a CompletionObserver.
type CompletionObserverFunc func(context.Context, Completion) error

// Complete implements CompletionObserver.
func (f CompletionObserverFunc) Complete(ctx context.Context, completion Completion) error {
	if f == nil {
		return nil
	}
	return f(ctx, completion)
}

func appendHooks(current []Hook, additions []Hook) ([]Hook, error) {
	if len(additions) == 0 {
		return append([]Hook(nil), current...), nil
	}
	hooks := make([]Hook, 0, len(current)+len(additions))
	hooks = append(hooks, current...)
	for _, hook := range additions {
		if nilcheck.Is(hook) {
			return nil, fmt.Errorf("rasql: hook must not be nil")
		}
		hooks = append(hooks, hook)
	}
	return hooks, nil
}

func appendObservers(current []Observer, additions []Observer) ([]Observer, error) {
	observers := make([]Observer, 0, len(current)+len(additions))
	observers = append(observers, current...)
	for _, observer := range additions {
		if nilcheck.Is(observer) {
			return nil, fmt.Errorf("rasql: observer must not be nil")
		}
		observers = append(observers, observer)
	}
	return observers, nil
}

func appendInvocationObservers(current []InvocationObserver, additions []InvocationObserver) ([]InvocationObserver, error) {
	observers := make([]InvocationObserver, 0, len(current)+len(additions))
	observers = append(observers, current...)
	for _, observer := range additions {
		if nilcheck.Is(observer) {
			return nil, fmt.Errorf("rasql: invocation observer must not be nil")
		}
		observers = append(observers, observer)
	}
	return observers, nil
}

func (db DB) beforeHooks(ctx context.Context, operation Operation) ([]Hook, error) {
	entered := make([]Hook, 0, len(db.hooks))
	for _, hook := range db.hooks {
		if err := hook.Before(ctx, operation); err != nil {
			return entered, fmt.Errorf("rasql: hook before %s: %w", operation.Kind(), err)
		}
		entered = append(entered, hook)
	}
	return entered, nil
}

func (db DB) observe(ctx context.Context, operation Operation, driverErr error) {
	for _, observer := range db.observers {
		if err := observer.Observe(ctx, operation, driverErr); err != nil {
			db.reportExtensionError(ctx, ExtensionError{Operation: operation, Errors: []error{err}, succeeded: driverErr == nil})
		}
	}
}

func (db DB) reportExtensionError(ctx context.Context, extensionErr ExtensionError) {
	if db.extensionErrorHandler != nil {
		db.extensionErrorHandler.HandleExtensionError(ctx, extensionErr)
	}
}

type invocation struct {
	contexts    []context.Context
	completions []CompletionObserver
	started     time.Time
}

func (db DB) startInvocation(ctx context.Context, operation Operation) (context.Context, invocation) {
	current := ctx
	started := time.Now()
	result := invocation{started: started}
	for _, observer := range db.invocationObservers {
		derived, completion := observer.Start(current, operation)
		if derived == nil {
			derived = current
		}
		current = derived
		result.contexts = append(result.contexts, current)
		result.completions = append(result.completions, completion)
	}
	return current, result
}

func (db DB) completeInvocation(invocation invocation, operation Operation, phase Phase, ctx context.Context, err error, rowsRead int64, earlyClose bool) {
	completion := Completion{
		Operation:  operation,
		Phase:      phase,
		Started:    invocation.started,
		Finished:   time.Now(),
		Err:        err,
		RowsRead:   rowsRead,
		EarlyClose: earlyClose,
	}
	for index := len(invocation.completions) - 1; index >= 0; index-- {
		observer := invocation.completions[index]
		if observer == nil {
			continue
		}
		observerContext := ctx
		if index < len(invocation.contexts) && invocation.contexts[index] != nil {
			observerContext = invocation.contexts[index]
		}
		if observerErr := observer.Complete(observerContext, completion); observerErr != nil {
			db.reportExtensionError(ctx, ExtensionError{Operation: operation, Errors: []error{observerErr}, succeeded: err == nil})
		}
	}
}

func (db DB) afterHooks(ctx context.Context, operation Operation, entered []Hook, err error) *ExtensionError {
	var extensionErr *ExtensionError
	for index := len(entered) - 1; index >= 0; index-- {
		if hookErr := entered[index].After(ctx, operation, err); hookErr != nil {
			wrapped := fmt.Errorf("rasql: hook after %s: %w", operation.Kind(), hookErr)
			if db.extensionErrorHandler != nil {
				db.reportExtensionError(ctx, ExtensionError{Operation: operation, Errors: []error{wrapped}, succeeded: err == nil})
				continue
			}
			if extensionErr == nil {
				extensionErr = &ExtensionError{Operation: operation, succeeded: err == nil}
			}
			extensionErr.Errors = append(extensionErr.Errors, wrapped)
		}
	}
	return extensionErr
}
