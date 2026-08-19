package rasqlgen

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"go/token"
	"strings"
	"time"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/dsnredact"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// defaultInspectionTimeout bounds the whole run: opening the database,
// reading its metadata, and writing the package. It matches the timeout the
// init scaffold starts with, and -timeout replaces it.
const defaultInspectionTimeout = 30 * time.Second

// runGenerate implements the "generate" command: it reads a live database
// and writes the store package, with no program in between.
//
// This is the command for a project whose generation is fully described by
// its flags, which is the common case: a package name, an output directory,
// a dialect, and a table selection. A project that must state something no
// flag can carry -- a schema.TableHint, or a generate.Query compiling a
// static SQL template into the package -- runs "init" instead and owns the
// program those values live in. Both routes call the same catalog and
// generate packages and produce byte-identical output from the same
// database, so a project that outgrows this command can scaffold the
// program later without regenerating differently.
//
// Unlike init, this command opens a database, so this file is where the
// three drivers enter the binary. They reach no library package: catalog
// takes an already-open handle exactly so that a user's own generator adds
// no driver to its module graph, and that promise is about the library, not
// about a command the user runs.
func (c command) runGenerate(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "generate")
	dsn := flags.String("dsn", "", "connection string for the database to generate from")
	dialectName := flags.String("dialect", "", "postgresql (or postgres), mysql, or sqlite")
	packageName := flags.String("package", "", "generated package name")
	output := flags.String("output", "", "the generated package's directory, module-root-relative")
	root := flags.String("root", "", "directory -output resolves against (default: the module root above the working directory)")
	include := flags.String("include", "", "comma-separated tables to generate, instead of every base table")
	exclude := flags.String("exclude", "", "comma-separated tables to skip; not accepted with -include")
	historyTable := flags.String("history-table", "", "migration history table to skip (default: rasql_schema_migrations)")
	prune := flags.Bool("prune", true, "delete a generated file this run no longer writes, instead of refusing the run")
	check := flags.Bool("check", false, "report whether the generated package is current instead of writing it")
	timeout := flags.Duration("timeout", defaultInspectionTimeout, "limit on the whole run")
	if err := parseCommandFlags(flags, args); err != nil {
		return err
	}

	spec, ok := supportedDialects[*dialectName]
	if !ok {
		return fmt.Errorf("generate: unsupported -dialect %q; want postgresql, postgres, mysql, or sqlite", *dialectName)
	}
	if *dsn == "" {
		return errors.New("generate: -dsn is required")
	}
	if *packageName == "_" || !token.IsIdentifier(*packageName) {
		return fmt.Errorf("generate: -package %q must be a Go identifier", *packageName)
	}
	if *output == "" {
		return errors.New("generate: -output is required")
	}
	if *timeout <= 0 {
		return fmt.Errorf("generate: -timeout %s must be positive", *timeout)
	}
	includeTables, err := splitTableNames("include", *include)
	if err != nil {
		return err
	}
	excludeTables, err := splitTableNames("exclude", *exclude)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	database, err := sql.Open(spec.openName, *dsn)
	if err != nil {
		return fmt.Errorf("generate: open database: %w", dsnredact.Error(err, *dsn))
	}
	defer func() { _ = database.Close() }()

	tables, err := catalog.FromDatabase(ctx, database, catalog.Options{
		Dialect:      spec.dialect,
		Include:      includeTables,
		Exclude:      excludeTables,
		HistoryTable: *historyTable,
	})
	if err != nil {
		return fmt.Errorf("generate: %w", dsnredact.Error(err, *dsn))
	}

	store := generate.Store{
		Package: *packageName,
		Dir:     *output,
		Root:    *root,
		Tables:  tables,
		Dialect: spec.dialect,
		Prune:   *prune,
	}
	if *check {
		if err := store.Check(); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(c.output, "%s is up to date\n", *output)
		return nil
	}
	if err := store.Write(); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(c.output, "wrote %s from %d %s\n", *output, len(tables), pluralTables(len(tables)))
	return nil
}

// splitTableNames parses one comma-separated table selection flag. An empty
// value selects nothing, which is what both catalog.Options selections treat
// as "unset"; an element that is empty or blank is refused rather than
// dropped, because "users,,orders" is a typo and silently generating from
// two tables would hide it.
func splitTableNames(flagName, value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, fmt.Errorf("generate: -%s %q holds an empty table name", flagName, value)
		}
		names = append(names, name)
	}
	return names, nil
}

func pluralTables(count int) string {
	if count == 1 {
		return "table"
	}
	return "tables"
}
