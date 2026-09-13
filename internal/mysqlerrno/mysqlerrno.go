// Package mysqlerrno reads MySQL server error numbers without importing a
// MySQL driver, and names the numbers that mean a statement asked for work the
// database had already done.
//
// It exists so migrate can read those numbers while keeping the driver out of
// its import graph. github.com/go-sql-driver/mysql registers a database/sql
// driver under the name "mysql" from its init as a side effect of being
// imported at all, so an ordinary import of it in migrate would register that
// driver in every program that runs a migration, whether or not it ever speaks
// to MySQL. inspect/mysql_error_number.go keeps the same boundary for the same
// reason.
package mysqlerrno

import "reflect"

// driverPackagePath and driverTypeName identify *mysql.MySQLError, the error
// value github.com/go-sql-driver/mysql returns for an error the server
// reported. Both halves are that package's own public API: the import path is
// the module path callers use, and MySQLError is the exported type name,
// stable since the type was introduced.
//
// Matching on the field alone would not do. *mysql.MySQLError is a plain
// struct with an exported Number uint16 field and no accessor method a narrow
// interface could match, and an error from an unrelated package is free to
// declare a field of that same name and kind holding a number from some other
// numbering scheme entirely. The type identity, not the field, is what makes
// the match sound.
const (
	driverPackagePath = "github.com/go-sql-driver/mysql"
	driverTypeName    = "MySQLError"
)

// alreadyApplied holds the MySQL server error numbers that mean the statement
// raising one asked for work the database had already done. Each was measured
// against a live MySQL 8.4.11 server rather than taken from documentation,
// whose manual advertises spellings this server rejects:
//
//	1050  Table 'x' already exists                           repeating CREATE TABLE
//	1060  Duplicate column name 'c'                          repeating ALTER TABLE ... ADD COLUMN
//	1061  Duplicate key name 'i'                             repeating CREATE INDEX, or ADD INDEX
//	1091  Can't DROP 'x'; check that column/key exists       dropping an absent column, index, or foreign key
//	3821  Check constraint 'x' is not found in the table.    dropping an absent check constraint
//	3940  Constraint 'x' does not exist.                     dropping an absent constraint
//
// 1091 carries one number and one message for a column, an index, and a
// foreign key alike, so a caller cannot tell which was meant. That is enough
// to decide the work was already done.
//
// The set is deliberately narrow. 1051 (Unknown table) and 1146 (table
// doesn't exist) stay out: MySQL spells DROP TABLE IF EXISTS, so a repeated
// drop needs no tolerance, and 1146 is also what the server reports when a
// statement's dependency is missing, which is the opposite of already done.
// 1826 (duplicate foreign key constraint name) and 3822 (duplicate check
// constraint name) stay out because nothing has measured them into this set
// yet, not because they mean something else.
//
// Two of these numbers also answer a statement that is simply malformed:
// MySQL reports 1060 for CREATE TABLE t (x INT, x INT) and for a CREATE VIEW
// whose own SELECT repeats a column alias, and 1061 for a CREATE TABLE that
// names one key twice. Nothing was already done in those cases, and a caller
// that tolerates the number accepts a statement that created nothing. Telling
// the two apart would mean reading the SQL, which rasql does not do; such a
// source is an authoring mistake that the application's own tests catch.
var alreadyApplied = map[uint16]struct{}{
	1050: {}, 1060: {}, 1061: {}, 1091: {}, 3821: {}, 3940: {},
}

// AlreadyApplied reports whether number is one of the MySQL server error
// numbers that mean the statement's work was already done.
func AlreadyApplied(number uint16) bool {
	_, ok := alreadyApplied[number]
	return ok
}

// Number reports the MySQL server error number carried by err, or by anything
// err wraps. It walks err's chain the way errors.As does, following
// Unwrap() error and Unwrap() []error, and compares each value's reflected
// type against the driver's own error type by package path and type name.
//
// When no error in the chain is that type, ok is false and the caller must
// treat err exactly as it treats any error it does not recognize. This
// function never guesses a number for an error it cannot positively identify,
// so a future driver change that moves, renames, or reshapes the type fails
// closed rather than silently mismatching a code.
func Number(err error) (number uint16, ok bool) {
	if err == nil {
		return 0, false
	}
	if number, ok := numberFromValue(err); ok {
		return number, true
	}
	switch unwrapper := err.(type) {
	case interface{ Unwrap() error }:
		return Number(unwrapper.Unwrap())
	case interface{ Unwrap() []error }:
		for _, wrapped := range unwrapper.Unwrap() {
			if number, ok := Number(wrapped); ok {
				return number, true
			}
		}
	}
	return 0, false
}

// numberFromValue inspects a single error value, not its wrapped chain. It
// reads the "Number" field only from a value whose pointed-to struct type is
// the driver's own, and only when that field has the uint16 kind the driver
// declares it with.
func numberFromValue(err error) (uint16, bool) {
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
	structType := value.Type()
	if structType.PkgPath() != driverPackagePath || structType.Name() != driverTypeName {
		return 0, false
	}
	field := value.FieldByName("Number")
	if !field.IsValid() || field.Kind() != reflect.Uint16 {
		return 0, false
	}
	return uint16(field.Uint()), true
}
