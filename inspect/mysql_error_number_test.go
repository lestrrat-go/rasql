package inspect

import (
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
)

// mysqlErrorFixture mimics the shape *mysql.MySQLError
// (github.com/go-sql-driver/mysql) has -- an exported Number uint16 field on
// a pointed-to struct -- without importing that package. See
// mysqlErrorNumber's own doc comment for why the driver is never imported
// here.
//
// The shape alone does not make this fixture a MySQL error, and
// mysqlErrorNumber does not treat it as one: the fixture is declared by this
// package, not by the driver, so mysqlDriverErrorType does not name it. A
// test that wants the fixture accepted passes fixtureErrorType instead, which
// stands in for the driver identity a test cannot otherwise produce.
type mysqlErrorFixture struct {
	Number  uint16
	Message string
}

func (e *mysqlErrorFixture) Error() string {
	return fmt.Sprintf("Error %d: %s", e.Number, e.Message)
}

// fixtureErrorType returns the identity of the local type sample points to,
// so a test can drive the accepted path against a fixture. Only the identity
// changes; the walk over the error chain and the read of the Number field are
// the same code the driver identity runs through.
func fixtureErrorType(sample error) errorTypeIdentity {
	sampleType := reflect.TypeOf(sample)
	for sampleType.Kind() == reflect.Pointer {
		sampleType = sampleType.Elem()
	}
	return errorTypeIdentity{packagePath: sampleType.PkgPath(), typeName: sampleType.Name()}
}

func TestMysqlErrorNumber(t *testing.T) {
	fixtureType := fixtureErrorType(&mysqlErrorFixture{})

	t.Run("direct match", func(t *testing.T) {
		number, ok := mysqlErrorNumber(&mysqlErrorFixture{Number: 1146, Message: "no such table"}, fixtureType)
		require.True(t, ok)
		require.Equal(t, uint16(1146), number)
	})

	t.Run("wrapped with fmt.Errorf", func(t *testing.T) {
		wrapped := fmt.Errorf("read table: %w", &mysqlErrorFixture{Number: 1146})
		number, ok := mysqlErrorNumber(wrapped, fixtureType)
		require.True(t, ok)
		require.Equal(t, uint16(1146), number)
	})

	t.Run("wrapped multiple levels deep", func(t *testing.T) {
		wrapped := fmt.Errorf("outer: %w", fmt.Errorf("inner: %w", &mysqlErrorFixture{Number: 1146}))
		number, ok := mysqlErrorNumber(wrapped, fixtureType)
		require.True(t, ok)
		require.Equal(t, uint16(1146), number)
	})

	t.Run("wrapped inside errors.Join", func(t *testing.T) {
		joined := errors.Join(errors.New("unrelated"), &mysqlErrorFixture{Number: 1146})
		number, ok := mysqlErrorNumber(joined, fixtureType)
		require.True(t, ok)
		require.Equal(t, uint16(1146), number)
	})

	t.Run("nil error", func(t *testing.T) {
		number, ok := mysqlErrorNumber(nil, fixtureType)
		require.False(t, ok)
		require.Equal(t, uint16(0), number)
	})

	t.Run("plain error has no Number field", func(t *testing.T) {
		number, ok := mysqlErrorNumber(errors.New("boom"), fixtureType)
		require.False(t, ok)
		require.Equal(t, uint16(0), number)
	})

	t.Run("struct with a Number field of the wrong kind is not mistaken for a match", func(t *testing.T) {
		number, ok := mysqlErrorNumber(&wrongKindNumberError{Number: "1146"}, fixtureErrorType(&wrongKindNumberError{}))
		require.False(t, ok)
		require.Equal(t, uint16(0), number)
	})

	t.Run("nil pointer error value does not panic", func(t *testing.T) {
		var nilFixture *mysqlErrorFixture
		number, ok := mysqlErrorNumber(nilFixture, fixtureType)
		require.False(t, ok)
		require.Equal(t, uint16(0), number)
	})
}

// TestMysqlErrorNumberRejectsForeignErrorTypes pins the direction the field
// match alone got wrong: an error declared by some package other than the
// MySQL driver carries no MySQL server error number, however closely its
// shape resembles *mysql.MySQLError, and 1146 read out of one would make
// Inspector.Table report a table that exists as missing. Every type named
// here is declared by this package, so mysqlDriverErrorType names none of
// them and the walk must come back empty for all of them.
func TestMysqlErrorNumberRejectsForeignErrorTypes(t *testing.T) {
	tests := map[string]error{
		"a fixture with the driver's field shape": &mysqlErrorFixture{Number: 1146, Message: "no such table"},
		"a foreign error carrying 1146 in a numbering scheme of its own": &foreignNumberedError{
			Number: 1146,
			Detail: "connection reset mid-handshake",
		},
		"a foreign error wrapped by fmt.Errorf": fmt.Errorf(
			"read table: %w",
			&foreignNumberedError{Number: 1146, Detail: "connection reset mid-handshake"},
		),
		"a foreign error joined with another": errors.Join(
			errors.New("unrelated"),
			&foreignNumberedError{Number: 1146, Detail: "connection reset mid-handshake"},
		),
	}
	for name, err := range tests {
		t.Run(name, func(t *testing.T) {
			number, ok := mysqlErrorNumber(err, mysqlDriverErrorType)
			require.False(t, ok)
			require.Equal(t, uint16(0), number)
		})
	}
}

// TestMysqlErrorNumberRejectsEveryErrorForTheZeroIdentity pins that an
// Inspector that was not built by New -- whose mysqlErrorType is therefore
// the zero identity -- recognizes no error number at all, rather than
// matching the empty package path and empty name an anonymous struct type
// reports.
func TestMysqlErrorNumberRejectsEveryErrorForTheZeroIdentity(t *testing.T) {
	tests := map[string]error{
		"a named struct type": &mysqlErrorFixture{Number: 1146},
		"an anonymous struct type": error(&struct {
			anonymousErrorBody
			Number uint16
		}{Number: 1146}),
	}
	for name, err := range tests {
		t.Run(name, func(t *testing.T) {
			number, ok := mysqlErrorNumber(err, errorTypeIdentity{})
			require.False(t, ok)
			require.Equal(t, uint16(0), number)
		})
	}
}

// wrongKindNumberError has a "Number" field, like *mysql.MySQLError, but of
// the wrong kind (string, not uint16), exercising that mysqlErrorNumber
// checks the field's kind and does not match on name alone once the type
// identity has matched.
type wrongKindNumberError struct {
	Number string
}

func (e *wrongKindNumberError) Error() string { return "Error " + e.Number }

// foreignNumberedError stands in for an error from a package with nothing to
// do with MySQL that happens to report its own numeric code in a field named
// Number. Its 1146 belongs to a numbering scheme of its own and means nothing
// to MySQL.
type foreignNumberedError struct {
	Number uint16
	Detail string
}

func (e *foreignNumberedError) Error() string {
	return fmt.Sprintf("protocol error %d: %s", e.Number, e.Detail)
}

// anonymousErrorBody supplies Error() to an anonymous struct type, which
// cannot declare a method of its own. An anonymous struct type reports an
// empty package path and an empty name through reflection -- the pair the
// zero errorTypeIdentity carries.
type anonymousErrorBody struct{}

func (anonymousErrorBody) Error() string { return "anonymous struct error" }
