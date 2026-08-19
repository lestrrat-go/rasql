package dsnredact_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql/internal/dsnredact"
	"github.com/stretchr/testify/require"
)

func TestError(t *testing.T) {
	dsn := "postgres://user:secret@example.test/database"

	t.Run("removes the dsn from the message", func(t *testing.T) {
		err := dsnredact.Error(errors.New("connect "+dsn+" failed"), dsn)
		require.NotContains(t, err.Error(), dsn)
		require.NotContains(t, err.Error(), "secret")
		require.Contains(t, err.Error(), "[redacted]")
	})

	t.Run("keeps the cause reachable", func(t *testing.T) {
		cause := errors.New("connect " + dsn + " failed")
		wrapped := fmt.Errorf("open database: %w", cause)
		err := dsnredact.Error(wrapped, dsn)
		require.ErrorIs(t, err, cause)
	})

	t.Run("returns a nil error unchanged", func(t *testing.T) {
		require.NoError(t, dsnredact.Error(nil, dsn))
	})

	t.Run("returns the error unchanged for an empty dsn", func(t *testing.T) {
		cause := errors.New("connect failed")
		require.Equal(t, cause, dsnredact.Error(cause, ""))
	})
}
