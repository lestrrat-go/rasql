package inspect

import "reflect"

// mysqlErrNoSuchTable is MySQL server error 1146 ("table doesn't exist"),
// the code mySQLCheckColumnVisibility treats as proof that SHOW CREATE TABLE
// found no table rather than a failure worth propagating.
const mysqlErrNoSuchTable uint16 = 1146

// errorTypeIdentity names one concrete error type by the import path of the
// package that declares it and the name it is declared under. Both halves
// must match, which is what separates the MySQL driver's own error type from
// an unrelated package's struct that happens to declare a field of the same
// name and kind.
type errorTypeIdentity struct {
	packagePath string
	typeName    string
}

// matches reports whether structType is the type this identity names. The
// zero identity matches nothing: an unnamed struct type reports an empty
// package path and an empty name, so without this guard a zero identity
// would accept any anonymous struct.
func (identity errorTypeIdentity) matches(structType reflect.Type) bool {
	if identity.packagePath == "" || identity.typeName == "" {
		return false
	}
	return structType.PkgPath() == identity.packagePath && structType.Name() == identity.typeName
}

// mysqlDriverErrorType is the identity of *mysql.MySQLError, the error value
// github.com/go-sql-driver/mysql returns for an error the server reported.
// Both halves are that package's own public API: the import path is the
// module path callers use, and MySQLError is the exported type name, stable
// since the type was introduced.
var mysqlDriverErrorType = errorTypeIdentity{
	packagePath: "github.com/go-sql-driver/mysql",
	typeName:    "MySQLError",
}

// mysqlErrorNumber reports the MySQL server error number carried by err, or
// by anything err wraps, without importing github.com/go-sql-driver/mysql.
// That package's init registers a database/sql driver under the name
// "mysql" as a side effect of merely being imported, so an ordinary
// (non-blank) import of it here would register that driver in every
// program that imports this package, whether or not it ever speaks to
// MySQL — and sql.Register panics on a name already registered, which makes
// an unwanted registration a real hazard, not just noise.
//
// errorType names the only error type a number is read from, normally
// mysqlDriverErrorType. Because the driver cannot be imported, that type
// cannot be named in a type assertion either, so this function walks err's
// chain the same way errors.As does (following Unwrap() error and
// Unwrap() []error) and compares each error value's reflected type against
// errorType by package path and type name. Only a value of that one type is
// accepted, and only then is its exported Number field read, which the
// driver declares as a uint16.
//
// Matching on the field alone would not do. *mysql.MySQLError is a plain
// struct with an exported Number uint16 field and no accessor method a
// narrow interface could match, and an error from an unrelated package is
// free to declare a field of that same name and kind holding a number from
// some other numbering scheme entirely. Accepting it would report that
// foreign number as a MySQL server error number, so the type identity, not
// the field, is what makes the match sound.
//
// When no error in the chain is of errorType, ok is false and the caller
// must treat the error exactly as it treats any error it does not
// recognize — this function never guesses a number for an error it cannot
// positively identify, so a future driver change that moves, renames, or
// reshapes the type fails closed (the error propagates as unrecognized)
// instead of silently mismatching a code.
func mysqlErrorNumber(err error, errorType errorTypeIdentity) (number uint16, ok bool) {
	if err == nil {
		return 0, false
	}
	if number, ok := mysqlErrorNumberFromValue(err, errorType); ok {
		return number, true
	}
	switch unwrapper := err.(type) {
	case interface{ Unwrap() error }:
		return mysqlErrorNumber(unwrapper.Unwrap(), errorType)
	case interface{ Unwrap() []error }:
		for _, wrapped := range unwrapper.Unwrap() {
			if number, ok := mysqlErrorNumber(wrapped, errorType); ok {
				return number, true
			}
		}
	}
	return 0, false
}

// mysqlErrorNumberFromValue inspects a single error value (not its wrapped
// chain). It reads the "Number" field only from a value whose pointed-to
// struct type is the one errorType names, and only when that field has the
// uint16 kind *mysql.MySQLError declares it with.
func mysqlErrorNumberFromValue(err error, errorType errorTypeIdentity) (uint16, bool) {
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
	if !errorType.matches(value.Type()) {
		return 0, false
	}
	field := value.FieldByName("Number")
	if !field.IsValid() || field.Kind() != reflect.Uint16 {
		return 0, false
	}
	return uint16(field.Uint()), true
}
