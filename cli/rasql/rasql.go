// Package rasql implements the unified rasql command.
package rasql

import (
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/cli/rasqlmigrate"
)

// Run executes the unified rasql command and writes command output to writer.
func Run(args []string, writer io.Writer) error {
	if writer == nil {
		return errors.New("rasql: command output must not be nil")
	}
	if len(args) == 0 {
		return errors.New("usage: rasql <codegen|migrate> <command> [flags]")
	}
	switch args[0] {
	case "-h", "-help", "--help":
		printUsage(writer)
		return flag.ErrHelp
	case "codegen":
		return runCodegen(args[1:], writer)
	case "migrate":
		return runMigrate(args[1:], writer)
	default:
		return fmt.Errorf("unknown rasql command %q; expected codegen or migrate", args[0])
	}
}

func runCodegen(args []string, writer io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: rasql codegen <init> [flags]")
	}
	switch args[0] {
	case "-h", "-help", "--help":
		printCodegenUsage(writer)
		return flag.ErrHelp
	case "init":
		return rasqlgen.Run(args, writer)
	default:
		return fmt.Errorf("unknown rasql codegen command %q; expected init", args[0])
	}
}

func runMigrate(args []string, writer io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: rasql migrate <new|diff|diff-live|plan|apply|status|verify> [flags]")
	}
	return rasqlmigrate.Run(args, writer)
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "Usage: rasql <context> <command> [flags]")
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "Contexts:")
	_, _ = fmt.Fprintln(output, "  codegen   Scaffold the generator program that writes Go source")
	_, _ = fmt.Fprintln(output, "  migrate   Create and apply versioned SQL migrations")
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "Run 'rasql <context> -h' for context commands.")
}

func printCodegenUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "Usage: rasql codegen <command> [flags]")
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "Commands:")
	_, _ = fmt.Fprintln(output, "  init      Scaffold the generator program, gen/main.go")
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "Run 'rasql codegen <command> -h' for command flags.")
}
