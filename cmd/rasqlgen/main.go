// rasqlgen generates Go source from rasql schemas and query templates.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/schema"
	querytemplate "github.com/lestrrat-go/rasql/template"
)

var (
	openDatabase            = sql.Open
	commandOutput io.Writer = os.Stderr
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

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rasqlgen <schema|query> [flags]")
	}
	switch args[0] {
	case "-h", "-help", "--help":
		printUsage(commandOutput)
		return flag.ErrHelp
	case "schema":
		return runSchema(args[1:])
	case "query":
		return runQuery(args[1:])
	default:
		return fmt.Errorf("unknown rasqlgen command %q", args[0])
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: rasqlgen <command> [flags]")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Commands:")
	fmt.Fprintln(output, "  schema    Generate Go source from a schema")
	fmt.Fprintln(output, "  query     Generate Go source from a SQL template")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Run 'rasqlgen <command> -h' for command flags.")
}

func runSchema(args []string) error {
	flags := newFlagSet("schema")
	input := flags.String("input", "", "path to a JSON array of schema tables (max 64 MiB)")
	dsn := flags.String("dsn", "", "PostgreSQL connection string")
	dialectName := flags.String("dialect", "postgresql", "database dialect for -dsn")
	var tableNames tableNames
	flags.Var(&tableNames, "table", "database table to generate; repeat for multiple tables (duplicate values are rejected)")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "path for generated Go source")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if rest := flags.Args(); len(rest) > 0 {
		return unexpectedArgumentsError(rest)
	}
	if *packageName == "" || *output == "" {
		return errors.New("schema requires -package and -output")
	}
	if *input != "" && *dsn != "" {
		return errors.New("schema accepts either -input or -dsn, not both")
	}
	var tables []schema.Table
	switch {
	case *input != "":
		data, err := readInputFile(*input)
		if err != nil {
			return fmt.Errorf("read schema input: %w", err)
		}
		if err := json.Unmarshal(data, &tables); err != nil {
			return fmt.Errorf("decode schema input: %w", err)
		}
		tables, err = filterTables(tables, tableNames)
		if err != nil {
			return err
		}
	case *dsn != "":
		if len(tableNames) == 0 {
			return errors.New("schema with -dsn requires at least one -table")
		}
		d, err := builtinDialect(*dialectName)
		if err != nil {
			return err
		}
		if d.Name() != dialect.PostgreSQL().Name() {
			return fmt.Errorf("schema direct inspection supports PostgreSQL, not %q", d.Name())
		}
		database, err := openDatabase("pgx", *dsn)
		if err != nil {
			return fmt.Errorf("open PostgreSQL database: %w", err)
		}
		defer database.Close()
		inspector, err := inspect.New(database, d)
		if err != nil {
			return err
		}
		tables, err = inspectTables(context.Background(), inspector, tableNames)
		if err != nil {
			return err
		}
	default:
		return errors.New("schema requires either -input or -dsn")
	}
	source, err := generate.Schema(*packageName, tables...)
	if err != nil {
		return err
	}
	if err := os.WriteFile(*output, source, 0o600); err != nil {
		return fmt.Errorf("write schema output: %w", err)
	}
	return nil
}

func inspectTables(ctx context.Context, inspector inspect.Inspector, names []string) ([]schema.Table, error) {
	tables := make([]schema.Table, len(names))
	for index, name := range names {
		table, err := inspector.Table(ctx, name)
		if err != nil {
			return nil, err
		}
		tables[index] = table
	}
	return tables, nil
}

func filterTables(tables []schema.Table, names []string) ([]schema.Table, error) {
	if len(names) == 0 {
		return tables, nil
	}
	requested := make(map[string]struct{}, len(names))
	for _, name := range names {
		requested[name] = struct{}{}
	}
	filtered := make([]schema.Table, 0, len(tables))
	found := make(map[string]struct{}, len(names))
	for _, table := range tables {
		if _, ok := requested[table.Name]; !ok {
			continue
		}
		filtered = append(filtered, table)
		found[table.Name] = struct{}{}
	}
	for _, name := range names {
		if _, ok := found[name]; !ok {
			return nil, fmt.Errorf("schema input has no table %q", name)
		}
	}
	return filtered, nil
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

func runQuery(args []string) error {
	flags := newFlagSet("query")
	input := flags.String("input", "", "path to a static SQL template (max 64 MiB)")
	functionName := flags.String("function", "", "generated function name")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "path for generated Go source")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if rest := flags.Args(); len(rest) > 0 {
		return unexpectedArgumentsError(rest)
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
	if err := os.WriteFile(*output, source, 0o600); err != nil {
		return fmt.Errorf("write query output: %w", err)
	}
	return nil
}

// unexpectedArgumentsError reports the leftover arguments a command did not consume.
// Every argument is quoted, so an empty argument stays visible and an argument
// holding spaces cannot be mistaken for several arguments.
func unexpectedArgumentsError(rest []string) error {
	return fmt.Errorf("unexpected arguments: %q", rest)
}

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(commandOutput)
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
