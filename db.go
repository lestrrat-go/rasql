package rasql

import (
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/exec"
)

// Handle is a database/sql handle that both reads rows and executes
// statements. *sql.DB, *sql.Conn, and *sql.Tx all implement it, and so does a
// logging or debugging wrapper around one. New requires it, so a DB can always
// run a write; a value that only reads is rejected where it is supplied rather
// than where a write is attempted.
// A debug Handle may return nil rows after logging a query; dynamic.Scan
// treats that as no result rows.
type Handle = exec.Handle

// DB executes statements against one database/sql handle for one SQL dialect.
//
// It is the only type this package executes through. A DB from New runs
// statements on the handle it was given; a DB from Begin runs them inside the
// transaction it started, and Commit or Rollback finishes it. Everything that
// takes a DB takes either one, so moving work into a transaction changes which
// DB is passed and nothing else.
//
// A DB is a value. Copying it shares the handle, and WithHooks and Begin
// return new values rather than changing the one they are called on. Its
// methods are safe for concurrent use when its Handle is, which for a
// transaction means one goroutine at a time, because *sql.Tx is bound to a
// single connection.
type DB = exec.DB

// New pairs a database/sql handle with the dialect used to render SQL for it.
// handle may be a *sql.DB for a connection pool, a *sql.Conn for one pinned
// connection, a *sql.Tx for a transaction that is already open, or any other
// Handle. New opens no connection and starts no transaction.
//
// A DB built from a *sql.Tx is a transaction: its Commit and Rollback finish
// that transaction, and its Begin reports an error rather than nesting. That
// is how an application already holding a *sql.Tx hands it to this package
// without a second type.
//
// Optional hooks observe every statement run through the returned DB and,
// unless narrowed or extended by WithHooks or by Begin's own hooks parameter,
// every transaction Begin starts from it.
func New(handle Handle, d dialect.Dialect, hooks ...Hook) (DB, error) {
	return exec.New(handle, d, hooks...)
}
