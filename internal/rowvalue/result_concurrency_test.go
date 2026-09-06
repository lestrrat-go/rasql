package rowvalue

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestResultRejectsOverlappingHeaderRowsAndClose(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	source := &blockingSource{started: started, release: release}
	result := NewResult(func() (Source, error) { return source, nil })
	headerDone := make(chan error, 1)
	go func() {
		_, err := result.Header()
		headerDone <- err
	}()
	<-started
	_, err := result.Header()
	require.ErrorIs(t, err, ErrConcurrentUse)
	seq := result.Rows()
	var yielded error
	for _, err := range seq {
		yielded = err
	}
	require.ErrorIs(t, yielded, ErrConcurrentUse)
	require.ErrorIs(t, result.Close(), ErrConcurrentUse)
	close(release)
	require.NoError(t, <-headerDone)
	require.NoError(t, result.Close())
}

type blockingSource struct {
	started chan struct{}
	release chan struct{}
}

func (s *blockingSource) Columns() ([]string, error) {
	close(s.started)
	<-s.release
	return []string{"value"}, nil
}
func (*blockingSource) Next() bool        { return false }
func (*blockingSource) Scan(...any) error { return nil }
func (*blockingSource) Close() error      { return nil }
func (*blockingSource) Err() error        { return nil }

func TestResultRetainsExecutionAndIterationErrors(t *testing.T) {
	executionErr := errors.New("execution failed")
	result := NewResult(func() (Source, error) { return nil, executionErr })
	_, err := result.Header()
	require.ErrorIs(t, err, executionErr)
	var yielded error
	for _, err := range result.Rows() {
		yielded = err
	}
	require.ErrorIs(t, yielded, executionErr)

	iterationErr := errors.New("iteration failed")
	source := &errorSource{iterationErr: iterationErr}
	result = NewResult(func() (Source, error) { return source, nil })
	var rows int
	var yielded2 error
	for _, err := range result.Rows() {
		yielded2 = err
		if err == nil {
			rows++
		}
	}
	require.Zero(t, rows)
	require.ErrorIs(t, yielded2, iterationErr)
	_, err = result.Header()
	require.NoError(t, err)
	require.Equal(t, 1, source.closeCount)
}

func TestResultClosesAfterEarlyBreakAndScanFailure(t *testing.T) {
	source := &valueSource{}
	result := NewResult(func() (Source, error) { return source, nil })
	for _, err := range result.Rows() {
		require.NoError(t, err)
		break
	}
	require.Equal(t, 1, source.closeCount)

	scanErr := errors.New("scan failed")
	failing := &valueSource{scanErr: scanErr}
	result = NewResult(func() (Source, error) { return failing, nil })
	var yielded error
	for _, err := range result.Rows() {
		yielded = err
	}
	require.ErrorContains(t, yielded, "scan result row")
	require.ErrorIs(t, yielded, scanErr)
	require.Equal(t, 1, failing.closeCount)
}

type valueSource struct {
	scanErr    error
	closeCount int
	read       bool
}

func (*valueSource) Columns() ([]string, error) { return []string{"value"}, nil }
func (s *valueSource) Next() bool {
	if s.read {
		return false
	}
	s.read = true
	return true
}
func (s *valueSource) Scan(destinations ...any) error {
	if s.scanErr != nil {
		return s.scanErr
	}
	*(destinations[0].(*any)) = "value"
	return nil
}
func (s *valueSource) Close() error { s.closeCount++; return nil }
func (*valueSource) Err() error     { return nil }

type errorSource struct {
	iterationErr error
	closeCount   int
}

func (*errorSource) Columns() ([]string, error) { return []string{"value"}, nil }
func (*errorSource) Next() bool                 { return false }
func (*errorSource) Scan(...any) error          { return nil }
func (s *errorSource) Close() error             { s.closeCount++; return nil }
func (s *errorSource) Err() error               { return s.iterationErr }
