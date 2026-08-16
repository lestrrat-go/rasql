// Package rasqlgen implements the rasqlgen command.
package rasqlgen

import (
	"errors"
	"flag"
	"fmt"
	"io"
)

// Run executes rasqlgen with args and writes command output to writer.
func Run(args []string, writer io.Writer) error {
	if writer == nil {
		return errors.New("rasqlgen: command output must not be nil")
	}
	if len(args) == 0 {
		return errors.New("usage: rasqlgen <init> [flags]")
	}
	switch args[0] {
	case "-h", "-help", "--help":
		printUsage(writer)
		return flag.ErrHelp
	case "init":
		return runInit(args[1:], writer)
	default:
		return fmt.Errorf("unknown rasqlgen command %q; expected init", args[0])
	}
}

func printUsage(output io.Writer) {
	_, _ = fmt.Fprintln(output, "Usage: rasqlgen <command> [flags]")
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "Commands:")
	_, _ = fmt.Fprintln(output, "  init      Scaffold the generator program, gen/main.go")
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "Run 'rasqlgen <command> -h' for command flags.")
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
