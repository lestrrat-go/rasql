package rowvalue

import (
	"database/sql"
	"errors"
	"fmt"
	"iter"
	"sync"
)

// Source is the cursor surface consumed by Result.
type Source interface {
	Columns() ([]string, error)
	Next() bool
	Scan(...any) error
	Close() error
	Err() error
}

type owner interface {
	Source
	RecordRow()
	Finish(error, bool) error
}

// Result owns one lazily opened database cursor and its immutable header.
type Result struct {
	mu           sync.Mutex
	open         func() (Source, error)
	source       Source
	opened       bool
	closed       bool
	busy         bool
	claimed      bool
	errorYielded bool
	header       *columnHeader
	headerErr    error
	closeErr     error
	executionErr error
}

// NewResult creates a lazy result whose opener runs at most once.
func NewResult(open func() (Source, error)) *Result { return &Result{open: open} }

// ScanResult takes ownership of rows immediately without executing anything.
func ScanResult(rows *sql.Rows) *Result { return &Result{source: rows} }

// Header opens the cursor when needed and returns its ordered columns.
func (r *Result) Header() (Header, error) {
	if r == nil {
		return Header{}, nil
	}
	r.mu.Lock()
	if r.closed {
		header, err := Header{columns: r.header}, r.headerErr
		r.mu.Unlock()
		return header, err
	}
	r.mu.Unlock()
	if !r.beginOperation() {
		return Header{}, ErrConcurrentUse
	}
	err := r.openSource()
	r.endOperation()
	r.mu.Lock()
	header := Header{columns: r.header}
	err = errors.Join(err, r.headerErr)
	r.mu.Unlock()
	return header, err
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
	return func(yield func(Row, error) bool) { r.consume(yield) }
}

// Close closes the cursor and prevents a lazy result from executing.
func (r *Result) Close() error {
	if r == nil {
		return nil
	}
	if !r.beginOperation() {
		return ErrConcurrentUse
	}
	r.mu.Lock()
	r.closed = true
	source := r.source
	r.mu.Unlock()
	var err error
	if source != nil {
		err = source.Close()
		if scoped, ok := source.(owner); ok {
			err = errors.Join(err, scoped.Finish(err, true))
		}
	}
	r.mu.Lock()
	r.closeErr = errors.Join(r.closeErr, err)
	r.mu.Unlock()
	r.endOperation()
	return err
}

func (r *Result) beginOperation() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.busy {
		return false
	}
	r.busy = true
	return true
}

func (r *Result) endOperation() {
	r.mu.Lock()
	r.busy = false
	r.mu.Unlock()
}

func (r *Result) openSource() error {
	r.mu.Lock()
	if r.opened {
		err := errors.Join(r.executionErr, r.headerErr)
		r.mu.Unlock()
		return err
	}
	r.opened = true
	source := r.source
	open := r.open
	r.mu.Unlock()
	if source == nil && open != nil {
		var err error
		source, err = open()
		if err != nil {
			r.mu.Lock()
			r.executionErr = err
			r.mu.Unlock()
			return err
		}
	}
	if source == nil {
		r.mu.Lock()
		r.header = &columnHeader{}
		r.mu.Unlock()
		return nil
	}
	names, err := source.Columns()
	r.mu.Lock()
	r.source = source
	if err != nil {
		r.headerErr = errors.Join(fmt.Errorf("row: read result columns: %w", err), source.Close())
		r.closed = true
		if scoped, ok := source.(owner); ok {
			r.headerErr = errors.Join(r.headerErr, scoped.Finish(r.headerErr, true))
		}
		r.mu.Unlock()
		return r.headerErr
	}
	r.header, err = newColumnHeader(names)
	if err != nil {
		r.headerErr = errors.Join(fmt.Errorf("row: create result row: %w", err), source.Close())
		r.closed = true
		if scoped, ok := source.(owner); ok {
			r.headerErr = errors.Join(r.headerErr, scoped.Finish(r.headerErr, true))
		}
	}
	r.mu.Unlock()
	return err
}

func (r *Result) consume(yield func(Row, error) bool) {
	r.mu.Lock()
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
	r.mu.Unlock()
	if !r.beginOperation() {
		yield(Row{}, ErrConcurrentUse)
		return
	}
	err := r.openSource()
	r.mu.Lock()
	source, header, closed := r.source, r.header, r.closed
	r.mu.Unlock()
	if err != nil {
		r.endOperation()
		yield(Row{}, err)
		return
	}
	if closed || source == nil || header == nil {
		r.endOperation()
		return
	}
	values := make([]any, len(header.names))
	destinations := make([]any, len(values))
	for index := range values {
		destinations[index] = &values[index]
	}
	for source.Next() {
		if err := source.Scan(destinations...); err != nil {
			r.finish(source, err, true)
			r.endOperation()
			yield(Row{}, fmt.Errorf("row: scan result row: %w", err))
			return
		}
		if scoped, ok := source.(owner); ok {
			scoped.RecordRow()
		}
		if !yield(newRowFrom(header, values), nil) {
			r.finish(source, nil, true)
			r.endOperation()
			return
		}
	}
	if err := source.Err(); err != nil {
		r.finish(source, err, false)
		r.endOperation()
		yield(Row{}, fmt.Errorf("row: iterate result rows: %w", err))
		return
	}
	r.finish(source, nil, false)
	r.endOperation()
}

func (r *Result) finish(source Source, err error, earlyClose bool) {
	closeErr := source.Close()
	if scoped, ok := source.(owner); ok {
		closeErr = errors.Join(closeErr, scoped.Finish(err, earlyClose))
	}
	r.mu.Lock()
	r.closed = true
	r.closeErr = errors.Join(r.closeErr, closeErr)
	r.mu.Unlock()
}
