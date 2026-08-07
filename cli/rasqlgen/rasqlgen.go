// Package rasqlgen implements the rasqlgen command.
package rasqlgen

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/schema"
	querytemplate "github.com/lestrrat-go/rasql/template"
)

var (
	openDatabase = sql.Open
	// maxInputBytes caps how much of a -input file rasqlgen reads into
	// memory. It is a var, not a const, so tests can lower it.
	maxInputBytes = 64 << 20
)

// readInputFile reads path through a size-limited reader, rejecting the
// input once it exceeds maxInputBytes. A Stat-based size check is not
// enough here: a fifo or character device reports size 0 regardless of how
// much data it actually produces, so only reading through a limit catches
// an oversized input reliably.
func readInputFile(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = f.Close() }()

	data, err := io.ReadAll(io.LimitReader(f, int64(maxInputBytes)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > maxInputBytes {
		return nil, fmt.Errorf("input file %s exceeds maximum size of %d bytes", path, maxInputBytes)
	}
	return data, nil
}

// Run executes rasqlgen with args and writes command output to writer.
func Run(args []string, writer io.Writer) error {
	if writer == nil {
		return errors.New("rasqlgen: command output must not be nil")
	}
	if len(args) == 0 {
		return errors.New("usage: rasqlgen <schema|query> [flags]")
	}
	switch args[0] {
	case "-h", "-help", "--help":
		printUsage(writer)
		return flag.ErrHelp
	case "schema":
		return runSchema(args[1:], writer)
	case "query":
		return runQuery(args[1:], writer)
	default:
		return fmt.Errorf("unknown rasqlgen command %q", args[0])
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: rasqlgen <command> [flags]")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Commands:")
	fmt.Fprintln(output, "  schema    Generate Go source from a schema")
	fmt.Fprintln(output, "  query     Generate Go source from a SQL template")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Run 'rasqlgen <command> -h' for command flags.")
}

func runSchema(args []string, writer io.Writer) error {
	flags := newFlagSet("schema", writer)
	input := flags.String("input", "", "path to a JSON array of schema tables (max 64 MiB)")
	dsn := flags.String("dsn", "", "PostgreSQL connection string")
	dialectName := flags.String("dialect", "postgresql", "database dialect for -dsn")
	timeout := flags.Duration("timeout", 30*time.Second, "deadline for -dsn metadata inspection")
	var tableNames tableNames
	flags.Var(&tableNames, "table", "database table to generate; repeat for multiple tables (duplicate values are rejected)")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "path for generated Go source ending in _gen.go")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	if *timeout <= 0 {
		return fmt.Errorf("schema -timeout must be positive, got %s", *timeout)
	}
	if *packageName == "" || *output == "" {
		return errors.New("schema requires -package and -output")
	}
	if *input != "" && *dsn != "" {
		return errors.New("schema accepts either -input or -dsn, not both")
	}
	var tables []schema.Table
	switch {
	case *input != "":
		data, err := readInputFile(*input)
		if err != nil {
			return fmt.Errorf("read schema input: %w", err)
		}
		if err := json.Unmarshal(data, &tables); err != nil {
			return fmt.Errorf("decode schema input: %w", err)
		}
		tables, err = filterTables(tables, tableNames)
		if err != nil {
			return err
		}
	case *dsn != "":
		if len(tableNames) == 0 {
			return errors.New("schema with -dsn requires at least one -table")
		}
		d, err := builtinDialect(*dialectName)
		if err != nil {
			return err
		}
		if d.Name() != dialect.PostgreSQL().Name() {
			return fmt.Errorf("schema direct inspection supports PostgreSQL, not %q", d.Name())
		}
		database, err := openDatabase("pgx", *dsn)
		if err != nil {
			return fmt.Errorf("open PostgreSQL database: %w", err)
		}
		defer database.Close()
		ctx, cancel := context.WithTimeout(context.Background(), *timeout)
		defer cancel()
		tx, err := database.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
		if err != nil {
			return fmt.Errorf("begin PostgreSQL inspection transaction: %w", err)
		}
		inspector, err := inspect.New(tx, d)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		tables, err = inspectTables(ctx, inspector, tableNames)
		if err != nil {
			_ = tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit PostgreSQL inspection transaction: %w", err)
		}
	default:
		return errors.New("schema requires either -input or -dsn")
	}
	source, err := generate.Schema(*packageName, tables...)
	if err != nil {
		return err
	}
	if err := writeGeneratedFile(*output, source); err != nil {
		return fmt.Errorf("write schema output: %w", err)
	}
	return nil
}

func inspectTables(ctx context.Context, inspector inspect.Inspector, names []string) ([]schema.Table, error) {
	tables := make([]schema.Table, len(names))
	for index, name := range names {
		table, err := inspector.Table(ctx, name)
		if err != nil {
			return nil, err
		}
		tables[index] = table
	}
	return tables, nil
}

func filterTables(tables []schema.Table, names []string) ([]schema.Table, error) {
	if len(names) == 0 {
		return tables, nil
	}
	requested := make(map[string]struct{}, len(names))
	for _, name := range names {
		requested[name] = struct{}{}
	}
	filtered := make([]schema.Table, 0, len(tables))
	found := make(map[string]struct{}, len(names))
	for _, table := range tables {
		if _, ok := requested[table.Name]; !ok {
			continue
		}
		filtered = append(filtered, table)
		found[table.Name] = struct{}{}
	}
	for _, name := range names {
		if _, ok := found[name]; !ok {
			return nil, fmt.Errorf("schema input has no table %q", name)
		}
	}
	return filtered, nil
}

type tableNames []string

func (names *tableNames) String() string {
	return strings.Join(*names, ",")
}

func (names *tableNames) Set(name string) error {
	for _, existing := range *names {
		if existing == name {
			return fmt.Errorf("duplicate -table %q", name)
		}
	}
	*names = append(*names, name)
	return nil
}

func runQuery(args []string, writer io.Writer) error {
	flags := newFlagSet("query", writer)
	input := flags.String("input", "", "path to a static SQL template (max 64 MiB)")
	functionName := flags.String("function", "", "generated function name")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "path for generated Go source ending in _gen.go")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}
	if *input == "" || *functionName == "" || *dialectName == "" || *packageName == "" || *output == "" {
		return errors.New("query requires -input, -function, -dialect, -package, and -output")
	}
	data, err := readInputFile(*input)
	if err != nil {
		return fmt.Errorf("read query input: %w", err)
	}
	d, err := builtinDialect(*dialectName)
	if err != nil {
		return err
	}
	parsed, err := querytemplate.Parse(*functionName, string(data))
	if err != nil {
		return err
	}
	compiled, err := parsed.Compile(d)
	if err != nil {
		return err
	}
	source, err := compiled.GoSource(*packageName, *functionName)
	if err != nil {
		return err
	}
	if err := writeGeneratedFile(*output, source); err != nil {
		return fmt.Errorf("write query output: %w", err)
	}
	return nil
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

// writeGeneratedFile writes source to path without ever truncating an
// existing file in place. Its resolved destination must end in _gen.go.
// When path is a symbolic link it writes through
// the link to the file the link points at, leaving the link itself intact.
// A path that names a directory, or a symbolic link that resolves to one,
// is rejected with an error instead of being written into, regardless of
// whether path ends in a trailing slash.
// It writes to a temporary file in the same directory as that resolved
// destination, then renames the temporary file over the destination only
// after the write fully succeeds, so a failure at any point along the way
// leaves the destination untouched instead of empty or partially written.
// When the destination already exists, the mode bits generatedFileMode
// reports for it are copied to the temporary file before the rename, so
// regenerating never changes the mode of an existing output file; a file
// that did not exist is created at 0600.
//
// The rename is only atomic on Unix platforms. os.Rename documents that
// even within a single directory it is not an atomic operation on
// non-Unix platforms, so a run interrupted there can leave the destination
// missing or still holding its old contents.
func writeGeneratedFile(path string, source []byte) error {
	// Both the temporary file's directory and the rename destination come
	// from the resolved path. Resolving only the destination would put the
	// temporary file beside the link instead of beside its target, and
	// renaming across filesystems fails with EXDEV.
	destination, err := resolveOutputPath(path)
	if err != nil {
		return err
	}
	if !strings.HasSuffix(destination, "_gen.go") {
		return fmt.Errorf("generated output %q must end in _gen.go", destination)
	}
	mode, err := generatedFileMode(destination)
	if err != nil {
		return err
	}
	directory := filepath.Dir(destination)
	temporary, err := os.CreateTemp(directory, "."+filepath.Base(destination)+".tmp*")
	if err != nil {
		return err
	}
	removeTemporary := true
	defer func() {
		if removeTemporary {
			_ = os.Remove(temporary.Name())
		}
	}()

	if _, err := temporary.Write(source); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	// chmod before the rename, so the destination is never visible with
	// the temporary file's mode instead of the mode it had before.
	if err := os.Chmod(temporary.Name(), mode); err != nil {
		return err
	}
	if err := os.Rename(temporary.Name(), destination); err != nil {
		return err
	}
	removeTemporary = false
	return nil
}

// maxOutputSymlinkDepth caps how many symbolic links resolveOutputPath
// follows, so a link that points at itself or at another link in a cycle
// reports an error instead of looping forever. Linux allows 40 links in a
// single resolution, so this matches what an ordinary open would accept:
// the destination the fortieth link names is still used, and only a
// forty-first link that must be resolved is too many.
const maxOutputSymlinkDepth = 40

// resolveOutputPath reports the file writeGeneratedFile must replace for
// the requested output path. A path that is not a symbolic link is its own
// destination. A symbolic link resolves to whatever it points at, so the
// generated source lands in the file the link names and the link itself
// survives the rename; writing to the link's own path would instead delete
// the link and leave its target holding stale content.
//
// A path that names a directory, or a symbolic link that resolves to one,
// is rejected instead of being treated as a destination. os.Lstat follows
// a trailing slash through to the directory even when the final component
// is a link, so this one check catches a directory named with or without a
// trailing slash and a directory reached through a link either way; without
// it, withResolvedParent would rewrite a path such as "outdir/" into a new
// child file "outdir/outdir" inside that directory and report success on
// what was almost certainly a typo.
//
// A link that points at a path which does not exist resolves to that
// missing path, matching what writing through the link would have done:
// the missing file is created, with the 0600 that generatedFileMode gives
// any new file, and the link keeps pointing at it. filepath.EvalSymlinks
// cannot resolve the whole path here, because it fails on such a dangling
// link; it is used only on directories, which always exist by the time
// they are resolved.
func resolveOutputPath(path string) (string, error) {
	current := path
	// followed counts the links already resolved. Testing it before the
	// next Readlink rather than after the last one keeps the destination
	// the fortieth link names in play, so a chain the kernel would accept
	// is accepted here too.
	for followed := 0; ; followed++ {
		info, err := os.Lstat(current)
		if err != nil {
			if errors.Is(err, fs.ErrNotExist) {
				return withResolvedParent(current), nil
			}
			return "", err
		}
		if info.IsDir() {
			return "", fmt.Errorf("resolve output %s: is a directory", path)
		}
		if info.Mode()&fs.ModeSymlink == 0 {
			return withResolvedParent(current), nil
		}
		if followed == maxOutputSymlinkDepth {
			return "", fmt.Errorf("resolve output %s: too many levels of symbolic links", path)
		}
		target, err := os.Readlink(current)
		if err != nil {
			return "", err
		}
		if filepath.IsAbs(target) {
			current = target
			continue
		}
		// A relative link is relative to the directory holding the link,
		// and that is the directory the filesystem reaches rather than the
		// one filepath.Dir spells. Joining against the lexical parent
		// collapses a leading ".." in the target against a parent that is
		// itself a link, which lands the resolved path beside the link
		// instead of beside the directory the link points at. The Lstat
		// above succeeded, so that parent exists and resolves.
		parent, err := filepath.EvalSymlinks(filepath.Dir(current))
		if err != nil {
			return "", err
		}
		current = filepath.Join(parent, target)
	}
}

// withResolvedParent reports path with the directory holding it replaced
// by the directory the filesystem reaches, so writeGeneratedFile creates
// its temporary file in, and renames over, the directory an ordinary open
// of path would have used. A parent that cannot be resolved is left as it
// was spelled, which leaves the resulting open to report the ENOENT an
// ordinary open would have reported.
//
// path is never itself a directory here: resolveOutputPath rejects that
// case before calling this, so filepath.Base(path) always names the file
// the caller asked for rather than the directory holding it.
func withResolvedParent(path string) string {
	parent, err := filepath.EvalSymlinks(filepath.Dir(path))
	if err != nil {
		return path
	}
	return filepath.Join(parent, filepath.Base(path))
}

// generatedFileMode reports the mode bits writeGeneratedFile must give
// path. An existing file keeps the bits it already has, because the
// generated output's mode is the caller's to choose. Only a path that does
// not exist yet gets os.CreateTemp's 0600.
//
// The sticky bit travels with the permission bits, because writing the
// file in place kept it and replacing the file must not quietly drop it.
// Setuid and setgid deliberately do not travel: Linux clears both when an
// unprivileged process writes a file, so writing in place lost them too,
// and carrying them across here would make regenerating a file stricter
// than writing it directly.
func generatedFileMode(path string) (fs.FileMode, error) {
	info, err := os.Stat(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return 0o600, nil
		}
		return 0, err
	}
	return info.Mode() & (fs.ModePerm | fs.ModeSticky), nil
}

func newFlagSet(name string, writer io.Writer) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(writer)
	return flags
}

func builtinDialect(name string) (dialect.Dialect, error) {
	switch name {
	case "postgres", "postgresql":
		return dialect.PostgreSQL(), nil
	case "mysql":
		return dialect.MySQL(), nil
	case "sqlite":
		return dialect.SQLite(), nil
	default:
		return nil, fmt.Errorf("unsupported dialect %q", name)
	}
}
