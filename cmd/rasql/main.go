// rasql provides the unified code-generation and migration command.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/lestrrat-go/rasql/cli/rasql"
	"github.com/lestrrat-go/rasql/cli/rasqlgen"
)

func main() {
	if err := rasql.Run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		code := rasqlgen.ExitCode(err)
		if code == 0 {
			code = 2
		}
		os.Exit(code)
	}
}
