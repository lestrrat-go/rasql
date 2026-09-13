package mysqlerrno_test

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/internal/mysqlerrno"
	"github.com/stretchr/testify/require"
)

// foreignNumberError declares a Number field of the same name and kind the
// driver's own error type declares. Number must not read it: the type
// identity, not the field, is what makes the match sound.
type foreignNumberError struct {
	Number uint16
}

func (foreignNumberError) Error() string { return "not a MySQL driver error" }

func TestNumberReadsOnlyTheDriversOwnErrorType(t *testing.T) {
	native := &mysql.MySQLError{Number: 1060, SQLState: [5]byte{'4', '2', 'S', '2', '1'}, Message: "Duplicate column name 'note'"}
	number, ok := mysqlerrno.Number(native)
	require.True(t, ok)
	require.EqualValues(t, 1060, number)
	number, ok = mysqlerrno.Number(fmt.Errorf("exec: %w", native))
	require.True(t, ok)
	require.EqualValues(t, 1060, number)
	number, ok = mysqlerrno.Number(errors.Join(context.Canceled, fmt.Errorf("exec: %w", native)))
	require.True(t, ok)
	require.EqualValues(t, 1060, number)
	for _, err := range []error{
		nil,
		errors.New("boom"),
		context.Canceled,
		foreignNumberError{Number: 1060},
		(*mysql.MySQLError)(nil),
	} {
		number, ok = mysqlerrno.Number(err)
		require.False(t, ok, "%v", err)
		require.EqualValues(t, 0, number)
	}
}

func TestAlreadyAppliedNumbers(t *testing.T) {
	for _, number := range []uint16{1050, 1060, 1061, 1091, 3821, 3940} {
		require.True(t, mysqlerrno.AlreadyApplied(number), "%d means the work was already done", number)
	}
	for _, number := range []uint16{0, 1051, 1064, 1146, 1826, 3822} {
		require.False(t, mysqlerrno.AlreadyApplied(number), "%d means something else", number)
	}
}
