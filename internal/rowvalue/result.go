package rowvalue

import (
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"sync"
)

// Result owns one lazily opened database/sql cursor and its immutable header.
type Result struct {
	mu           sync.Mutex
	open         func() (*sql.Rows, error)
	rows         *sql.Rows
	opened       bool
	closed       bool
	busy         bool
	claimed      bool
	header       *columnHeader
	headerErr    error
	closeErr     error
	errorYielded bool
	executionErr error
}

// NewResult creates a lazy result whose opener runs at most once.
func NewResult(open func() (*sql.Rows, error)) *Result {
	return &Result{open: open}
}

// ScanResult takes ownership of rows immediately without executing anything.
func ScanResult(rows *sql.Rows) *Result {
	return &Result{rows: rows}
}

// Header opens the cursor when needed and returns its ordered columns.
func (r *Result) Header() (Header, error) {
	if r == nil {
		return Header{}, nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy {
		return Header{}, ErrConcurrentUse
	}
	if r.closed {
		return Header{columns: r.header}, r.headerErr
	}
	if err := r.openLocked(); err != nil {
		return Header{}, err
	}
	return Header{columns: r.header}, r.headerErr
}

// Rows returns the single-use sequence for this result.
func (r *Result) Rows() iter.Seq2[Row, error] {
	if r == nil {
		return func(func(Row, error) bool) {}
	}
	r.mu.Lock()
	if r.claimed {
		r.mu.Unlock()
		return func(func(Row, error) bool) {}
	}
	r.claimed = true
	r.mu.Unlock()
	return func(yield func(Row, error) bool) {
		r.consume(yield)
	}
}

// Close closes the cursor and prevents a lazy result from executing.
func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy {
		return ErrConcurrentUse
	}
	if r.closed {
		return r.closeErr
	}
	r.closed = true
	if r.rows == nil {
		return nil
	}
	r.closeErr = r.rows.Close()
	return r.closeErr
}

func (r *Result) openLocked() error {
	if r.opened {
		return r.executionErr
	}
	r.opened = true
	if r.rows == nil && r.open == nil {
		r.header = &columnHeader{}
		return nil
	}
	if r.rows == nil {
		rows, err := r.open()
		if err != nil {
			r.executionErr = err
			return err
		}
		r.rows = rows
	}
	if r.rows == nil {
		r.header = &columnHeader{}
		return nil
	}
	names, err := r.rows.Columns()
	if err != nil {
		r.headerErr = fmt.Errorf("row: read result columns: %w", err)
		closeErr := r.rows.Close()
		r.headerErr = errors.Join(r.headerErr, closeErr)
		r.closed = true
		return r.headerErr
	}
	r.header, err = newColumnHeader(names)
	if err != nil {
		r.headerErr = fmt.Errorf("row: create result row: %w", err)
		closeErr := r.rows.Close()
		r.headerErr = errors.Join(r.headerErr, closeErr)
		r.closed = true
	}
	return r.headerErr
}

func (r *Result) consume(yield func(Row, error) bool) {
	r.mu.Lock()
	if r.busy {
		r.mu.Unlock()
		yield(Row{}, ErrConcurrentUse)
		return
	}
	if r.closed {
		err := r.headerErr
		if err != nil && !r.errorYielded {
			r.errorYielded = true
			r.mu.Unlock()
			yield(Row{}, err)
			return
		}
		r.mu.Unlock()
		return
	}
	r.busy = true
	err := r.openLocked()
	rows, header := r.rows, r.header
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		r.busy = false
		r.mu.Unlock()
	}()
	if err != nil {
		yield(Row{}, err)
		return
	}
	if header == nil || rows == nil {
		return
	}
	values := make([]any, len(header.names))
	destinations := make([]any, len(values))
	for index := range values {
		destinations[index] = &values[index]
	}
	for rows.Next() {
		if err := rows.Scan(destinations...); err != nil {
			r.finish(rows, fmt.Errorf("row: scan result row: %w", err))
			yield(Row{}, fmt.Errorf("row: scan result row: %w", err))
			return
		}
		decoded := newRowFrom(header, values)
		if !yield(decoded, nil) {
			r.finish(rows, nil)
			return
		}
	}
	if err := rows.Err(); err != nil {
		err = fmt.Errorf("row: iterate result rows: %w", err)
		r.finish(rows, err)
		yield(Row{}, err)
		return
	}
	r.finish(rows, nil)
}

func (r *Result) finish(rows *sql.Rows, _ error) {
	closeErr := rows.Close()
	r.mu.Lock()
	r.closed = true
	r.closeErr = errors.Join(r.closeErr, closeErr)
	r.mu.Unlock()
}
