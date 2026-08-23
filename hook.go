package rasql

import (
	"context"
	"errors"
	"fmt"

	"github.com/lestrrat-go/rasql/stmt"
)

// OperationKind identifies the database/sql method an Operation wraps.
type OperationKind uint8

const (
	// QueryOperation identifies a statement executed through QueryContext.
	QueryOperation OperationKind = iota
	// ExecOperation identifies a statement executed through ExecContext.
	ExecOperation
)

// String returns the short name used for the operation kind.
func (k OperationKind) String() string {
	switch k {
	case QueryOperation:
		return "query"
	case ExecOperation:
		return "exec"
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

func appendHooks(current []Hook, additions []Hook) ([]Hook, error) {
	if len(additions) == 0 {
		return append([]Hook(nil), current...), nil
	}
	hooks := make([]Hook, 0, len(current)+len(additions))
	hooks = append(hooks, current...)
	for _, hook := range additions {
		if isNil(hook) {
			return nil, fmt.Errorf("rasql: hook must not be nil")
		}
		hooks = append(hooks, hook)
	}
	return hooks, nil
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

func afterHooks(ctx context.Context, operation Operation, entered []Hook, err error) error {
	for index := len(entered) - 1; index >= 0; index-- {
		if hookErr := entered[index].After(ctx, operation, err); hookErr != nil {
			err = errors.Join(err, fmt.Errorf("rasql: hook after %s: %w", operation.Kind(), hookErr))
		}
	}
	return err
}
