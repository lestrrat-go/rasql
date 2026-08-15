package inspect

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/require"
)

// mysqlErrorFixture mimics the shape *mysql.MySQLError
// (github.com/go-sql-driver/mysql) has -- an exported Number uint16 field on
// a pointed-to struct -- without importing that package. See
// mysqlErrorNumber's own doc comment for why the driver is never imported
// here.
type mysqlErrorFixture struct {
	Number  uint16
	Message string
}

func (e *mysqlErrorFixture) Error() string {
	return fmt.Sprintf("Error %d: %s", e.Number, e.Message)
}

func TestMysqlErrorNumber(t *testing.T) {
	t.Run("direct match", func(t *testing.T) {
		number, ok := mysqlErrorNumber(&mysqlErrorFixture{Number: 1146, Message: "no such table"})
		require.True(t, ok)
		require.Equal(t, uint16(1146), number)
	})

	t.Run("wrapped with fmt.Errorf", func(t *testing.T) {
		wrapped := fmt.Errorf("read table: %w", &mysqlErrorFixture{Number: 1146})
		number, ok := mysqlErrorNumber(wrapped)
		require.True(t, ok)
		require.Equal(t, uint16(1146), number)
	})

	t.Run("wrapped multiple levels deep", func(t *testing.T) {
		wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &mysqlErrorFixture{Number: 1146}))
		number, ok := mysqlErrorNumber(wrapped)
		require.True(t, ok)
		require.Equal(t, uint16(1146), number)
	})

	t.Run("wrapped inside errors.Join", func(t *testing.T) {
		joined := errors.Join(errors.New("unrelated"), &mysqlErrorFixture{Number: 1146})
		number, ok := mysqlErrorNumber(joined)
		require.True(t, ok)
		require.Equal(t, uint16(1146), number)
	})

	t.Run("nil error", func(t *testing.T) {
		number, ok := mysqlErrorNumber(nil)
		require.False(t, ok)
		require.Equal(t, uint16(0), number)
	})

	t.Run("plain error has no Number field", func(t *testing.T) {
		number, ok := mysqlErrorNumber(errors.New("boom"))
		require.False(t, ok)
		require.Equal(t, uint16(0), number)
	})

	t.Run("struct with a Number field of the wrong kind is not mistaken for a match", func(t *testing.T) {
		number, ok := mysqlErrorNumber(&wrongKindNumberError{Number: "1146"})
		require.False(t, ok)
		require.Equal(t, uint16(0), number)
	})

	t.Run("nil pointer error value does not panic", func(t *testing.T) {
		var nilFixture *mysqlErrorFixture
		number, ok := mysqlErrorNumber(nilFixture)
		require.False(t, ok)
		require.Equal(t, uint16(0), number)
	})
}

// wrongKindNumberError has a "Number" field, like *mysql.MySQLError, but of
// the wrong kind (string, not uint16), exercising that mysqlErrorNumber
// checks the field's kind and does not match on name alone.
type wrongKindNumberError struct {
	Number string
}

func (e *wrongKindNumberError) Error() string { return "Error " + e.Number }
