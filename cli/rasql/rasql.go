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

// Run executes the unified rasql command. Command output -- help text and
// what a successful command produced -- goes to output, and diagnostics go
// to diagnostics, so a caller can put the two on separate streams.
func Run(args []string, output, diagnostics io.Writer) error {
	if output == nil {
		return errors.New("rasql: command output must not be nil")
	}
	if diagnostics == nil {
		return errors.New("rasql: command diagnostics must not be nil")
	}
	if len(args) == 0 {
		return errors.New("usage: rasql <codegen|migrate> <command> [flags]")
	}
	// The flag package writes a help request and a parse diagnostic to one
	// writer, so the stream a subcommand's flag set prints on is chosen
	// here: a help request is output the caller asked for, and everything
	// else the flag package prints is a diagnostic.
	flagOutput := diagnostics
	if helpRequested(args) {
		flagOutput = output
	}
	switch args[0] {
	case "-h", "-help", "--help":
		printUsage(output)
		return flag.ErrHelp
	case "codegen":
		// Each context states its own commands, usage line, and unknown-command
		// error under the name it was called by, so they are stated once, there.
		return rasqlgen.Run(args[1:], output, flagOutput)
	case "migrate":
		return rasqlmigrate.Run(args[1:], output, flagOutput)
	default:
		return fmt.Errorf("unknown rasql command %q; expected codegen or migrate", args[0])
	}
}

// helpRequested reports whether args ask a command to print its flags. An
// argument after "--" is a value rather than a flag, so the scan stops
// there. A help token that a flag value swallowed ("-dialect -h") is not a
// help request, and such a run prints nothing at all unless parsing fails
// later on, which puts that one diagnostic on the output stream.
func helpRequested(args []string) bool {
	for _, arg := range args {
		if arg == "--" {
			return false
		}
		if arg == "-h" || arg == "-help" || arg == "--help" {
			return true
		}
	}
	return false
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
