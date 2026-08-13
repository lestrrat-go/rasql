package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/render"
)

// Handle is a database/sql handle that both reads rows and executes
// statements. *sql.DB, *sql.Conn, and *sql.Tx all implement it, and so does a
// logging or debugging wrapper around one. New requires it, so a DB can always
// run a write; a value that only reads is rejected where it is supplied rather
// than where a write is attempted.
// A debug Handle may return nil rows after logging a query; row.Scan treats
// that as no result rows.
type Handle interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

// beginner is a Handle that can also start a transaction. *sql.DB and
// *sql.Conn implement it; *sql.Tx does not. Begin asks the handle for it
// instead of requiring it in New, so reading and writing outside a transaction
// works with any Handle at all.
type beginner interface {
	BeginTx(context.Context, *sql.TxOptions) (*sql.Tx, error)
}

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
type DB struct {
	handle  Handle
	dialect dialect.Dialect
	hooks   []Hook
	// tx is the transaction this DB runs in, and is nil when it runs directly
	// on handle. When it is set it is the same value as handle.
	tx *sql.Tx
}

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
	if isNil(handle) {
		return DB{}, fmt.Errorf("rasql: handle must not be nil")
	}
	if isNil(d) {
		return DB{}, fmt.Errorf("rasql: dialect must not be nil")
	}
	db := DB{handle: handle, dialect: d}
	if transaction, ok := handle.(*sql.Tx); ok {
		db.tx = transaction
	}
	return db.WithHooks(hooks...)
}

// WithHooks returns a copy of db that runs hooks around rendered queries and
// mutations, appended after the hooks db already carries. Every transaction
// Begin starts from the copy inherits them. It does not affect a DB that Begin
// already returned, and it does not wrap the database handle, so the SQL and
// bound arguments that reach database/sql are unchanged.
func (db DB) WithHooks(hooks ...Hook) (DB, error) {
	if err := db.valid(); err != nil {
		return DB{}, err
	}
	configured, err := appendHooks(db.hooks, hooks)
	if err != nil {
		return DB{}, err
	}
	db.hooks = configured
	return db, nil
}

// Dialect returns the dialect this DB renders SQL for.
// It returns nil for a zero DB.
func (db DB) Dialect() dialect.Dialect {
	return db.dialect
}

// Handle returns the database/sql handle db runs on, which is the *sql.Tx
// itself once db is a transaction. An application can therefore keep the DB
// alone and reach the handle from it for the work this package does not cover,
// instead of carrying both.
// It returns nil for a zero DB.
func (db DB) Handle() Handle {
	return db.handle
}

// Begin starts a transaction on the handle db runs on and returns a DB bound
// to it, carrying the same dialect. Statements run through the returned DB run
// inside the transaction until Commit or Rollback finishes it.
//
// The handle must be able to start a transaction. *sql.DB and *sql.Conn can;
// *sql.Tx cannot, so Begin on a DB that is already a transaction reports an
// error rather than quietly opening a savepoint.
//
// The returned DB inherits db's hooks, with hooks appended after them, the
// same way WithHooks appends rather than replaces. A hook registered on db
// therefore also runs for every operation inside a transaction started from
// it.
//
// opts may be nil, which leaves the isolation level and read-only mode to the
// driver. Begin does not roll back on ctx cancellation by itself; that is
// database/sql's own behavior for the transaction it returns.
func (db DB) Begin(ctx context.Context, opts *sql.TxOptions, hooks ...Hook) (DB, error) {
	if err := db.valid(); err != nil {
		return DB{}, err
	}
	if db.tx != nil {
		return DB{}, fmt.Errorf("rasql: this DB is already a transaction: nested transactions are not supported")
	}
	starter, ok := db.handle.(beginner)
	if !ok {
		return DB{}, fmt.Errorf("rasql: handle of type %T cannot start a transaction", db.handle)
	}
	transaction, err := starter.BeginTx(ctx, opts)
	if err != nil {
		return DB{}, fmt.Errorf("rasql: begin transaction: %w", err)
	}
	if transaction == nil {
		// *sql.DB and *sql.Conn never do this: their BeginTx returns a
		// transaction or an error. A hand-written handle can, and the nil
		// would otherwise reach Rollback below as a nil receiver.
		return DB{}, fmt.Errorf("rasql: handle returned a nil transaction without an error")
	}
	db.handle = transaction
	db.tx = transaction
	db, err = db.WithHooks(hooks...)
	if err != nil {
		// The transaction is already open, so it is rolled back rather than
		// leaked. Its own error cannot reach the caller, who never received
		// the handle, and it would hide the reason Begin is failing.
		_ = transaction.Rollback()
		return DB{}, err
	}
	return db, nil
}

// Commit commits the transaction db runs in. It reports an error when db is
// not a transaction, which is every DB except one from Begin and one built by
// New from a *sql.Tx.
//
// The caller owns the transaction: every path out of the function that called
// Begin must reach Commit or Rollback. A bare defer of Rollback right after
// Begin is the intended shape, because Rollback reports no error once the
// transaction is finished. Every later Commit or Rollback finds it finished: a
// later Commit reports that, and a later Rollback reports nothing.
func (db DB) Commit() error {
	if db.tx == nil {
		return fmt.Errorf("rasql: this DB is not a transaction: Commit needs one from Begin")
	}
	if err := db.tx.Commit(); err != nil {
		return fmt.Errorf("rasql: commit transaction: %w", err)
	}
	return nil
}

// Rollback rolls back the transaction db runs in, and reports no error when it
// is already finished, whether by a successful Commit, an earlier Rollback, or
// a rollback database/sql performed when the context was cancelled. That is
// what makes a bare defer of Rollback right after Begin correct rather than an
// error a caller learns to discard.
//
// It reports an error when db is not a transaction, which is every DB except
// one from Begin and one built by New from a *sql.Tx.
func (db DB) Rollback() error {
	if db.tx == nil {
		return fmt.Errorf("rasql: this DB is not a transaction: Rollback needs one from Begin")
	}
	if err := db.tx.Rollback(); err != nil {
		if errors.Is(err, sql.ErrTxDone) {
			return nil
		}
		return fmt.Errorf("rasql: roll back transaction: %w", err)
	}
	return nil
}

// QueryRendered executes statement and returns its result rows.
// The caller owns the returned rows: hand them to row.Scan, which closes them,
// or close them directly. A debug Handle that logs the statement instead of
// running it may return nil rows, which row.Scan reads as no result rows.
func (db DB) QueryRendered(ctx context.Context, statement render.Statement) (*sql.Rows, error) {
	if err := db.validStatement(statement); err != nil {
		return nil, err
	}
	operation := Operation{kind: QueryOperation, statement: statement}
	entered, err := db.beforeHooks(ctx, operation)
	if err != nil {
		return nil, afterHooks(ctx, operation, entered, err)
	}
	rows, err := db.handle.QueryContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		err = fmt.Errorf("rasql: execute query: %w", err)
	}
	if hookErr := afterHooks(ctx, operation, entered, err); hookErr != nil {
		if rows != nil {
			if closeErr := rows.Close(); closeErr != nil {
				hookErr = errors.Join(hookErr, fmt.Errorf("rasql: close query rows: %w", closeErr))
			}
		}
		return nil, hookErr
	}
	return rows, nil
}

// ExecRendered executes a pre-rendered parameterized statement.
func (db DB) ExecRendered(ctx context.Context, statement render.Statement) (sql.Result, error) {
	if err := db.validStatement(statement); err != nil {
		return nil, err
	}
	operation := Operation{kind: ExecOperation, statement: statement}
	entered, err := db.beforeHooks(ctx, operation)
	if err != nil {
		return nil, afterHooks(ctx, operation, entered, err)
	}
	result, err := db.handle.ExecContext(ctx, statement.SQL(), statement.Args()...)
	if err != nil {
		err = fmt.Errorf("rasql: execute statement: %w", err)
	}
	if hookErr := afterHooks(ctx, operation, entered, err); hookErr != nil {
		return nil, hookErr
	}
	return result, nil
}

// Validate reports whether db came from New rather than being a zero DB. Every
// entry point in this package answers a zero value with this error instead of a
// nil dereference, and rasql/dynamic calls it for the same reason.
func (db DB) Validate() error {
	return db.valid()
}

// valid reports whether db came from New rather than being a zero DB, so every
// entry point answers a zero value with an error instead of a nil dereference.
func (db DB) valid() error {
	if isNil(db.handle) || isNil(db.dialect) {
		return fmt.Errorf("rasql: invalid DB: create one with rasql.New")
	}
	return nil
}

func (db DB) validStatement(statement render.Statement) error {
	if err := db.valid(); err != nil {
		return err
	}
	if statement.SQL() == "" {
		return fmt.Errorf("rasql: statement SQL must not be empty")
	}
	return nil
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflectValue := reflect.ValueOf(value)
	switch reflectValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflectValue.IsNil()
	default:
		return false
	}
}
