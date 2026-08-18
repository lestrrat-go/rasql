// Package rasql implements the unified rasql command.
package rasql

import (
	"bytes"
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
	// The flag package writes a help request and a parse diagnostic to the
	// single writer a flag set holds, and only the flag package knows which
	// of the two it printed: a "-h" among the arguments is a help request in
	// one run and another flag's value in the next. The run reports that
	// itself, because a help request is the one outcome that returns
	// flag.ErrHelp. So a subcommand's flag set prints into a buffer, and the
	// error the run returned picks the stream that buffer is written to: the
	// help the caller asked for is command output, and everything else the
	// flag package printed is a diagnostic.
	var flagPrinted bytes.Buffer
	var err error
	switch args[0] {
	case "-h", "-help", "--help":
		printUsage(output)
		return flag.ErrHelp
	case "codegen":
		// Each context states its own commands, usage line, and unknown-command
		// error under the name it was called by, so they are stated once, there.
		err = rasqlgen.Run(args[1:], output, &flagPrinted)
	case "migrate":
		err = rasqlmigrate.Run(args[1:], output, &flagPrinted)
	default:
		return fmt.Errorf("unknown rasql command %q; expected codegen or migrate", args[0])
	}
	if flagPrinted.Len() > 0 {
		flagStream := diagnostics
		if errors.Is(err, flag.ErrHelp) {
			flagStream = output
		}
		_, _ = flagStream.Write(flagPrinted.Bytes())
	}
	return err
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
