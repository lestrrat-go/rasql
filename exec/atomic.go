package exec

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"sync/atomic"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

// AtomicFunc runs one repository operation inside an owned transaction or
// savepoint.
type AtomicFunc func(context.Context, DB) error

// AtomicPanic preserves a callback panic when cleanup also fails.
type AtomicPanic struct {
	Value   any
	Cleanup error
}

func (p AtomicPanic) Error() string {
	return fmt.Sprintf("rasql: atomic callback panic cleanup failed: %v", p.Cleanup)
}

// Unwrap returns the cleanup error reported while handling the panic.
func (p AtomicPanic) Unwrap() error { return p.Cleanup }

var atomicSavepointID atomic.Uint64

type atomicCallbackResult struct {
	err      error
	panicked bool
	value    any
}

// Atomic owns the transaction boundary for fn. It opens a transaction on a
// regular DB and uses a private savepoint when db already runs in a transaction.
func (db DB) Atomic(ctx context.Context, opts *sql.TxOptions, fn AtomicFunc) error {
	if err := db.valid(); err != nil {
		return err
	}
	if fn == nil {
		return fmt.Errorf("rasql: atomic function must not be nil")
	}
	if db.tx != nil {
		return db.atomicSavepoint(ctx, opts, fn)
	}
	return db.atomicTransaction(ctx, opts, fn)
}

func (db DB) atomicTransaction(ctx context.Context, opts *sql.TxOptions, fn AtomicFunc) error {
	transaction, err := db.Begin(ctx, opts)
	if err != nil {
		return err
	}
	result := runAtomicCallback(ctx, transaction, fn)
	if result.panicked {
		cleanupErr := transaction.Rollback()
		panicAtomic(result.value, cleanupErr)
	}
	if result.err != nil {
		return errors.Join(result.err, transaction.Rollback())
	}
	return transaction.Commit()
}

func (db DB) atomicSavepoint(ctx context.Context, opts *sql.TxOptions, fn AtomicFunc) error {
	if opts != nil {
		return fmt.Errorf("rasql: nested atomic scope does not accept transaction options")
	}
	if !db.dialect.Supports(dialect.CapabilitySavepoint) {
		return fmt.Errorf("rasql: dialect %s does not support savepoints", db.dialect.Name())
	}
	name, err := db.atomicSavepointName()
	if err != nil {
		return err
	}
	if _, err := db.ExecRendered(ctx, stmt.New(sqltext.Text("SAVEPOINT "+name))); err != nil {
		return err
	}
	result := runAtomicCallback(ctx, db, fn)
	if result.panicked {
		cleanupErr := db.atomicRollbackSavepoint(ctx, name)
		cleanupErr = errors.Join(cleanupErr, db.atomicReleaseSavepoint(ctx, name))
		panicAtomic(result.value, cleanupErr)
	}
	if result.err != nil {
		cleanupErr := db.atomicRollbackSavepoint(ctx, name)
		cleanupErr = errors.Join(cleanupErr, db.atomicReleaseSavepoint(ctx, name))
		return errors.Join(result.err, cleanupErr)
	}
	return db.atomicReleaseSavepoint(ctx, name)
}

func runAtomicCallback(ctx context.Context, db DB, fn AtomicFunc) (result atomicCallbackResult) {
	defer func() {
		if value := recover(); value != nil {
			result.panicked = true
			result.value = value
		}
	}()
	result.err = fn(ctx, db)
	return result
}

func panicAtomic(value any, cleanup error) {
	if cleanup == nil {
		panic(value)
	}
	panic(AtomicPanic{Value: value, Cleanup: cleanup})
}

func (db DB) atomicSavepointName() (string, error) {
	quoted, err := db.dialect.QuoteIdentifier("rasql_sp_" + strconv.FormatUint(atomicSavepointID.Add(1), 36))
	if err != nil {
		return "", fmt.Errorf("rasql: quote savepoint name: %w", err)
	}
	return quoted, nil
}

func (db DB) atomicRollbackSavepoint(ctx context.Context, name string) error {
	_, err := db.ExecRendered(ctx, stmt.New(sqltext.Text("ROLLBACK TO SAVEPOINT "+name)))
	return err
}

func (db DB) atomicReleaseSavepoint(ctx context.Context, name string) error {
	_, err := db.ExecRendered(ctx, stmt.New(sqltext.Text("RELEASE SAVEPOINT "+name)))
	return err
}
