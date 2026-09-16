package rasql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

// Handle is a database/sql handle that both reads rows and executes
// statements. *sql.DB, *sql.Conn, and *sql.Tx all implement it, and so does a
// logging or debugging wrapper around one. New requires it, so a DB can always
// run a write; a value that only reads is rejected where it is supplied rather
// than where a write is attempted.
// A debug Handle may return nil rows after logging a query; dynamic.Scan
// treats that as no result rows.
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

type scopeFinalizer struct {
	commit   func(context.Context) error
	rollback func(context.Context) error
}

func (f scopeFinalizer) Commit(ctx context.Context) error   { return f.commit(ctx) }
func (f scopeFinalizer) Rollback(ctx context.Context) error { return f.rollback(ctx) }

// IsTransaction reports whether db is bound to an open transaction.
func (db DB) IsTransaction() bool { return db.tx != nil }

// beginScope starts an owned transaction and returns its child DB and
// finalizer. BeginScope below wraps this to satisfy ScopeBeginner.
func (db DB) beginScope(ctx context.Context, opts *sql.TxOptions) (DB, ScopeFinalizer, error) {
	child, err := db.Begin(ctx, opts)
	if err != nil {
		return DB{}, nil, err
	}
	return child, scopeFinalizer{commit: child.commitContext, rollback: child.rollbackContext}, nil
}

// beginSavepoint starts an owned savepoint on a transaction DB. BeginSavepoint
// below wraps this to satisfy SavepointBeginner.
func (db DB) beginSavepoint(ctx context.Context) (DB, ScopeFinalizer, error) {
	if db.tx == nil {
		return DB{}, nil, fmt.Errorf("rasql: savepoint requires a transaction")
	}
	if !db.dialect.Supports(dialect.CapabilitySavepoint) {
		return DB{}, nil, fmt.Errorf("rasql: savepoint unsupported by dialect %s", db.dialect.Name())
	}
	name, err := db.atomicSavepointName()
	if err != nil {
		return DB{}, nil, err
	}
	if _, err := db.execRendered(ctx, stmt.New(sqltext.Text("SAVEPOINT "+name))); err != nil {
		return DB{}, nil, err
	}
	cleanupCtx, cancel := atomicCleanupContext(ctx)
	return db, scopeFinalizer{
		commit: func(context.Context) error { defer cancel(); return db.atomicReleaseSavepoint(cleanupCtx, name) },
		rollback: func(context.Context) error {
			defer cancel()
			return errors.Join(db.atomicRollbackSavepoint(cleanupCtx, name), db.atomicReleaseSavepoint(cleanupCtx, name))
		},
	}, nil
}

// BeginScope starts an owned transaction and returns the child DB as an
// Executor, along with the finalizer that ends it. It satisfies ScopeBeginner,
// so a DB itself can be handed to Within or ExecMutationBatch. The child
// carries db's engine profile and compiler and a fresh busy token, so a
// statement run concurrently on the child while another is still in flight is
// rejected rather than racing the same *sql.Tx.
func (db DB) BeginScope(ctx context.Context, opts *sql.TxOptions) (Executor, ScopeFinalizer, error) {
	child, finalizer, err := db.beginScope(ctx, opts)
	if err != nil {
		return nil, nil, err
	}
	child.busy = newExecutorBusy()
	return child, guardedScopeFinalizer{ScopeFinalizer: finalizer, busy: child.busy}, nil
}

// BeginSavepoint starts an owned savepoint on a transaction DB and returns the
// child DB as an Executor, along with the finalizer that ends it. It satisfies
// SavepointBeginner. The child shares db's busy token, because a savepoint
// runs on the same underlying transaction and connection as its parent.
func (db DB) BeginSavepoint(ctx context.Context) (Executor, ScopeFinalizer, error) {
	if db.busy != nil && !db.busy.acquire() {
		return nil, nil, planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	child, finalizer, err := db.beginSavepoint(ctx)
	if db.busy != nil {
		db.busy.release()
	}
	if err != nil {
		var planErr *PlanError
		if errors.As(err, &planErr) {
			return nil, nil, err
		}
		return nil, nil, planError("savepoint_unsupported", "scope", err.Error())
	}
	child.busy = db.busy
	return child, guardedScopeFinalizer{ScopeFinalizer: finalizer, busy: db.busy}, nil
}

// DB executes statements against one database/sql handle for one SQL dialect.
//
// It is the only type this package executes through. A DB from New runs
// statements on the handle it was given; a DB from Begin runs them inside the
// transaction it started, and Commit or Rollback finishes it. Everything that
// takes a DB takes either one, so moving work into a transaction changes which
// DB is passed and nothing else.
//
// A DB is a value. Copying it shares the handle, and WithHooks, WithObservers,
// and Begin
// return new values rather than changing the one they are called on. Its
// methods are safe for concurrent use when its Handle is, which for a
// transaction means one goroutine at a time, because *sql.Tx is bound to a
// single connection.
//
// A DB configured through AsExecutor also satisfies Executor: its Query and
// Exec methods compile and run statements through the engine profile and
// compiler AsExecutor attached, guarded by the same busy token a transaction
// DB carries so two goroutines cannot run statements on it at once.
type DB struct {
	handle                Handle
	dialect               dialect.Dialect
	hooks                 []Hook
	observers             []Observer
	extensionErrorHandler ExtensionErrorHandler
	invocationObservers   []InvocationObserver
	savepointScoped       bool
	// tx is the transaction this DB runs in, and is nil when it runs directly
	// on handle. When it is set it is the same value as handle.
	tx *sql.Tx
	// profile and compiler are set by AsExecutor, and propagate to every child
	// Begin, BeginScope, and BeginSavepoint return by ordinary struct copy.
	profile  EngineProfile
	compiler *querycompile.Compiler
	// busy guards concurrent use of this DB as an Executor. It is nil for a DB
	// AsExecutor built from a non-transaction DB, because concurrent statements
	// on a connection pool need no such guard.
	busy *executorBusy
}

// Option configures the DB that New returns.
type Option interface{ apply(*DB) error }

// New pairs a database/sql handle with the dialect used to render SQL for it.
// handle may be a *sql.DB for a connection pool, a *sql.Conn for one pinned
// connection, a *sql.Tx for a transaction that is already open, or any other
// Handle. New opens no connection and starts no transaction.
//
// A DB built from a *sql.Tx is a transaction: its Commit and Rollback finish
// that transaction, and its Begin reports an error rather than nesting. Use
// Atomic when work must compose inside an existing transaction.
//
// Optional hooks and observers configure the returned DB and observe every
// statement run through it and, unless narrowed or extended by WithHooks,
// WithObservers, or by Begin's own hooks parameter, every transaction Begin
// starts from it.
//
// `handle` and `d` must not be nil.
func New(handle Handle, d dialect.Dialect, options ...any) (DB, error) {
	if handle == nil {
		return DB{}, fmt.Errorf("rasql: handle must not be nil")
	}
	if d == nil {
		return DB{}, fmt.Errorf("rasql: dialect must not be nil")
	}
	db := DB{handle: handle, dialect: d}
	if transaction, ok := handle.(*sql.Tx); ok {
		db.tx = transaction
	}
	for _, option := range options {
		switch value := option.(type) {
		case Hook:
			var err error
			db, err = db.WithHooks(value)
			if err != nil {
				return DB{}, err
			}
		case Option:
			if err := value.apply(&db); err != nil {
				return DB{}, err
			}
		default:
			return DB{}, fmt.Errorf("rasql: unsupported database option %T", option)
		}
	}
	return db, nil
}

// WithHooks returns a copy of db that runs hooks around rendered queries and
// mutations, appended after the hooks db already carries. Every transaction
// Begin starts from the copy inherits them. It does not affect a DB that Begin
// already returned, and it does not wrap the database handle, so the SQL and
// bound arguments that reach database/sql are unchanged.
//
// No element of `hooks` may be nil.
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

// WithObservers returns a copy of db that reports observer and legacy hook
// failures to handler. Observers are appended in registration order.
//
// `handler` must not be nil when db already carries an observer or when
// `observers` is non-empty, and no element of `observers` may be nil.
func (db DB) WithObservers(handler ExtensionErrorHandler, observers ...Observer) (DB, error) {
	if err := db.valid(); err != nil {
		return DB{}, err
	}
	if len(db.observers)+len(observers) > 0 && handler == nil {
		return DB{}, fmt.Errorf("rasql: extension error handler must not be nil when observers are supplied")
	}
	configured, err := appendObservers(db.observers, observers)
	if err != nil {
		return DB{}, err
	}
	db.observers = configured
	db.extensionErrorHandler = handler
	return db, nil
}

// WithInvocationObservers returns a copy of db that reports complete
// execution, consumption, and transaction lifecycles.
//
// `handler` must not be nil when db already carries an invocation observer or
// when `observers` is non-empty, and no element of `observers` may be nil.
func (db DB) WithInvocationObservers(handler ExtensionErrorHandler, observers ...InvocationObserver) (DB, error) {
	if err := db.valid(); err != nil {
		return DB{}, err
	}
	if len(db.invocationObservers)+len(observers) > 0 && handler == nil {
		return DB{}, fmt.Errorf("rasql: extension error handler must not be nil when invocation observers are supplied")
	}
	configured, err := appendInvocationObservers(db.invocationObservers, observers)
	if err != nil {
		return DB{}, err
	}
	db.invocationObservers = configured
	db.extensionErrorHandler = handler
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
// error rather than quietly opening a savepoint. Atomic uses a savepoint for
// that case.
//
// The returned DB inherits db's hooks, with hooks appended after them, the
// same way WithHooks appends rather than replaces. A hook registered on db
// therefore also runs for every operation inside a transaction started from
// it.
//
// opts may be nil, which leaves the isolation level and read-only mode to the
// driver. Begin does not roll back on ctx cancellation by itself; that is
// database/sql's own behavior for the transaction it returns.
//
// No element of `hooks` may be nil.
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
	operation := Operation{kind: BeginOperation}
	callContext, invocation := db.startInvocation(ctx, operation)
	transaction, err := starter.BeginTx(callContext, opts)
	if err != nil {
		err = fmt.Errorf("rasql: begin transaction: %w", err)
		db.completeInvocation(invocation, operation, TransactionPhase, callContext, err, 0, false)
		return DB{}, err
	}
	if transaction == nil {
		// *sql.DB and *sql.Conn never do this: their BeginTx returns a
		// transaction or an error. A hand-written handle can, and the nil
		// would otherwise reach Rollback below as a nil receiver.
		err := fmt.Errorf("rasql: handle returned a nil transaction without an error")
		db.completeInvocation(invocation, operation, TransactionPhase, callContext, err, 0, false)
		return DB{}, err
	}
	db.completeInvocation(invocation, operation, TransactionPhase, callContext, nil, 0, false)
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
	return db.commitContext(context.Background())
}

func (db DB) commitContext(ctx context.Context) error {
	if db.savepointScoped {
		return fmt.Errorf("rasql: atomic savepoint DB cannot commit its outer transaction")
	}
	if db.tx == nil {
		return fmt.Errorf("rasql: this DB is not a transaction: Commit needs one from Begin")
	}
	operation := Operation{kind: CommitOperation}
	callContext, invocation := db.startInvocation(ctx, operation)
	if err := db.tx.Commit(); err != nil {
		err = fmt.Errorf("rasql: commit transaction: %w", err)
		db.completeInvocation(invocation, operation, TransactionPhase, callContext, err, 0, false)
		return err
	}
	db.completeInvocation(invocation, operation, TransactionPhase, callContext, nil, 0, false)
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
	return db.rollbackContext(context.Background())
}

func (db DB) rollbackContext(ctx context.Context) error {
	if db.savepointScoped {
		return fmt.Errorf("rasql: atomic savepoint DB cannot roll back its outer transaction")
	}
	if db.tx == nil {
		return fmt.Errorf("rasql: this DB is not a transaction: Rollback needs one from Begin")
	}
	operation := Operation{kind: RollbackOperation}
	callContext, invocation := db.startInvocation(ctx, operation)
	if err := db.tx.Rollback(); err != nil {
		if errors.Is(err, sql.ErrTxDone) {
			db.completeInvocation(invocation, operation, TransactionPhase, callContext, nil, 0, false)
			return nil
		}
		err = fmt.Errorf("rasql: roll back transaction: %w", err)
		db.completeInvocation(invocation, operation, TransactionPhase, callContext, err, 0, false)
		return err
	}
	db.completeInvocation(invocation, operation, TransactionPhase, callContext, nil, 0, false)
	return nil
}

// QueryRendered executes statement and returns its result rows. It reports the
// driver execution lifecycle only; use Query for consumption observation.
// The caller owns the returned rows: hand them to dynamic.Scan, which closes
// them, or close them directly. A debug Handle that logs the statement instead
// of running it may return nil rows, which dynamic.Scan reads as no result rows.
func (db DB) QueryRendered(ctx context.Context, s stmt.Statement) (*sql.Rows, error) {
	if err := db.validStatement(s); err != nil {
		return nil, err
	}
	operation := Operation{kind: QueryOperation, stmt: s}
	callContext, invocation := db.startInvocation(ctx, operation)
	entered, err := db.beforeHooks(callContext, operation)
	if err != nil {
		if extensionErr := db.afterHooks(callContext, operation, entered, err); extensionErr != nil {
			err = errors.Join(err, extensionErr)
		}
		db.completeInvocation(invocation, operation, ExecutionPhase, callContext, err, 0, false)
		return nil, err
	}
	rows, driverErr := db.handle.QueryContext(callContext, s.SQL(), s.BoundArgs()...)
	if driverErr != nil {
		err = fmt.Errorf("rasql: execute query: %w", driverErr)
	}
	db.observe(callContext, operation, driverErr)
	db.completeInvocation(invocation, operation, ExecutionPhase, callContext, err, 0, false)
	if extensionErr := db.afterHooks(callContext, operation, entered, err); extensionErr != nil {
		if rows != nil {
			if closeErr := rows.Close(); closeErr != nil {
				extensionErr.Errors = append(extensionErr.Errors, fmt.Errorf("rasql: close query rows: %w", closeErr))
			}
		}
		return nil, errors.Join(err, extensionErr)
	}
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// queryOwned executes a statement and returns rows whose consumption can be
// observed through the invocation lifecycle API. Query wraps it to satisfy
// Executor.Query.
func (db DB) queryOwned(ctx context.Context, s stmt.Statement) (*ownedRows, error) {
	if err := db.validStatement(s); err != nil {
		return nil, err
	}
	operation := Operation{kind: QueryOperation, stmt: s}
	callContext, execution := db.startInvocation(ctx, operation)
	entered, err := db.beforeHooks(callContext, operation)
	if err != nil {
		if extensionErr := db.afterHooks(callContext, operation, entered, err); extensionErr != nil {
			err = errors.Join(err, extensionErr)
		}
		db.completeInvocation(execution, operation, ExecutionPhase, callContext, err, 0, false)
		return nil, err
	}
	rows, driverErr := db.handle.QueryContext(callContext, s.SQL(), s.BoundArgs()...)
	if driverErr != nil {
		err = fmt.Errorf("rasql: execute query: %w", driverErr)
	}
	db.observe(callContext, operation, driverErr)
	db.completeInvocation(execution, operation, ExecutionPhase, callContext, err, 0, false)
	if extensionErr := db.afterHooks(callContext, operation, entered, err); extensionErr != nil {
		if rows != nil {
			if closeErr := rows.Close(); closeErr != nil {
				extensionErr.Errors = append(extensionErr.Errors, fmt.Errorf("rasql: close query rows: %w", closeErr))
			}
		}
		return nil, errors.Join(err, extensionErr)
	}
	if err != nil {
		return nil, err
	}
	_, consumption := db.startInvocation(callContext, operation)
	return &ownedRows{rows: rows, db: db, operation: operation, invocation: consumption}, nil
}

// execRendered executes a pre-rendered parameterized statement. Exec wraps it
// to satisfy Executor.Exec.
func (db DB) execRendered(ctx context.Context, s stmt.Statement) (sql.Result, error) {
	if err := db.validStatement(s); err != nil {
		return nil, err
	}
	operation := Operation{kind: ExecOperation, stmt: s}
	callContext, invocation := db.startInvocation(ctx, operation)
	entered, err := db.beforeHooks(callContext, operation)
	if err != nil {
		if extensionErr := db.afterHooks(callContext, operation, entered, err); extensionErr != nil {
			err = errors.Join(err, extensionErr)
		}
		db.completeInvocation(invocation, operation, ExecutionPhase, callContext, err, 0, false)
		return nil, err
	}
	result, driverErr := db.handle.ExecContext(callContext, s.SQL(), s.BoundArgs()...)
	if driverErr != nil {
		err = fmt.Errorf("rasql: execute statement: %w", driverErr)
	}
	db.observe(callContext, operation, driverErr)
	db.completeInvocation(invocation, operation, ExecutionPhase, callContext, err, 0, false)
	if extensionErr := db.afterHooks(callContext, operation, entered, err); extensionErr != nil {
		return result, errors.Join(err, extensionErr)
	}
	return result, err
}

// Write renders and executes a write statement.
// It rejects a statement carrying a RETURNING clause, because ExecContext
// discards result rows; RenderWrite reads them instead.
//
// `s` must not be nil.
func Write(ctx context.Context, db DB, s query.WriteStatement) (sql.Result, error) {
	if err := db.valid(); err != nil {
		return nil, err
	}
	if s != nil && len(s.Returning()) > 0 {
		return nil, fmt.Errorf("rasql: write statement has a RETURNING clause: use QueryWrite to read its rows")
	}
	rendered, err := render.Write(db.Dialect(), s)
	if err != nil {
		return nil, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return db.execRendered(ctx, rendered)
}

// RenderWrite renders a write statement that must carry a RETURNING clause,
// and reports an error when it does not.
//
// `s` must not be nil.
func RenderWrite(db DB, s query.WriteStatement) (stmt.Statement, error) {
	if err := db.valid(); err != nil {
		return stmt.Statement{}, err
	}
	if s == nil || len(s.Returning()) == 0 {
		return stmt.Statement{}, fmt.Errorf("rasql: write statement has no RETURNING clause: use Exec for a statement that returns no rows")
	}
	rendered, err := render.Write(db.Dialect(), s)
	if err != nil {
		return stmt.Statement{}, fmt.Errorf("rasql: render write statement: %w", err)
	}
	return rendered, nil
}

// Query renders no SQL of its own: it runs statement, already compiled and
// rendered by the caller, and satisfies Executor.Query. A DB guards Query and
// Exec with a busy token when it is a transaction, so a second call made while
// the first is still running is rejected with transaction_concurrent_use
// rather than racing the same *sql.Tx.
func (db DB) Query(ctx context.Context, statement stmt.Statement) (ResultRows, error) {
	if db.busy != nil && !db.busy.acquire() {
		return nil, planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	rows, err := db.queryOwned(ctx, statement)
	if err != nil {
		if db.busy != nil {
			db.busy.release()
		}
		return nil, err
	}
	if rows == nil {
		if db.busy != nil {
			db.busy.release()
		}
		return nil, nil
	}
	if db.busy == nil {
		return rows, nil
	}
	return &exclusiveRows{ResultRows: rows, release: db.busy.release}, nil
}

// Exec runs statement, already compiled and rendered by the caller, and
// satisfies Executor.Exec. See Query for the busy-token guard it shares.
func (db DB) Exec(ctx context.Context, statement stmt.Statement) (sql.Result, error) {
	if db.busy != nil && !db.busy.acquire() {
		return nil, planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	if db.busy != nil {
		defer db.busy.release()
	}
	return db.execRendered(ctx, statement)
}

func (db DB) queryCompiler() *querycompile.Compiler { return db.compiler }

func (db DB) executionDurability() executionDurabilityEvidence {
	if !db.IsTransaction() {
		return executionDurabilityCommitted
	}
	return executionDurabilityPending
}

// Validate reports whether db came from New rather than being a zero DB.
// Every entry point in this package calls it, so a zero DB produces an error
// rather than a nil dereference.
func (db DB) Validate() error {
	return db.valid()
}

// ValidateStatement reports whether db came from New and s carries SQL to
// run. rasql's typed QueryRendered calls it so an unusable statement is
// reported by the call itself rather than by the sequence it would return.
func (db DB) ValidateStatement(s stmt.Statement) error {
	return db.validStatement(s)
}

// valid reports whether db came from New rather than being a zero DB, so every
// entry point answers a zero value with an error instead of a nil dereference.
func (db DB) valid() error {
	if db.handle == nil || db.dialect == nil {
		return fmt.Errorf("rasql: invalid DB: create one with rasql.New")
	}
	return nil
}

func (db DB) validStatement(s stmt.Statement) error {
	if err := db.valid(); err != nil {
		return err
	}
	if strings.TrimSpace(s.SQL()) == "" {
		return fmt.Errorf("rasql: statement SQL must not be empty")
	}
	return nil
}

// executorBusy is a one-token guard against running two statements
// concurrently through the same transaction DB, which would otherwise race
// the same *sql.Tx.
type executorBusy struct{ token chan struct{} }

func newExecutorBusy() *executorBusy { return &executorBusy{token: make(chan struct{}, 1)} }
func (b *executorBusy) acquire() bool {
	select {
	case b.token <- struct{}{}:
		return true
	default:
		return false
	}
}
func (b *executorBusy) release() { <-b.token }

// guardedScopeFinalizer wraps the ScopeFinalizer BeginScope or BeginSavepoint
// returns so that Commit and Rollback also honor the child DB's busy token.
type guardedScopeFinalizer struct {
	ScopeFinalizer
	busy *executorBusy
}

func (f guardedScopeFinalizer) Commit(ctx context.Context) error {
	if f.busy != nil && !f.busy.acquire() {
		return planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	if f.busy != nil {
		defer f.busy.release()
	}
	return f.ScopeFinalizer.Commit(ctx)
}
func (f guardedScopeFinalizer) Rollback(ctx context.Context) error {
	if f.busy != nil && !f.busy.acquire() {
		return planError("transaction_concurrent_use", "executor", "transaction executor is already in use")
	}
	if f.busy != nil {
		defer f.busy.release()
	}
	return f.ScopeFinalizer.Rollback(ctx)
}

// exclusiveRows releases db's busy token once, on Finish, so a statement run
// after the rows it returned are fully consumed sees the token free again.
type exclusiveRows struct {
	ResultRows
	release  func()
	released atomic.Bool
}

func (r *exclusiveRows) Finish(err error, early bool) error {
	result := r.ResultRows.Finish(err, early)
	if r.released.CompareAndSwap(false, true) {
		r.release()
	}
	return result
}
