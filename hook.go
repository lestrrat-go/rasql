package rasql

import "github.com/lestrrat-go/rasql/exec"

// OperationKind identifies the database/sql method an Operation wraps.
type OperationKind = exec.OperationKind

const (
	// QueryOperation identifies a statement executed through QueryContext.
	QueryOperation = exec.QueryOperation
	// ExecOperation identifies a statement executed through ExecContext.
	ExecOperation = exec.ExecOperation
	// BeginOperation identifies a transaction begin.
	BeginOperation = exec.BeginOperation
	// CommitOperation identifies a transaction commit.
	CommitOperation = exec.CommitOperation
	// RollbackOperation identifies a transaction rollback.
	RollbackOperation = exec.RollbackOperation
)

// Operation is the immutable, rendered statement passed to a Hook.
//
// Args returns a copy, so a hook can inspect bound values without changing
// what reaches database/sql. Hooks cannot replace the SQL or its arguments.
type Operation = exec.Operation

// Hook observes and optionally rejects rendered database operations. New
// observation code should use Observer; Hook.After remains for compatibility.
//
// Before methods run in registration order. After methods run in reverse
// registration order and receive the execution or hook error, if any. A
// non-nil error from Before prevents database/sql from being called. A
// non-nil error from After is returned to the caller and joined with any
// earlier error.
type Hook = exec.Hook

// HookFunc adapts functions into a Hook. Either function may be nil.
type HookFunc = exec.HookFunc

// ExtensionError reports a failure in a hook or observer after the database operation.
type ExtensionError = exec.ExtensionError

// ExtensionErrorHandler receives extension failures independently from the operation result.
type ExtensionErrorHandler = exec.ExtensionErrorHandler

// ExtensionErrorHandlerFunc adapts a function into an ExtensionErrorHandler.
type ExtensionErrorHandlerFunc = exec.ExtensionErrorHandlerFunc

// Observer receives the driver error after an operation.
type Observer = exec.Observer

// ObserverFunc adapts a function into an Observer.
type ObserverFunc = exec.ObserverFunc

// Phase identifies a lifecycle phase.
type Phase = exec.Phase

const (
	ExecutionPhase   = exec.ExecutionPhase
	ConsumptionPhase = exec.ConsumptionPhase
	TransactionPhase = exec.TransactionPhase
)

// Completion describes one completed operation lifecycle phase.
type Completion = exec.Completion

// InvocationObserver observes a complete operation lifecycle.
type InvocationObserver = exec.InvocationObserver

// InvocationObserverFunc adapts a function into an InvocationObserver.
type InvocationObserverFunc = exec.InvocationObserverFunc

// CompletionObserver receives a terminal lifecycle event.
type CompletionObserver = exec.CompletionObserver

// CompletionObserverFunc adapts a function into a CompletionObserver.
type CompletionObserverFunc = exec.CompletionObserverFunc
