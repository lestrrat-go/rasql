package rasql

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

// ownedRows owns a query result and completes its consumption lifecycle once.
type ownedRows struct {
	rows       *sql.Rows
	db         DB
	operation  Operation
	invocation invocation
	mu         sync.Mutex
	exhausted  bool
	closed     bool
	completed  bool
	iterErr    error
	scanErr    error
	columnErr  error
	closeErr   error
	rowsRead   int64
}

// Columns returns the result column names and records any error for Finish.
func (r *ownedRows) Columns() ([]string, error) {
	if r == nil || r.rows == nil {
		return nil, nil
	}
	columns, err := r.rows.Columns()
	if err != nil {
		r.mu.Lock()
		r.columnErr = errors.Join(r.columnErr, err)
		r.mu.Unlock()
	}
	return columns, err
}

// Next advances to the next result row. Exhaustion records iteration errors
// and closes the underlying rows, while Finish remains the completion owner.
func (r *ownedRows) Next() bool {
	if r == nil || r.rows == nil {
		if r != nil {
			r.mu.Lock()
			r.exhausted = true
			r.mu.Unlock()
		}
		return false
	}
	if !r.rows.Next() {
		err := r.rows.Err()
		closeErr := r.rows.Close()
		r.mu.Lock()
		r.exhausted = true
		r.closed = true
		r.iterErr = errors.Join(r.iterErr, err)
		r.closeErr = errors.Join(r.closeErr, closeErr)
		r.mu.Unlock()
		return false
	}
	return true
}

// Scan scans the current row and records any conversion error for Finish.
func (r *ownedRows) Scan(destinations ...any) error {
	if r == nil || r.rows == nil {
		return nil
	}
	err := r.rows.Scan(destinations...)
	if err != nil {
		r.mu.Lock()
		r.scanErr = errors.Join(r.scanErr, err)
		r.mu.Unlock()
	}
	return err
}

// Close closes rows and completes consumption as an early close when needed.
func (r *ownedRows) Close() error {
	if r == nil {
		return nil
	}
	return r.Finish(nil, true)
}

// Err returns the recorded iteration error without completing consumption.
func (r *ownedRows) Err() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	err := r.iterErr
	r.mu.Unlock()
	return err
}

// RecordRow increments the count of successfully decoded rows.
func (r *ownedRows) RecordRow() {
	if r == nil {
		return
	}
	r.mu.Lock()
	if !r.completed {
		r.rowsRead++
	}
	r.mu.Unlock()
}

// Finish completes consumption exactly once. Decoder integrations use err for
// conversion or cardinality failures and earlyClose for consumer early stop.
func (r *ownedRows) Finish(err error, earlyClose bool) error {
	if r == nil {
		return err
	}
	r.mu.Lock()
	if r.completed {
		r.mu.Unlock()
		return err
	}
	r.completed = true
	if !r.closed && r.rows != nil {
		r.closed = true
		r.mu.Unlock()
		closeErr := r.rows.Close()
		r.mu.Lock()
		r.closeErr = errors.Join(r.closeErr, closeErr)
	}
	completionErr := errors.Join(err, r.columnErr, r.scanErr, r.iterErr, r.closeErr)
	rowsRead := r.rowsRead
	wasExhausted := r.exhausted
	r.mu.Unlock()
	r.db.completeInvocation(r.invocation, r.operation, ConsumptionPhase, contextFromInvocation(r.invocation), completionErr, rowsRead, earlyClose && !wasExhausted)
	return completionErr
}

func contextFromInvocation(invocation invocation) context.Context {
	if len(invocation.contexts) == 0 {
		return context.Background()
	}
	return invocation.contexts[len(invocation.contexts)-1]
}
