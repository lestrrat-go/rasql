// rasqlgen generates Go source from rasql schema snapshots and query templates.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"
	querytemplate "github.com/lestrrat-go/rasql/template"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rasqlgen <schema|query> [flags]")
	}
	switch args[0] {
	case "schema":
		return runSchema(args[1:])
	case "query":
		return runQuery(args[1:])
	default:
		return fmt.Errorf("unknown rasqlgen command %q", args[0])
	}
}

func runSchema(args []string) error {
	flags := flag.NewFlagSet("schema", flag.ContinueOnError)
	input := flags.String("input", "", "path to a JSON array of schema tables")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "path for generated Go source")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *input == "" || *packageName == "" || *output == "" {
		return errors.New("schema requires -input, -package, and -output")
	}
	data, err := os.ReadFile(*input)
	if err != nil {
		return fmt.Errorf("read schema input: %w", err)
	}
	var tables []schema.Table
	if err := json.Unmarshal(data, &tables); err != nil {
		return fmt.Errorf("decode schema input: %w", err)
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

func runQuery(args []string) error {
	flags := flag.NewFlagSet("query", flag.ContinueOnError)
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
