// Package rasqlgen implements the rasqlgen command.
package rasqlgen

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/schema"
	querytemplate "github.com/lestrrat-go/rasql/template"
	_ "modernc.org/sqlite"
)

var (
	openDatabase = sql.Open
	// maxInputBytes caps how much of a -input file rasqlgen reads into
	// memory. It is a var, not a const, so tests can lower it.
	maxInputBytes = 64 << 20
)

// readInputFile reads path through a size-limited reader, rejecting the
// input once it exceeds maxInputBytes. A Stat-based size check is not
// enough here: a fifo or character device reports size 0 regardless of how
// much data it actually produces, so only reading through a limit catches
// an oversized input reliably.
func readInputFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, int64(maxInputBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxInputBytes {
		return nil, fmt.Errorf("input file %s exceeds maximum size of %d bytes", path, maxInputBytes)
	}
	return data, nil
}

// Run executes rasqlgen with args and writes command output to writer.
func Run(args []string, writer io.Writer) error {
	if writer == nil {
		return errors.New("rasqlgen: command output must not be nil")
	}
	if len(args) == 0 {
		return errors.New("usage: rasqlgen <bootstrap|schema|query|init> [flags]")
	}
	switch args[0] {
	case "-h", "-help", "--help":
		printUsage(writer)
		return flag.ErrHelp
	case "bootstrap":
		return runBootstrap(args[1:], writer)
	case "schema":
		return runSchema(args[1:], writer)
	case "query":
		return runQuery(args[1:], writer)
	case "init":
		return runInit(args[1:], writer)
	default:
		return fmt.Errorf("unknown rasqlgen command %q; expected bootstrap, schema, query, or init", args[0])
	}
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "Usage: rasqlgen <command> [flags]")
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "Commands:")
	_, _ = fmt.Fprintln(output, "  bootstrap Describe a live database as a checked-in schema package")
	_, _ = fmt.Fprintln(output, "  schema    Generate Go source from a schema")
	_, _ = fmt.Fprintln(output, "  query     Generate Go source from a SQL template")
	_, _ = fmt.Fprintln(output, "  init      Scaffold the generator program, gen/main.go")
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "Run 'rasqlgen <command> -h' for command flags.")
}

func runSchema(args []string, writer io.Writer) error {
	flags := newFlagSet("schema", writer)
	dsn := flags.String("dsn", "", "database connection string")
	source := flags.String("source", "", "directory of a Go package exporting func Tables() []schema.TableDef")
	dialectName := flags.String("dialect", "postgresql", "database dialect for -dsn")
	timeout := flags.Duration("timeout", 30*time.Second, "deadline for -dsn metadata inspection")
	var tableNames tableNames
	flags.Var(&tableNames, "table", "table to generate instead of sweeping every base table with -dsn, or to filter a -source package; repeat for multiple tables (duplicate values are rejected)")
	var excludes excludeTableNames
	flags.Var(&excludes, "exclude", "table to skip during a -dsn sweep; repeat for multiple tables (duplicate values are rejected); not accepted together with -table, and not supported with -source")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "directory for generated Go source files")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("schema -timeout must be positive, got %s", *timeout)
	}
	if *packageName == "" || *output == "" {
		return errors.New("schema requires -package and -output")
	}
	if len(tableNames) > 0 && len(excludes) > 0 {
		return errors.New("schema accepts -table or -exclude, not both")
	}
	modeCount := 0
	if *dsn != "" {
		modeCount++
	}
	if *source != "" {
		modeCount++
	}
	switch {
	case modeCount == 0:
		return errors.New("schema requires one of -dsn or -source")
	case modeCount > 1:
		return errors.New("schema accepts one of -dsn or -source, not both")
	}
	if len(excludes) > 0 && *source != "" {
		return errors.New("schema -exclude is not supported with -source")
	}
	if err := ensureOutputDirectory(*output); err != nil {
		return err
	}
	var tables []schema.TableDef
	switch {
	case *source != "":
		return runSchemaSource(*source, *packageName, *output, tableNames, writer)
	case *dsn != "":
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
		tables, err = sweepTables(ctx, inspector, tableNames, excludes, "schema")
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit %s inspection transaction: %w", databaseName, err)
		}
	default:
		return errors.New("schema requires either -dsn or -source")
	}
	if err := generate.WritePackage(*packageName, *output, tables...); err != nil {
		return fmt.Errorf("write schema output: %w", err)
	}
	return nil
}

// ensureOutputDirectory creates directory and any missing parents so
// `schema` and `bootstrap` no longer require -output to exist beforehand.
// It is a no-op when directory already exists as a directory or a symbolic
// link to one; when directory exists as something else, the failure
// surfaces here with directory named, rather than later as a generic "not a
// directory" error from whichever generate.Write* call runs next.
func ensureOutputDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create output directory %q: %w", directory, err)
	}
	return nil
}

func inspectTables(ctx context.Context, inspector inspect.Inspector, names []string) ([]schema.TableDef, error) {
	tables := make([]schema.TableDef, len(names))
	for index, name := range names {
		table, err := inspector.Table(ctx, name)
		if err != nil {
			return nil, err
		}
		tables[index] = table
	}
	return tables, nil
}

type tableNames []string

func (names *tableNames) String() string {
	return strings.Join(*names, ",")
}

func (names *tableNames) Set(name string) error {
	for _, existing := range *names {
		if existing == name {
			return fmt.Errorf("duplicate -table %q", name)
		}
	}
	*names = append(*names, name)
	return nil
}

func runQuery(args []string, writer io.Writer) error {
	flags := newFlagSet("query", writer)
	input := flags.String("input", "", "path to a static SQL template (max 64 MiB)")
	functionName := flags.String("function", "", "generated function name")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "path for generated Go source ending in _gen.go")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	if *input == "" || *functionName == "" || *dialectName == "" || *packageName == "" || *output == "" {
		return errors.New("query requires -input, -function, -dialect, -package, and -output")
	}
	data, err := readInputFile(*input)
	if err != nil {
		return fmt.Errorf("read query input: %w", err)
	}
	d, err := builtinDialect(*dialectName)
	if err != nil {
		return err
	}
	parsed, err := querytemplate.Parse(*functionName, string(data))
	if err != nil {
		return err
	}
	compiled, err := parsed.Compile(d)
	if err != nil {
		return err
	}
	source, err := compiled.GoSource(*packageName, *functionName)
	if err != nil {
		return err
	}
	if err := genfile.Write(*output, source); err != nil {
		return fmt.Errorf("write query output: %w", err)
	}
	return nil
}

// parseCommandFlags parses a subcommand's arguments and rejects whatever the
// flag set did not consume. A help request needs the same rejection as a
// successful parse: flag parsing stops at -h with the arguments that follow it
// still in Args(), and the command exits 0 on flag.ErrHelp, so returning the help
// error unchecked would drop those arguments without a diagnostic. Any other
// parse failure is returned as it is, because the flag package reports it more
// precisely than a leftover-argument message can.
func parseCommandFlags(flags *flag.FlagSet, args []string) error {
	err := flags.Parse(args)
	if err != nil && !errors.Is(err, flag.ErrHelp) {
		return err
	}
	if rest := flags.Args(); len(rest) > 0 {
		return unexpectedArgumentsError(rest)
	}
	return err
}

// unexpectedArgumentsError reports the leftover arguments a command did not consume.
// Every argument is quoted, so an empty argument stays visible and an argument
// holding spaces cannot be mistaken for several arguments.
func unexpectedArgumentsError(rest []string) error {
	return fmt.Errorf("unexpected arguments: %q", rest)
}

func newFlagSet(name string, writer io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(writer)
	return flags
}

func builtinDialect(name string) (dialect.Dialect, error) {
	switch name {
	case "postgres", "postgresql":
		return dialect.PostgreSQL(), nil
	case "mysql":
		return dialect.MySQL(), nil
	case "sqlite":
		return dialect.SQLite(), nil
	default:
		return nil, fmt.Errorf("unsupported dialect %q", name)
	}
}

func inspectionDriver(d dialect.Dialect) (string, string, error) {
	switch d.Name() {
	case "postgresql":
		return "pgx", "PostgreSQL", nil
	case "mysql":
		return "mysql", "MySQL", nil
	case "sqlite":
		return "sqlite", "SQLite", nil
	default:
		return "", "", fmt.Errorf("schema direct inspection supports PostgreSQL, MySQL, and SQLite, not %q", d.Name())
	}
}
