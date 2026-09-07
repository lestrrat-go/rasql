package dynamic

import (
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/exec"
	"github.com/stretchr/testify/require"
)

func TestScanSourceCompletesColumnErrorThroughRowSource(t *testing.T) {
	columnErr := errors.New("columns unavailable")
	rows := &fakeLifecycleSource{columnErr: columnErr}
	values := make([]Row, 0)
	for value, err := range scanSource(rows, false) {
		if err == nil {
			values = append(values, value)
		}
		require.ErrorIs(t, err, columnErr)
	}
	require.Empty(t, values)
	require.ErrorIs(t, rows.finishErr, columnErr)
}

type fakeLifecycleSource struct {
	columnErr error
	finishErr error
}

func (f *fakeLifecycleSource) Columns() ([]string, error) { return nil, f.columnErr }
func (f *fakeLifecycleSource) Next() bool                 { return false }
func (f *fakeLifecycleSource) Scan(...any) error          { return nil }
func (f *fakeLifecycleSource) Close() error               { return nil }
func (f *fakeLifecycleSource) Err() error                 { return nil }
func (f *fakeLifecycleSource) RecordRow()                 {}
func (f *fakeLifecycleSource) Finish(err error, _ bool) error {
	f.finishErr = err
	return err
}

var _ exec.RowSource = (*fakeLifecycleSource)(nil)
