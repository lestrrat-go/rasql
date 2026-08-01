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
)

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
	input := flags.String("input", "", "path to a JSON array of schema tables")
	dsn := flags.String("dsn", "", "PostgreSQL connection string")
	dialectName := flags.String("dialect", "postgresql", "database dialect for -dsn")
	var tableNames tableNames
	flags.Var(&tableNames, "table", "database table to generate; repeat for multiple tables")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "path for generated Go source")
	if err := flags.Parse(args); err != nil {
		return err
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
		data, err := os.ReadFile(*input)
		if err != nil {
			return fmt.Errorf("read schema input: %w", err)
		}
		if err := json.Unmarshal(data, &tables); err != nil {
			return fmt.Errorf("decode schema input: %w", err)
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

type tableNames []string

func (names *tableNames) String() string {
	return strings.Join(*names, ",")
}

func (names *tableNames) Set(name string) error {
	*names = append(*names, name)
	return nil
}

func runQuery(args []string) error {
	flags := newFlagSet("query")
	input := flags.String("input", "", "path to a static SQL template")
	functionName := flags.String("function", "", "generated function name")
	dialectName := flags.String("dialect", "", "postgresql, mysql, sqlite, or spanner")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "path for generated Go source")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" || *functionName == "" || *dialectName == "" || *packageName == "" || *output == "" {
		return errors.New("query requires -input, -function, -dialect, -package, and -output")
	}
	data, err := os.ReadFile(*input)
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
	case "spanner":
		return dialect.Spanner(), nil
	default:
		return nil, fmt.Errorf("unsupported dialect %q", name)
	}
}
