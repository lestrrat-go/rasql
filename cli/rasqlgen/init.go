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

	"github.com/lestrrat-go/rasql/internal/modroot"
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
// as a string, and -gen-dir has the same freedom. Each is therefore
// resolved to an absolute path first -- which also settles the case of an
// absolute -gen-dir against the relative -output that is all this command
// accepts -- and then canonicalized, so that two names the filesystem
// already leads to one directory compare equal too.
//
// The two are resolved against different bases, because the two flags are
// documented against different bases: -gen-dir names a directory relative
// to the working directory, and -output is module-root-relative, since the
// scaffold hands it to generate.Store.Dir with Store.Root left empty. See
// initOutputDirectory for what comparing them on one base let through.
//
// Only equality is refused. An -output nested under -gen-dir, such as
// "gen/store", is a separate directory and so a separate package, which is
// a layout this command has no reason to refuse.
//
// A run that gets past this check is still not proof the two directories
// stay distinct: a symbolic link made after init, and any hand-written
// generate.Store, reach the same collision with nothing here to consult.
// generate.Store.Plan refuses the write itself for that reason, and is
// what actually stands between the collision and a broken package; this
// check is what turns it into a mistake reported at the moment the flags
// are typed.
func requireDistinctInitDirs(output, genDir string) error {
	outputPath, err := initOutputDirectory(output)
	if err != nil {
		return fmt.Errorf("init: resolve -output %q: %w", output, err)
	}
	genPath, err := filepath.Abs(genDir)
	if err != nil {
		return fmt.Errorf("init: resolve -gen-dir %q: %w", genDir, err)
	}
	canonicalOutput := canonicalDirectory(outputPath)
	if canonicalOutput != canonicalDirectory(genPath) {
		return nil
	}
	return fmt.Errorf("init: -output %q and -gen-dir %q name the same directory %s; the scaffold is package main and the generated store is another package, so they cannot share one", output, genDir, canonicalOutput)
}

// initOutputDirectory resolves -output the way the scaffold's own
// generate.Store resolves Store.Dir: against the module root, because the
// scaffold leaves Store.Root empty and a relative Dir is then
// module-root-relative. internal/modroot is the same walk generate itself
// makes, so the directory compared here is the directory the scaffold
// writes into.
//
// Resolving -output against the working directory instead, as this once
// did, compares two different bases whenever init runs below the module
// root, and no symbolic link is needed to see it: from a subdirectory sub/,
// "-output sub/gen -gen-dir gen" compared <root>/sub/sub/gen against
// <root>/sub/gen and passed, while the scaffold then generated the store
// into <root>/sub/gen, beside the main.go init had just written there.
//
// A working directory with no go.mod above it has no module root to
// resolve against. The scaffold's own Store.Plan refuses such a run
// outright, so nothing is lost by falling back to the working directory
// here, and the comparison still happens rather than being skipped.
func initOutputDirectory(output string) (string, error) {
	root, err := modroot.FromWorkingDirectory()
	if err != nil {
		return "", err
	}
	if root == "" {
		return filepath.Abs(output)
	}
	return filepath.Abs(filepath.Join(root, output))
}

// canonicalDirectory resolves path through every symbolic link along it,
// whether or not that link's target exists yet, and rejoins the part of the
// path the filesystem does not hold.
//
// filepath.Abs cannot do this on its own: it is purely lexical -- Clean
// plus a join with the working directory -- so "alias" and "gen" stay two
// strings even when "alias" is a symbolic link to "gen", and so does a
// parent directory of either that is a link. filepath.EvalSymlinks does
// resolve them, but reports ErrNotExist for a path whose last element is
// missing, and -output and -gen-dir routinely name directories init is
// about to create. Resolving the longest existing prefix and appending the
// remainder to it is what serves both: it canonicalizes as much as the
// filesystem can answer for, and leaves the rest alone.
//
// A dangling symbolic link -- one whose target does not exist yet -- needs
// following by hand. EvalSymlinks reports ErrNotExist for it exactly as it
// does for a name that is not there at all, so the prefix walk alone would
// drop to the existing parent and rejoin the link's own name, leaving the
// link unresolved: "target -> gen" with gen still missing canonicalized to
// <root>/target and compared unequal to the <root>/gen that -gen-dir names,
// even though os.MkdirAll then creates gen and makes the two one directory.
// Reading the link and resolving its target against the directory holding
// the link is what closes that, and it is the same resolution the kernel
// performs once the target exists.
//
// A path whose every prefix fails to resolve is returned unchanged. That is
// the lexical comparison this had before, so a path that cannot be
// canonicalized is no worse off than it was. Links read by hand are counted
// against maxSymlinkHops and hitting that bound returns the path unchanged
// too, because a link may point at another link and two of them may point at
// each other, and neither the EvalSymlinks call nor the prefix walk ends
// such a chain.
func canonicalDirectory(path string) string {
	current := path
	remainder := ""
	hops := 0
	for {
		resolved, err := filepath.EvalSymlinks(current)
		if err == nil {
			return filepath.Join(resolved, remainder)
		}
		if target, ok := symlinkTarget(current); ok {
			hops++
			if hops > maxSymlinkHops {
				return path
			}
			current = target
			continue
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
	}
}

// maxSymlinkHops bounds how many symbolic links canonicalDirectory reads by
// hand before it gives up and returns the path it was given. Linux caps its
// own path resolution at 40 links for the same reason, and this walk needs a
// bound of its own because it is reached precisely when EvalSymlinks -- which
// carries that bound -- has already refused to answer.
const maxSymlinkHops = 40

// symlinkTarget reports where path points, resolved against the directory
// holding the link, when path is itself a symbolic link. A relative target
// is joined onto that directory because that is what it is relative to; an
// absolute one is taken as it stands. Anything that is not a symbolic link,
// and any link that cannot be read, reports false and leaves the caller on
// its ordinary walk.
func symlinkTarget(path string) (string, bool) {
	info, err := os.Lstat(path)
	if err != nil || info.Mode()&os.ModeSymlink == 0 {
		return "", false
	}
	target, err := os.Readlink(path)
	if err != nil {
		return "", false
	}
	if filepath.IsAbs(target) {
		return filepath.Clean(target), true
	}
	return filepath.Join(filepath.Dir(path), target), true
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
