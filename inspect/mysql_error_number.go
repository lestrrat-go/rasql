package inspect

import "reflect"

// mysqlErrNoSuchTable is MySQL server error 1146 ("table doesn't exist"),
// the code mySQLCheckColumnVisibility treats as proof that SHOW CREATE TABLE
// found no table rather than a failure worth propagating.
const mysqlErrNoSuchTable uint16 = 1146

// mysqlErrorNumber reports the MySQL server error number carried by err, or
// by anything err wraps, without importing github.com/go-sql-driver/mysql.
// That package's init registers a database/sql driver under the name
// "mysql" as a side effect of merely being imported, so an ordinary
// (non-blank) import of it here would register that driver in every
// program that imports this package, whether or not it ever speaks to
// MySQL — and sql.Register panics on a name already registered, which makes
// an unwanted registration a real hazard, not just noise.
//
// The driver's error type, *mysql.MySQLError, is a plain struct with an
// exported Number uint16 field and no accessor method a narrow interface
// could match, so there is no interface this function could assert against
// instead. It walks err's chain the same way errors.As does (following
// Unwrap() error and Unwrap() []error), and at each error value uses
// reflection to look for a field named "Number" of kind uint16 on the
// pointed-to struct. The field name is part of the driver's own public API
// and has been stable since the type was introduced, which is what makes
// this reflect-based match a reasonable substitute for a type assertion
// rather than a guess.
//
// When no error in the chain has such a field, ok is false and the caller
// must treat the error exactly as it treats any error it does not
// recognize — this function never guesses a number for an error it cannot
// positively identify, so a future driver change that removes or renames
// the field fails closed (the error propagates as unrecognized) instead of
// silently mismatching a code.
func mysqlErrorNumber(err error) (number uint16, ok bool) {
	if err == nil {
		return 0, false
	}
	if number, ok := mysqlErrorNumberFromValue(err); ok {
		return number, true
	}
	switch unwrapper := err.(type) {
	case interface{ Unwrap() error }:
		return mysqlErrorNumber(unwrapper.Unwrap())
	case interface{ Unwrap() []error }:
		for _, wrapped := range unwrapper.Unwrap() {
			if number, ok := mysqlErrorNumber(wrapped); ok {
				return number, true
			}
		}
	}
	return 0, false
}

// mysqlErrorNumberFromValue inspects a single error value (not its wrapped
// chain) for a "Number" field of kind uint16 on the struct a pointer points
// to, the shape *mysql.MySQLError has.
func mysqlErrorNumberFromValue(err error) (uint16, bool) {
	value := reflect.ValueOf(err)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			return 0, false
		}
		value = value.Elem()
	}
	if value.Kind() != reflect.Struct {
		return 0, false
	}
	field := value.FieldByName("Number")
	if !field.IsValid() || field.Kind() != reflect.Uint16 {
		return 0, false
	}
	return uint16(field.Uint()), true
}
