// rasqlmigrate applies versioned SQL migration directories.
package main

import (
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/lestrrat-go/rasql/cli/rasqlmigrate"
)

func main() {
	if err := rasqlmigrate.RunLegacy(os.Args[1:], os.Stdout); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
