package rasql

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
)

// Beginner is a Handle that can also start a transaction. *sql.DB and
// *sql.Conn implement it; *sql.Tx does not, so a transaction cannot be
// nested through NewDB and Begin: the attempt fails to compile rather than
// opening a savepoint or failing at run time.
type Beginner interface {
	Handle
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

// DB is a Client that can also start transactions on the database or
// connection it was built from.
type DB struct {
	client   Client
	beginner Beginner
}

// NewDB creates a DB bound to db and d. db may be a *sql.DB for a full
// connection pool, a *sql.Conn for a single pinned connection, or any other
// Beginner. It does not open a connection or start a transaction.
// Optional hooks observe every statement run directly through the returned
// DB and, unless narrowed or extended by WithHooks or Begin's own hooks
// parameter, every transaction Begin starts from it.
func NewDB(db Beginner, d dialect.Dialect, hooks ...Hook) (DB, error) {
	if isNil(db) {
		return DB{}, fmt.Errorf("rasql: beginner must not be nil")
	}
	client, err := New(db, d, hooks...)
	if err != nil {
		return DB{}, err
	}
	return DB{client: client, beginner: db}, nil
}

// Begin starts a transaction on the database or connection db was built from
// and returns a Tx bound to db's dialect. The returned Tx inherits db's
// hooks, with hooks appended after them, the same way Client.WithHooks and
// Tx.WithHooks append rather than replace. A hook registered on db therefore
// also runs for every operation inside a transaction started from it.
// opts may be nil, which leaves the isolation level and read-only mode to the
// driver. Begin does not roll back on ctx cancellation by itself; that is
// database/sql's own behavior for the transaction it returns.
// A Beginner that reports success while returning a nil transaction is
// rejected with an error rather than wrapped, so a hand-written one cannot
// produce a Tx that panics on its first use.
func (db DB) Begin(ctx context.Context, opts *sql.TxOptions, hooks ...Hook) (Tx, error) {
	if isNil(db.beginner) {
		return Tx{}, fmt.Errorf("rasql: invalid client")
	}
	transaction, err := db.beginner.BeginTx(ctx, opts)
	if err != nil {
		return Tx{}, fmt.Errorf("rasql: begin transaction: %w", err)
	}
	if transaction == nil {
		// *sql.DB and *sql.Conn never do this: their BeginTx returns a
		// transaction or an error. A hand-written Beginner can, and the nil
		// would otherwise reach Rollback below as a nil receiver.
		return Tx{}, fmt.Errorf("rasql: beginner returned a nil transaction without an error")
	}
	client := db.client
	client.handle = transaction
	client, err = client.WithHooks(hooks...)
	if err != nil {
		// The transaction is already open, so it is rolled back rather than
		// leaked. Its own error cannot reach the caller, who never received
		// the handle, and it would hide the reason Begin is failing.
		_ = transaction.Rollback()
		return Tx{}, err
	}
	return Tx{client: client, tx: transaction}, nil
}

// WithHooks returns a copy of db that runs hooks around statements executed
// directly through db, and inherits them into every transaction Begin starts
// from the copy afterward. It does not affect a Tx already returned by
// Begin.
func (db DB) WithHooks(hooks ...Hook) (DB, error) {
	if isNil(db.beginner) {
		return DB{}, fmt.Errorf("rasql: invalid client")
	}
	client, err := db.client.WithHooks(hooks...)
	if err != nil {
		return DB{}, err
	}
	db.client = client
	return db, nil
}

// Client returns the Client view of db: the same handle, dialect, and hooks,
// without the ability to start a transaction. Use it when application code
// stores a Client in its own types, so a DB does not have to be threaded
// everywhere a Client is enough.
func (db DB) Client() Client {
	return db.client
}

// Dialect returns the dialect this DB renders SQL for.
// It returns nil for a zero DB.
func (db DB) Dialect() dialect.Dialect {
	if isNil(db.beginner) {
		return nil
	}
	return db.client.Dialect()
}

// QueryRendered executes statement directly against the database or
// connection db was built from and returns its result rows. The caller owns
// the rows; row.Scan closes them.
func (db DB) QueryRendered(ctx context.Context, statement render.Statement) (*sql.Rows, error) {
	if isNil(db.beginner) {
		return nil, fmt.Errorf("rasql: invalid client")
	}
	return db.client.QueryRendered(ctx, statement)
}

// ExecRendered executes a rendered statement directly against the database or
// connection db was built from.
func (db DB) ExecRendered(ctx context.Context, statement render.Statement) (sql.Result, error) {
	if isNil(db.beginner) {
		return nil, fmt.Errorf("rasql: invalid client")
	}
	return db.client.ExecRendered(ctx, statement)
}

var _ Executor = DB{}
