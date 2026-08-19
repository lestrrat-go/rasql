// Package rasqlgen implements the rasqlgen command.
package rasqlgen

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
)

// Run executes the codegen commands of the unified rasql command with args.
// Command output -- help text and what a successful command produced -- goes
// to output, and what the flag package prints while parsing goes to
// diagnostics, so the unified command can keep the two on separate streams.
func Run(args []string, output, diagnostics io.Writer) error {
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
}

func (c command) run(args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s <generate|init> [flags]", c.program)
	}
	switch args[0] {
	case "-h", "-help", "--help":
		c.printUsage()
		return flag.ErrHelp
	case "generate":
		return c.runGenerate(args[1:])
	case "init":
		return c.runInit(args[1:])
	default:
		return fmt.Errorf("unknown %s command %q; expected generate or init", c.program, args[0])
	}
}

func (c command) printUsage() {
	_, _ = fmt.Fprintf(c.output, "Usage: %s <command> [flags]\n", c.program)
	_, _ = fmt.Fprintln(c.output)
	_, _ = fmt.Fprintln(c.output, "Commands:")
	_, _ = fmt.Fprintln(c.output, "  generate  Generate the store package from a live database")
	_, _ = fmt.Fprintln(c.output, "  init      Scaffold the generator program, gen/main.go")
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

func (c command) newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(c.diagnostics)
	return flags
}
