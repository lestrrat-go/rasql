// Package catalog produces schema descriptors from a database the caller has
// already opened.
//
// catalog is a tool-side package: a code-generation program imports it, and
// the runtime library never does. It registers no database driver and opens
// no connection, so importing it adds no driver to the importing module's
// graph. The caller opens the database with the driver it already uses and
// hands the handle over:
//
//	database, err := sql.Open("sqlite", "app.db")   // caller's driver
//	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{
//		Dialect: dialect.SQLite(),
//	})
//
// The descriptors it returns are ordinary values. A caller that must state a
// fact no database records -- a row type name, say -- sets it on the
// descriptor before generating from it, or passes it as a hint to
// generate.Store.
package catalog

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/schema"
)

// defaultHistoryTable is the migration history table a sweep skips when
// Options.HistoryTable is empty: migrate.New's own default history table
// name (migrate/runner.go's defaultHistoryTable), copied here rather than
// imported so that catalog never depends on migrate.
const defaultHistoryTable = "rasql_schema_migrations"

// DB is the database handle FromDatabase reads through. *sql.DB and
// *sql.Conn both satisfy it.
//
// It is deliberately narrower than *sql.DB and wider than inspect.Queryer.
// The one capability FromDatabase needs beyond querying is a transaction,
// because a sweep reads one table's metadata per round trip and every
// descriptor it returns must describe the same instant. A caller that
// already holds a transaction, or whose driver refuses the isolation level
// FromDatabase asks for, uses FromQueryer instead.
type DB interface {
	BeginTx(ctx context.Context, opts *sql.TxOptions) (*sql.Tx, error)
}

// Options selects which tables a call describes and how it reads them. The
// zero Options is invalid: Dialect is required.
type Options struct {
	// Dialect selects the SQL dialect the metadata is read as. Required. It
	// is not inferred from the handle, which carries no dialect.
	Dialect dialect.Dialect

	// Include names the tables to describe. Empty means every base table the
	// connection can see, minus HistoryTable and minus Exclude. A name in
	// Include that the database does not have is an error, because it was
	// asked for by name; a repeated name is an error too.
	Include []string

	// Exclude names tables a sweep skips. It is not accepted together with
	// Include. A name in Exclude that the database does not have is ignored,
	// because a sweep is asked for by shape rather than by name, so nothing
	// reports a misspelling here.
	Exclude []string

	// HistoryTable names the migration history table a sweep skips. Empty
	// means "rasql_schema_migrations", which is migrate.New's own default.
	// An application that renamed its history table with
	// migrate.NewWithHistoryTable names it here, or the generated store
	// grows a typed surface for the migration bookkeeping.
	HistoryTable string
}

// ErrNoTables reports that the selection matched no table at all. A caller
// scaffolding a project distinguishes "the database is empty, write the
// first migration" from a real failure with errors.Is.
var ErrNoTables = errors.New("catalog: no tables to describe")

// FromDatabase returns one descriptor per selected table, sorted by Schema
// and then Name.
//
// Every table is read inside one read-only transaction, begun with
// sql.LevelRepeatableRead, so the descriptors describe a single instant
// rather than a schema that may have moved between round trips. The
// transaction is committed before returning and rolled back on any failure.
// Nothing is written to the database.
//
// With Include empty this sweeps every base table the connection can see:
// views are never described, because inspection enumerates base tables only,
// and the migration history table is skipped (see Options.HistoryTable).
// A sweep that selects no table at all returns an error wrapping ErrNoTables
// rather than an empty slice, since generating a store from no tables is
// almost always a misconfigured connection.
//
// ctx bounds the whole read. FromDatabase adds no deadline of its own.
func FromDatabase(ctx context.Context, db DB, options Options) ([]schema.TableDef, error) {
	if isNil(db) {
		return nil, fmt.Errorf("catalog: db must not be nil")
	}
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, fmt.Errorf("catalog: begin inspection transaction: %w", err)
	}
	tables, err := selectTables(ctx, tx, options)
	if err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("catalog: commit inspection transaction: %w", err)
	}
	return tables, nil
}

// FromQueryer returns one descriptor per selected table, sorted by Schema
// and then Name, reading through queryer exactly as it is.
//
// It starts no transaction, so the caller owns the snapshot: passing an
// *sql.Tx gives the same one-instant guarantee FromDatabase provides for
// itself, and passing an *sql.DB gives none, since each table is then read
// on whatever connection the pool hands out. Use it when the caller already
// holds a transaction, when the driver refuses the isolation level
// FromDatabase asks for, or when the connection must be retained for
// SQLite's temp and attached databases.
//
// It is otherwise identical to FromDatabase, including the sweep rules and
// ErrNoTables.
func FromQueryer(ctx context.Context, queryer inspect.Queryer, options Options) ([]schema.TableDef, error) {
	if isNil(queryer) {
		return nil, fmt.Errorf("catalog: queryer must not be nil")
	}
	if err := validateOptions(options); err != nil {
		return nil, err
	}
	return selectTables(ctx, queryer, options)
}

// validateOptions rejects a nil Dialect, Include given together with
// Exclude, and a duplicate name inside either list. It is the one place both
// FromDatabase and FromQueryer check Options, so the two entry points reject
// the same input the same way.
func validateOptions(options Options) error {
	if isNil(options.Dialect) {
		return fmt.Errorf("catalog: options.Dialect must not be nil")
	}
	if len(options.Include) > 0 && len(options.Exclude) > 0 {
		return fmt.Errorf("catalog: options.Include and options.Exclude must not both be set")
	}
	if name, ok := firstDuplicate(options.Include); ok {
		return fmt.Errorf("catalog: options.Include has duplicate table name %q", name)
	}
	if name, ok := firstDuplicate(options.Exclude); ok {
		return fmt.Errorf("catalog: options.Exclude has duplicate table name %q", name)
	}
	return nil
}

// firstDuplicate reports the first name that appears more than once in
// names, preserving the order names are checked in.
func firstDuplicate(names []string) (string, bool) {
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		if _, ok := seen[name]; ok {
			return name, true
		}
		seen[name] = struct{}{}
	}
	return "", false
}

// selectTables is the one unexported implementation FromDatabase and
// FromQueryer both call once their own preconditions hold, so the two entry
// points cannot drift: FromDatabase differs only in opening and closing the
// transaction it hands to this function as queryer.
func selectTables(ctx context.Context, queryer inspect.Queryer, options Options) ([]schema.TableDef, error) {
	inspector, err := inspect.New(queryer, options.Dialect)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}

	var tables []schema.TableDef
	if len(options.Include) > 0 {
		tables, err = describeIncluded(ctx, inspector, options.Include)
	} else {
		tables, err = sweepTables(ctx, inspector, options)
	}
	if err != nil {
		return nil, err
	}

	sort.Slice(tables, func(i, j int) bool {
		if tables[i].Schema != tables[j].Schema {
			return tables[i].Schema < tables[j].Schema
		}
		return tables[i].Name < tables[j].Name
	})
	return tables, nil
}

// describeIncluded reads exactly the named tables, in the order given,
// through Inspector.Table -- never TableIn, even for a SQLite table whose
// TableNames scope would carry a schema, because a name in Include is a
// request by name and Inspector.Table already resolves it across every
// attached database.
func describeIncluded(ctx context.Context, inspector inspect.Inspector, names []string) ([]schema.TableDef, error) {
	tables := make([]schema.TableDef, len(names))
	for index, name := range names {
		table, err := inspector.Table(ctx, name)
		if err != nil {
			return nil, fmt.Errorf("catalog: %w", err)
		}
		tables[index] = table
	}
	return tables, nil
}

// sweepTables resolves every base table the connection can see, minus the
// migration history table and minus Exclude, matching sweepTables in
// cli/rasqlgen/bootstrap.go fact for fact: same history-table default, same
// exclusion handling, same empty-result refusal.
func sweepTables(ctx context.Context, inspector inspect.Inspector, options Options) ([]schema.TableDef, error) {
	names, err := inspector.TableNames(ctx)
	if err != nil {
		return nil, fmt.Errorf("catalog: %w", err)
	}

	historyTable := options.HistoryTable
	if historyTable == "" {
		historyTable = defaultHistoryTable
	}
	excluded := make(map[string]struct{}, len(options.Exclude)+1)
	excluded[historyTable] = struct{}{}
	for _, name := range options.Exclude {
		excluded[name] = struct{}{}
	}

	tables := make([]schema.TableDef, 0, len(names))
	for _, name := range names {
		if _, skip := excluded[name.Name]; skip {
			continue
		}
		table, err := describeSweptTable(ctx, inspector, name)
		if err != nil {
			return nil, fmt.Errorf("catalog: %w", err)
		}
		tables = append(tables, table)
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("catalog: sweep found no tables to describe: %w", ErrNoTables)
	}
	return tables, nil
}

// describeSweptTable reads name's descriptor, using the SQLite-scoped
// TableIn when name carries a Schema (main, temp, or an attached database)
// and the ordinary Table lookup otherwise, which is every case on
// PostgreSQL and MySQL, and an unscoped SQLite table -- the same rule
// cli/rasqlgen/bootstrap.go's describeSweptTable applies.
func describeSweptTable(ctx context.Context, inspector inspect.Inspector, name inspect.TableName) (schema.TableDef, error) {
	if name.Schema != "" {
		return inspector.TableIn(ctx, name.Schema, name.Name)
	}
	return inspector.Table(ctx, name.Name)
}

// isNil reports whether value is a nil interface or a non-nil interface
// holding a nil pointer, map, slice, channel, or function -- the same check
// inspect.isNil applies, duplicated here rather than exported across the
// package boundary.
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
