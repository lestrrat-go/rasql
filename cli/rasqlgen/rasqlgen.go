// Package rasqlgen implements the rasqlgen command.
package rasqlgen

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"

	"github.com/lestrrat-go/rasql/generate"
)

// ExitCode maps command errors to the CLI contract: success, stale/drift, or
// usage/configuration/engine failure.
func ExitCode(err error) int {
	if err == nil || errors.Is(err, flag.ErrHelp) {
		return 0
	}
	if errors.Is(err, generate.ErrStale) || errors.Is(err, ErrDrift) {
		return 1
	}
	return 2
}

// Run executes the codegen commands of the unified rasql command with args.
// Command output -- help text and what a successful command produced -- goes
// to output, and what the flag package prints while parsing goes to
// diagnostics, so the unified command can keep the two on separate streams.
func Run(args []string, output, diagnostics io.Writer) error {
	return RunContext(context.Background(), args, output, diagnostics)
}

// RunContext executes a codegen command with the supplied invocation context.
// Cancellation is propagated through source materialization and publication.
func RunContext(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if output == nil || diagnostics == nil {
		return errors.New("rasqlgen: command output must not be nil")
	}
	// A flag set has one writer for both a help listing and a parse
	// diagnostic, and only the run knows which of the two it printed,
	// because a help request is the one outcome that returns flag.ErrHelp.
	// So the flag set prints into a buffer and the returned error picks the
	// stream that buffer is written to: the help the caller asked for is
	// command output, and everything else the flag package printed is a
	// diagnostic.
	var flagPrinted bytes.Buffer
	err := command{
		program:       "rasql codegen",
		flagSetPrefix: "rasql codegen ",
		output:        output,
		diagnostics:   &flagPrinted,
		ctx:           ctx,
	}.run(args)
	if flagPrinted.Len() > 0 {
		flagStream := diagnostics
		if errors.Is(err, flag.ErrHelp) {
			flagStream = output
		}
		_, _ = flagStream.Write(flagPrinted.Bytes())
	}
	return err
}

// RunTopLevel dispatches the schema, generate, and check commands exposed by
// the unified rasql binary.
func RunTopLevel(args []string, output, diagnostics io.Writer) error {
	return RunTopLevelContext(context.Background(), args, output, diagnostics)
}

// RunTopLevelContext dispatches unified rasql schema commands with context.
func RunTopLevelContext(ctx context.Context, args []string, output, diagnostics io.Writer) error {
	if output == nil || diagnostics == nil {
		return errors.New("rasqlgen: command output must not be nil")
	}
	var printed bytes.Buffer
	if ctx == nil {
		ctx = context.Background()
	}
	err := command{program: "rasql", flagSetPrefix: "rasql ", output: output, diagnostics: &printed, ctx: ctx}.run(args)
	if printed.Len() > 0 {
		stream := diagnostics
		if errors.Is(err, flag.ErrHelp) {
			stream = output
		}
		_, _ = stream.Write(printed.Bytes())
	}
	return err
}

// RunLegacy executes the same commands under the standalone rasqlgen
// command, which reports its own name and writes everything to writer.
func RunLegacy(args []string, writer io.Writer) error {
	if writer == nil {
		return errors.New("rasqlgen: command output must not be nil")
	}
	return command{
		program: "rasqlgen",
		// The standalone binary prints what it has always printed, so its
		// flag set keeps the bare subcommand name in "Usage of init:" and
		// both streams stay on the one writer the caller supplied.
		flagSetPrefix: "",
		output:        writer,
		diagnostics:   writer,
		ctx:           context.Background(),
	}.run(args)
}

// command holds what one run calls itself and where that run writes.
type command struct {
	// program names the command in usage lines and error messages:
	// "rasqlgen" under the standalone binary, "rasql codegen" under the
	// unified rasql command.
	program string
	// flagSetPrefix goes in front of a subcommand's name to make the flag
	// set name the flag package prints as "Usage of <name>:". The unified
	// command sets "rasql codegen ", so a diagnostic says which command
	// produced it; the standalone binary leaves it empty and keeps
	// printing the bare subcommand name it always printed.
	flagSetPrefix string
	// output receives help text and what a successful command produced.
	output io.Writer
	// diagnostics receives everything the flag package prints while
	// parsing, which is a parse diagnostic with the usage block under it,
	// or the flag listing a help request asked for. Which of the two a run
	// printed is only known once it returns, so whoever built the command
	// sorts them: this writer is the single writer under the standalone
	// binary, and a buffer Run routes by the returned error.
	diagnostics io.Writer
	ctx         context.Context
}

func (c command) run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s <generate> [flags]", c.program)
	}
	switch args[0] {
	case "-h", "-help", "--help":
		c.printUsage()
		return flag.ErrHelp
	case "generate":
		return c.runGenerate(args[1:])
	case "check":
		return c.runGenerate(append([]string{"-check"}, args[1:]...))
	case "schema":
		if c.program != "rasql" {
			return fmt.Errorf("unknown %s command %q; expected generate", c.program, args[0])
		}
		if len(args) < 2 {
			return fmt.Errorf("usage: %s schema <update|import|verify> [flags]", c.program)
		}
		switch args[1] {
		case "update":
			return c.runSchemaUpdate(args[2:])
		case "import":
			return c.runSchemaImport(args[2:])
		case "verify":
			return c.runSchemaVerify(args[2:])
		default:
			return fmt.Errorf("unknown %s schema command %q; expected update, import, or verify", c.program, args[1])
		}
	default:
		return fmt.Errorf("unknown %s command %q; expected generate", c.program, args[0])
	}
}

func (c command) printUsage() {
	_, _ = fmt.Fprintf(c.output, "Usage: %s <command> [flags]\n", c.program)
	_, _ = fmt.Fprintln(c.output)
	_, _ = fmt.Fprintln(c.output, "Commands:")
	generateDescription := "Generate the store package from a live database"
	if c.program == "rasql" {
		generateDescription = "Generate the store package from a live database or schema lock"
	}
	_, _ = fmt.Fprintln(c.output, "  generate  "+generateDescription)
	_, _ = fmt.Fprintln(c.output, "  check     Check generated output without writing")
	if c.program == "rasql" {
		_, _ = fmt.Fprintln(c.output, "  schema    Update, import, or verify the declared schema")
	}
	_, _ = fmt.Fprintln(c.output)
	_, _ = fmt.Fprintln(c.output, "Settings live in rasql.json at the module root: the package name, the output")
	_, _ = fmt.Fprintln(c.output, "directory, the dialect, the table selection, row-type names, and static queries.")
	_, _ = fmt.Fprintln(c.output, "A flag overrides what that file says. The DSN is never read from it.")
	_, _ = fmt.Fprintln(c.output)
	_, _ = fmt.Fprintf(c.output, "Run '%s <command> -h' for command flags.\n", c.program)
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
		return unexpectedArgumentsError(len(rest))
	}
	return err
}

// unexpectedArgumentsError reports only how many leftover arguments a command
// did not consume and never echoes their values.
func unexpectedArgumentsError(count int) error {
	if count == 1 {
		return errors.New("unexpected positional argument; generate accepts flags only")
	}
	return fmt.Errorf("unexpected %d positional arguments; generate accepts flags only", count)
}

func (c command) newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(c.diagnostics)
	return flags
}
