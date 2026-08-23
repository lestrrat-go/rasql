package rasql

import "github.com/lestrrat-go/rasql/exec"

// OperationKind identifies the database/sql method an Operation wraps.
type OperationKind = exec.OperationKind

const (
	// QueryOperation identifies a statement executed through QueryContext.
	QueryOperation = exec.QueryOperation
	// ExecOperation identifies a statement executed through ExecContext.
	ExecOperation = exec.ExecOperation
)

// Operation is the immutable, rendered statement passed to a Hook.
//
// Args returns a copy, so a hook can inspect bound values without changing
// what reaches database/sql. Hooks cannot replace the SQL or its arguments.
type Operation = exec.Operation

// Hook observes and optionally rejects rendered database operations.
//
// Before methods run in registration order. After methods run in reverse
// registration order and receive the execution or hook error, if any. A
// non-nil error from Before prevents database/sql from being called. A
// non-nil error from After is returned to the caller and joined with any
// earlier error.
type Hook = exec.Hook

// HookFunc adapts functions into a Hook. Either function may be nil.
type HookFunc = exec.HookFunc
