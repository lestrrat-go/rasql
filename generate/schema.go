// Package generate creates deterministic Go source from schema descriptors.
//
// Every function here returns source a caller can write as a file and
// compile. PackageSource and TableSource each return a whole package,
// descriptors included, so their output stands on its own. DescriptorSource
// and DescriptorTestSource return the two files rasqlgen writes once per
// package, and are meant to sit beside the per-table files that command
// writes rather than alone.
package generate

import (
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

// Validate checks whether packageName and tables can produce Go source.
// It checks names across all tables, so callers that generate one file per
// table can still reject collisions between those files before writing any.
func Validate(packageName string, tables ...schema.TableDef) error {
	return schemagen.Validate(packageName, tables...)
}

// PackageSource returns a whole generated package for every table in tables:
// the row types, table types, column accessors and relationships, together
// with each table's runtime descriptor. The result is a complete compilation
// unit, so writing it as the only file of a package compiles.
func PackageSource(packageName string, tables ...schema.TableDef) ([]byte, error) {
	return schemagen.PackageSource(packageName, tables...)
}

// TableSource returns a whole generated package holding one table. allTables
// supplies the package-wide descriptors used to derive relationship methods,
// while only table itself is emitted. Like PackageSource, the result carries
// the table's own descriptor, so a table whose relationships stay within it
// compiles as the only file of a package.
//
// A relationship method names the generated type of the table at its other
// end, so a table related to a table outside its own file needs that table's
// file too, whichever call produced it. Passing every related table to
// PackageSource is the way to get all of them at once.
func TableSource(packageName string, table schema.TableDef, allTables ...schema.TableDef) ([]byte, error) {
	return schemagen.TableSource(packageName, table, allTables...)
}

// DescriptorSource returns the file holding every table's runtime
// descriptor: a schema.TableDef literal for each table, the package-level
// table value built from it through rasql.TableFrom, and an exported
// accessor that hands back a clone of the descriptor.
//
// PackageSource and TableSource emit these declarations themselves, so their
// output and this one cannot be written into the same package. This is the
// file rasqlgen writes once per package, beside the per-table files it
// writes without descriptors.
//
// The emitted literal is the merged definition the generator produces, not
// the input one: a derived belongs-to relationship is appended for every
// foreign key that no explicit relationship matches, so the literal can
// carry relationships the caller's own input did not state.
func DescriptorSource(packageName string, tables ...schema.TableDef) ([]byte, error) {
	return schemagen.DescriptorSource(packageName, tables...)
}

// DescriptorTestSource returns the generated test that validates every
// table's descriptor, so a hand-edit of a DO-NOT-EDIT descriptor file fails
// in the caller's own test run instead of against a database.
//
// The result is deliberately an internal test, package packageName rather
// than packageName_test: it reads each descriptor variable DescriptorSource
// emits unexported, and the generator knows only the package name, never its
// import path, so an external test package could not import it back. That is
// a deliberate exception to the house preference for external test
// packages.
func DescriptorTestSource(packageName string, tables ...schema.TableDef) ([]byte, error) {
	return schemagen.DescriptorTestSource(packageName, tables...)
}
