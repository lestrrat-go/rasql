// Package sqltext brands SQL text that rasql sends to a database without
// parsing it.
//
// Text exists so an unparsed string is distinguishable from an ordinary one
// at compile time. An untyped string constant converts to a named string type
// implicitly and a string variable does not, so a literal reaches a Text
// parameter with no ceremony while a value built at run time needs an
// explicit sqltext.Text conversion — one grep for every place a program vouches
// for SQL it assembled itself.
//
// rasql brands what a user or a live database hands it: the SQL-bearing
// fields of a schema descriptor, a migration's source, and a desired-schema
// source file. It does not brand what rasql itself produces as output or
// reports in an error.
//
// Rebuilding a statement from one rasql already rendered goes through
// stmt.Statement.Text, which hands stmt.New the branded text directly. That
// keeps a grep for the conversion on the call sites where a program vouches
// for SQL it assembled, rather than on rasql passing its own output back to
// itself.
package sqltext

// Text is SQL text rasql sends to a database as written, without parsing it.
type Text string
