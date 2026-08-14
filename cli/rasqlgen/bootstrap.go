package rasqlgen

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/schema"
)

// excludeTableNames collects -exclude values with the same duplicate check
// tableNames applies to -table, kept as a separate type only so the error
// it reports names the flag that actually rejected the value.
type excludeTableNames []string

func (names *excludeTableNames) String() string {
	return strings.Join(*names, ",")
}

func (names *excludeTableNames) Set(name string) error {
	for _, existing := range *names {
		if existing == name {
			return fmt.Errorf("duplicate -exclude %q", name)
		}
	}
	*names = append(*names, name)
	return nil
}

// defaultBootstrapHistorySkip is the migration history table a sweep skips
// by default: migrate.New's own default history table name
// (migrate/runner.go:13). It is a default, not a rule -- an application
// that renamed its history table with migrate.NewWithHistoryTable describes
// it under its own name during a sweep unless that name is also passed to
// -exclude.
const defaultBootstrapHistorySkip = "rasql_schema_migrations"

func runBootstrap(args []string, writer io.Writer) error {
	flags := newFlagSet("bootstrap", writer)
	dsn := flags.String("dsn", "", "database connection string")
	dialectName := flags.String("dialect", "postgresql", "database dialect for -dsn")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "directory for generated Go source files")
	timeout := flags.Duration("timeout", 30*time.Second, "deadline for -dsn metadata inspection")
	var tables tableNames
	flags.Var(&tables, "table", "table to describe instead of sweeping every base table; repeat for multiple tables (duplicate values are rejected)")
	var excludes excludeTableNames
	flags.Var(&excludes, "exclude", "table to skip during a sweep; repeat for multiple tables (duplicate values are rejected); not accepted together with -table")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	if *dsn == "" {
		return errors.New("bootstrap requires -dsn")
	}
	if *packageName == "" || *output == "" {
		return errors.New("bootstrap requires -package and -output")
	}
	if *timeout <= 0 {
		return fmt.Errorf("bootstrap -timeout must be positive, got %s", *timeout)
	}
	if len(tables) > 0 && len(excludes) > 0 {
		return errors.New("bootstrap accepts -table or -exclude, not both")
	}

	d, err := builtinDialect(*dialectName)
	if err != nil {
		return err
	}
	driverName, databaseName, err := inspectionDriver(d)
	if err != nil {
		return err
	}
	database, err := openDatabase(driverName, *dsn)
	if err != nil {
		return fmt.Errorf("open %s database: %w", databaseName, err)
	}
	defer func() { _ = database.Close() }()
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return fmt.Errorf("begin %s inspection transaction: %w", databaseName, err)
	}
	inspector, err := inspect.New(tx, d)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	descriptors, err := bootstrapTables(ctx, inspector, tables, excludes)
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s inspection transaction: %w", databaseName, err)
	}

	if err := generate.WriteDescriptionPackage(*packageName, *output, descriptors...); err != nil {
		return fmt.Errorf("write bootstrap output: %w", err)
	}
	return nil
}

// bootstrapTables resolves the tables a bootstrap run describes: exactly
// requestedTables when given, or every base table inspector.TableNames
// reports minus defaultBootstrapHistorySkip and excludedTables otherwise.
func bootstrapTables(ctx context.Context, inspector inspect.Inspector, requestedTables, excludedTables []string) ([]schema.TableDef, error) {
	if len(requestedTables) > 0 {
		return inspectTables(ctx, inspector, requestedTables)
	}

	names, err := inspector.TableNames(ctx)
	if err != nil {
		return nil, err
	}
	excluded := make(map[string]struct{}, len(excludedTables)+1)
	excluded[defaultBootstrapHistorySkip] = struct{}{}
	for _, name := range excludedTables {
		excluded[name] = struct{}{}
	}

	tables := make([]schema.TableDef, 0, len(names))
	for _, name := range names {
		if _, skip := excluded[name.Name]; skip {
			continue
		}
		table, err := bootstrapDescribe(ctx, inspector, name)
		if err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if len(tables) == 0 {
		return nil, errors.New("bootstrap found no tables to describe")
	}
	return tables, nil
}

// bootstrapDescribe reads name's descriptor, using the SQLite-scoped
// TableIn when name carries a Schema (main, temp, or an attached database)
// and the ordinary Table lookup otherwise, which is every case on
// PostgreSQL and MySQL, and an unscoped SQLite table.
func bootstrapDescribe(ctx context.Context, inspector inspect.Inspector, name inspect.TableName) (schema.TableDef, error) {
	if name.Schema != "" {
		return inspector.TableIn(ctx, name.Schema, name.Name)
	}
	return inspector.Table(ctx, name.Name)
}
