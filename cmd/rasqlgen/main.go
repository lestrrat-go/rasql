// rasqlgen scaffolds the project-owned generator through the init command.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/lestrrat-go/rasql/cli/rasqlgen"
)

func main() {
	err := rasqlgen.Run(os.Args[1:], os.Stderr)
	if errors.Is(err, flag.ErrHelp) {
		return
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
