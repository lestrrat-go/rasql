package rasqlgen

import (
	"bytes"
	"errors"
	"fmt"
	"go/format"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"
)

// initDialect names the driver import path, the sql.Open driver name, and
// the dialect.*() call written into the scaffold for one -dialect value.
type initDialect struct {
	driverImport string
	openName     string
	dialectCall  string
}

// initDialects is the same mapping inspectionDriver uses for -dsn
// inspection, restated here so init writes the matching import, sql.Open
// name and dialect.*() call into the scaffold at scaffold time.
var initDialects = map[string]initDialect{
	"postgres":   {driverImport: "github.com/jackc/pgx/v5/stdlib", openName: "pgx", dialectCall: "dialect.PostgreSQL()"},
	"postgresql": {driverImport: "github.com/jackc/pgx/v5/stdlib", openName: "pgx", dialectCall: "dialect.PostgreSQL()"},
	"mysql":      {driverImport: "github.com/go-sql-driver/mysql", openName: "mysql", dialectCall: "dialect.MySQL()"},
	"sqlite":     {driverImport: "modernc.org/sqlite", openName: "sqlite", dialectCall: "dialect.SQLite()"},
}

// initScaffoldTemplate renders gen/main.go. It is parsed once at package
// initialization; a template.Parse failure here is a programming error, not
// a runtime condition, so it panics like the standard library's own
// template.Must would.
var initScaffoldTemplate = template.Must(template.New("gen/main.go").Parse(`// Command gen regenerates the typed store in {{.Output}}.
//
// Run it with ` + "`go generate ./...`" + `. Pass -check to report whether the
// checked-in store is current without writing anything.
//
// rasqlgen init wrote this file once. Run again and it refuses to touch
// this file unless you pass -force, which rewrites it from the flags of
// that run and discards every edit made here. It is the manifest: the
// package name, the output directory, the dialect, the Go-side hints and
// the query list all live here, where the compiler checks them and
// ` + "`git diff`" + ` shows them.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/schema"

	_ "{{.DriverImport}}"
)

//go:generate go run .

// hints carry the Go-side facts no database can state, keyed by table
// name. A key naming no table in the schema is an error, not a silent
// no-op.
var hints = map[string]schema.TableHint{
	// "users": {RowName: "User"},
}

func main() {
	check := flag.Bool("check", false, "report whether the generated store is current instead of writing it")
	flag.Parse()
	if err := run(context.Background(), *check); err != nil {
		fmt.Fprintln(os.Stderr, "gen:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, check bool) error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("set DATABASE_URL to the database this store is generated from")
	}
	database, err := sql.Open("{{.OpenName}}", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = database.Close() }()

	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{
		Dialect: {{.DialectCall}},
	})
	if err != nil {
		return err
	}

	store := generate.Store{
		Package: "{{.Package}}",
		Dir:     {{.OutputLiteral}},
		Tables:  tables,
		Hints:   hints,
		Dialect: {{.DialectCall}},
		Queries: []generate.Query{
			// {Input: "queries/user_by_email.sql", Function: "UserByEmail", Output: "user_by_email_gen.go"},
		},
		// Prune lets a run delete a file rasqlgen wrote that this run no
		// longer writes -- the per-table file of a dropped table. Set it
		// to false to have the run refuse and name the file instead.
		Prune: true,
	}
	if check {
		return store.Check()
	}
	return store.Write()
}
`))

// runInit implements the "init" command: it scaffolds gen/main.go and does
// nothing else. It opens no database, runs no subprocess, and creates no
// output directory -- see cli/rasqlgen/init.go's package-level doc, and
// §3.1 of the phase 5 specification, for why.
func runInit(args []string, writer io.Writer) error {
	flags := newFlagSet("init", writer)
	dialectName := flags.String("dialect", "", "postgresql (or postgres), mysql, or sqlite")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "the generated package's directory, module-root-relative")
	genDir := flags.String("gen-dir", "gen", "directory the scaffold is written into, relative to the working directory")
	force := flags.Bool("force", false, "overwrite an existing -gen-dir/main.go")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}

	spec, ok := initDialects[*dialectName]
	if !ok {
		return fmt.Errorf("init: unsupported -dialect %q; want postgresql, postgres, mysql, or sqlite", *dialectName)
	}
	if *packageName == "_" || !token.IsIdentifier(*packageName) {
		return fmt.Errorf("init: -package %q must be a Go identifier", *packageName)
	}
	if *output == "" {
		return errors.New("init: -output is required")
	}
	if filepath.IsAbs(*output) {
		return fmt.Errorf("init: -output %q must not be absolute", *output)
	}
	// The scaffold's doc comment names -output raw, on one comment line,
	// where Store.Dir gets the quoted form. The policy here is broader than
	// what that line strictly needs: every ASCII control character is
	// refused, not only the ones that would break the generated source. Of
	// the rejected set only a newline actually ends the comment line, and
	// only a NUL fails the compiler ("unexpected NUL in input"); a tab, a
	// carriage return and a DEL all compile, and format.Source rewrites the
	// carriage return rather than refusing it. They are refused anyway,
	// because a directory named with unprintable text is far more likely a
	// mistake than an intention, and this keeps such text out of both the
	// comment and the source. A backtick is deliberately not rejected: it
	// is legal in a Go interpreted string literal and legal in a directory
	// name, so refusing it would refuse a valid path for nothing.
	if index := strings.IndexFunc(*output, func(r rune) bool { return r < 0x20 || r == 0x7f }); index >= 0 {
		return fmt.Errorf("init: -output %q must not contain the control character %q", *output, rune((*output)[index]))
	}
	if *genDir == "" {
		return errors.New("init: -gen-dir must not be empty")
	}
	if err := requireDistinctInitDirs(*output, *genDir); err != nil {
		return err
	}

	path := filepath.Join(*genDir, "main.go")
	overwriting := false
	if info, err := os.Stat(path); err == nil {
		if info.IsDir() {
			return fmt.Errorf("init: %s is a directory", path)
		}
		if !*force {
			return fmt.Errorf("rasqlgen init: %s already exists; edit it, or pass -force to overwrite it", path)
		}
		overwriting = true
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("init: stat %s: %w", path, err)
	}

	source, err := renderInitScaffold(spec, *packageName, *output)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(*genDir, 0o755); err != nil {
		return fmt.Errorf("init: create %s: %w", *genDir, err)
	}
	if err := os.WriteFile(path, source, 0o644); err != nil {
		return fmt.Errorf("init: write %s: %w", path, err)
	}

	if overwriting {
		_, _ = fmt.Fprintf(writer, "rasqlgen init: overwrote %s\n", path)
	} else {
		_, _ = fmt.Fprintf(writer, "wrote %s\n", path)
	}
	_, _ = fmt.Fprintln(writer)
	_, _ = fmt.Fprintln(writer, "Next:")
	_, _ = fmt.Fprintf(writer, "  go get %s\n", spec.driverImport)
	_, _ = fmt.Fprintln(writer, "  DATABASE_URL=... go generate ./...")
	return nil
}

// requireDistinctInitDirs refuses an -output that names the same directory
// as -gen-dir. The scaffold is package main and the store it generates is
// -package, so one directory holding both is a directory holding two
// packages: the first `go generate ./...` writes the store files happily,
// and every build after that fails with "found packages main (main.go) and
// <package> (schema_gen.go)". Nothing in the scaffold can run again, so
// this has to fail before the first write rather than be recovered from.
//
// Comparing the two as typed is not enough, because -output "./gen",
// "gen/" and "gen/../gen" all name the -gen-dir "gen" without matching it
// as a string, and -gen-dir has the same freedom. Both are therefore
// resolved against the working directory first, which also settles the
// case of an absolute -gen-dir against the relative -output that is all
// this command accepts.
//
// Only equality is refused. An -output nested under -gen-dir, such as
// "gen/store", is a separate directory and so a separate package, which is
// a layout this command has no reason to refuse.
func requireDistinctInitDirs(output, genDir string) error {
	outputPath, err := filepath.Abs(output)
	if err != nil {
		return fmt.Errorf("init: resolve -output %q: %w", output, err)
	}
	genPath, err := filepath.Abs(genDir)
	if err != nil {
		return fmt.Errorf("init: resolve -gen-dir %q: %w", genDir, err)
	}
	if outputPath == genPath {
		return fmt.Errorf("init: -output %q and -gen-dir %q name the same directory %s; the scaffold is package main and the generated store is another package, so they cannot share one", output, genDir, outputPath)
	}
	return nil
}

// renderInitScaffold executes initScaffoldTemplate for one -dialect,
// -package and -output combination and formats the result with gofmt, so
// the file init writes is gofmt-clean by construction rather than by
// convention.
func renderInitScaffold(spec initDialect, packageName, output string) ([]byte, error) {
	slashed := filepath.ToSlash(output)
	data := struct {
		DriverImport  string
		OpenName      string
		DialectCall   string
		Package       string
		Output        string
		OutputLiteral string
	}{
		DriverImport: spec.driverImport,
		OpenName:     spec.openName,
		DialectCall:  spec.dialectCall,
		Package:      packageName,
		Output:       slashed,
		// OutputLiteral, not Output, is what the template splices into
		// Store.Dir. The template writes it where a Go string literal
		// belongs, so it carries its own quotes: a -output holding a
		// double quote or a backslash would otherwise close or re-read
		// that literal, and format.Source cannot catch it because the
		// result is still syntactically valid Go. Quoting here makes the
		// directory the user typed the directory the scaffold writes to,
		// whatever it contains.
		OutputLiteral: strconv.Quote(slashed),
	}

	var buf bytes.Buffer
	if err := initScaffoldTemplate.Execute(&buf, data); err != nil {
		return nil, fmt.Errorf("init: render scaffold: %w", err)
	}

	formatted, err := format.Source(buf.Bytes())
	if err != nil {
		return nil, fmt.Errorf("init: scaffold does not gofmt cleanly: %w", err)
	}
	return formatted, nil
}
