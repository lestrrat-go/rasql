package rasqlgen

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
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

// defaultSweepHistorySkip is the migration history table a sweep skips by
// default, whether the sweep runs as `bootstrap` or as `schema -dsn` with no
// -table: migrate.New's own default history table name
// (migrate/runner.go:13). It is a default, not a rule -- an application
// that renamed its history table with migrate.NewWithHistoryTable describes
// it under its own name during a sweep unless that name is also passed to
// -exclude.
const defaultSweepHistorySkip = "rasql_schema_migrations"

func runBootstrap(args []string, writer io.Writer) error {
	flags := newFlagSet("bootstrap", writer)
	dsn := flags.String("dsn", "", "database connection string")
	dialectName := flags.String("dialect", "postgresql", "database dialect for -dsn")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "directory for generated Go source files")
	write := flags.Bool("write", false, "on a refresh, apply the reported changes instead of only reporting them; ignored on a first run into an empty -output")
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
	if err := ensureOutputDirectory(*output); err != nil {
		return err
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
	descriptors, err := sweepTables(ctx, inspector, tables, excludes, "bootstrap")
	if err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit %s inspection transaction: %w", databaseName, err)
	}

	fresh, err := isEmptyDirectory(*output)
	if err != nil {
		return err
	}
	if fresh {
		if err := generate.WriteDescriptionPackage(*packageName, *output, descriptors...); err != nil {
			return fmt.Errorf("write bootstrap output: %w", err)
		}
		return nil
	}
	return refreshBootstrapOutput(*packageName, *output, descriptors, *write, writer)
}

// isEmptyDirectory reports whether directory exists, is a directory, and
// holds no entries -- the condition that makes a bootstrap run a first
// write rather than a refresh of a directory bootstrap already owns.
func isEmptyDirectory(directory string) (bool, error) {
	info, err := os.Stat(directory)
	if err != nil {
		return false, err
	}
	if !info.IsDir() {
		return false, fmt.Errorf("bootstrap output %q is not a directory", directory)
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return false, err
	}
	return len(entries) == 0, nil
}

// refreshBootstrapOutput implements a bootstrap re-run into a directory
// bootstrap already wrote to: it validates packageName, validates that
// directory is one bootstrap recognizes as its own, loads its existing
// Tables() through a `go run` child the same way `rasqlgen schema -source`
// reads a schema source package, computes the drift against tables, prints
// the report to writer, and, only when write is true and the report is
// non-empty, applies it.
//
// The package-name check comes first because a refresh has paths that
// finish without rendering any source at all: a report-only run (write
// false) returns after printing, and an empty diff returns before that, so
// neither reaches the generate call that would otherwise reject the name.
// A first run into an empty -output is checked instead by
// generate.WriteDescriptionPackage, which renders on every path it takes.
//
// Loading the existing package runs with its own background context,
// separate from the timeout that bounds -dsn metadata inspection above:
// compiling and running a `go run` child is unrelated work with its own
// cost, not a database round trip that -timeout was ever meant to bound.
func refreshBootstrapOutput(packageName, output string, tables []schema.TableDef, write bool, writer io.Writer) error {
	// No tables are passed: this checks packageName alone. The files a
	// refresh writes come from the description generator, not from the
	// schema generator whose naming rules generate.Validate applies to
	// tables, so validating tables here would refuse input a bootstrap
	// otherwise accepts.
	if err := generate.Validate(packageName); err != nil {
		return err
	}
	if err := generate.ValidateDescriptionPackageOwnership(output); err != nil {
		return err
	}
	existing, err := loadExistingDescriptionTables(context.Background(), output)
	if err != nil {
		return err
	}

	diff := generate.DiffDescriptionPackage(existing, tables)
	report := diff.Report()
	if report != "" {
		if _, err := io.WriteString(writer, report); err != nil {
			return err
		}
	}
	if !write {
		return nil
	}
	if err := generate.ApplyDescriptionDiff(packageName, output, tables, diff); err != nil {
		return fmt.Errorf("apply bootstrap refresh: %w", err)
	}
	return nil
}

// sweepTables resolves the tables a bootstrap run or a `schema -dsn` run
// describes: exactly requestedTables when given, or every base table
// inspector.TableNames reports minus defaultSweepHistorySkip and
// excludedTables otherwise. Both commands share this one implementation
// rather than each sweeping the database its own way, so a sweep behaves
// identically -- same history-table default, same exclusion handling, same
// empty-result refusal -- regardless of which command ran it. commandName
// names the caller only for the empty-result error message, so that error
// still reads as "bootstrap found no tables to describe" or "schema found
// no tables to describe" rather than naming neither command.
func sweepTables(ctx context.Context, inspector inspect.Inspector, requestedTables, excludedTables []string, commandName string) ([]schema.TableDef, error) {
	if len(requestedTables) > 0 {
		return inspectTables(ctx, inspector, requestedTables)
	}

	names, err := inspector.TableNames(ctx)
	if err != nil {
		return nil, err
	}
	excluded := make(map[string]struct{}, len(excludedTables)+1)
	excluded[defaultSweepHistorySkip] = struct{}{}
	for _, name := range excludedTables {
		excluded[name] = struct{}{}
	}

	tables := make([]schema.TableDef, 0, len(names))
	for _, name := range names {
		if _, skip := excluded[name.Name]; skip {
			continue
		}
		table, err := describeSweptTable(ctx, inspector, name)
		if err != nil {
			return nil, err
		}
		tables = append(tables, table)
	}
	if len(tables) == 0 {
		return nil, fmt.Errorf("%s found no tables to describe", commandName)
	}
	return tables, nil
}

// describeSweptTable reads name's descriptor, using the SQLite-scoped
// TableIn when name carries a Schema (main, temp, or an attached database)
// and the ordinary Table lookup otherwise, which is every case on
// PostgreSQL and MySQL, and an unscoped SQLite table.
func describeSweptTable(ctx context.Context, inspector inspect.Inspector, name inspect.TableName) (schema.TableDef, error) {
	if name.Schema != "" {
		return inspector.TableIn(ctx, name.Schema, name.Name)
	}
	return inspector.Table(ctx, name.Name)
}
