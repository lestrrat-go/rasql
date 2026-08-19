package rasqlgen

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"go/token"
	"strings"
	"time"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/dsnredact"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"
)

// dialectSpec holds what one -dialect value selects: the sql.Open driver
// name the command opens the database with, and the dialect value it hands
// to catalog and generate.
type dialectSpec struct {
	openName string
	dialect  dialect.Dialect
}

// supportedDialects maps every accepted -dialect spelling to its spec.
//
// A dialect.Dialect is an immutable value, so holding one here rather than
// calling for it per run is safe to share across concurrent runs.
var supportedDialects = map[string]dialectSpec{
	"postgres":   {openName: "pgx", dialect: dialect.PostgreSQL()},
	"postgresql": {openName: "pgx", dialect: dialect.PostgreSQL()},
	"mysql":      {openName: "mysql", dialect: dialect.MySQL()},
	"sqlite":     {openName: "sqlite", dialect: dialect.SQLite()},
}

// defaultInspectionTimeout bounds the whole run: opening the database,
// reading its metadata, and writing the package. It matches the timeout the
// settings file leaves alone, and -timeout replaces it.
const defaultInspectionTimeout = 30 * time.Second

// runGenerate implements the "generate" command: it reads a live database
// and writes the store package, with no program in between.
//
// What stays the same from run to run comes from the settings file, and what
// changes per run comes from a flag, with a flag the user typed overriding
// the file. Two settings are deliberately flag-only: the DSN, because the
// file is checked in and a connection string carries a credential, and
// -check, because it selects what one run does rather than what the project
// is.
//
// This command opens a database, so this file is where the three drivers
// enter the binary. They reach no library package: catalog takes an
// already-open handle exactly so that a user's own program adds no driver to
// its module graph, and that promise is about the library, not about a
// command the user runs.
func (c command) runGenerate(args []string) error {
	flags := c.newFlagSet(c.flagSetPrefix + "generate")
	configPath := flags.String("config", "", "settings file to read (default: rasql.json at the module root, when it exists)")
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
	// Which flags the user actually typed decides what the settings file may
	// still supply, so a project keeps its settings in one place and a
	// one-off run overrides any of them without editing that file.
	typed := typedFlags(flags)

	settings, err := loadConfig(*configPath)
	if err != nil {
		return err
	}
	if !typed.has("dialect") && settings.Dialect != "" {
		*dialectName = settings.Dialect
	}
	if !typed.has("package") && settings.Package != "" {
		*packageName = settings.Package
	}
	if !typed.has("output") && settings.Output != "" {
		*output = settings.Output
	}
	if !typed.has("root") && settings.Root != "" {
		*root = settings.Root
	}
	if !typed.has("history-table") && settings.Tables.HistoryTable != "" {
		*historyTable = settings.Tables.HistoryTable
	}
	if !typed.has("prune") && settings.Prune != nil {
		*prune = *settings.Prune
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
	if !typed.has("include") && len(settings.Tables.Include) > 0 {
		includeTables = settings.Tables.Include
	}
	if !typed.has("exclude") && len(settings.Tables.Exclude) > 0 {
		excludeTables = settings.Tables.Exclude
	}
	hints, err := settings.hints()
	if err != nil {
		return err
	}
	queries, err := settings.queries()
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
		Hints:   hints,
		Dialect: spec.dialect,
		Queries: queries,
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

// typedFlagSet holds the flags one run named on the command line.
type typedFlagSet map[string]struct{}

func (t typedFlagSet) has(name string) bool {
	_, typed := t[name]
	return typed
}

// typedFlags reports which flags this run actually named, so a settings
// file fills in only what the command line left alone. flag.Visit walks
// exactly the flags that were set, which is the one way to tell a "-prune
// false" the user typed from a -prune nobody typed.
func typedFlags(flags *flag.FlagSet) typedFlagSet {
	typed := make(typedFlagSet)
	flags.Visit(func(f *flag.Flag) { typed[f.Name] = struct{}{} })
	return typed
}

func pluralTables(count int) string {
	if count == 1 {
		return "table"
	}
	return "tables"
}
