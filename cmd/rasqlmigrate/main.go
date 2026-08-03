// rasqlmigrate applies versioned SQL migration directories.
package main

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/lestrrat-go/rasql/migrate/diff/postgresql"
	_ "modernc.org/sqlite"
)

var (
	openDatabase            = sql.Open
	commandOutput io.Writer = os.Stdout
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: rasqlmigrate <new|diff|plan|apply|status|verify> [flags]")
	}
	switch args[0] {
	case "-h", "-help", "--help":
		printUsage(commandOutput)
		return flag.ErrHelp
	case "new":
		return runNew(args[1:])
	case "diff":
		return runDiff(args[1:])
	case "plan":
		return runPlan(args[1:])
	case "apply":
		return runApply(args[1:])
	case "status":
		return runStatus(args[1:])
	case "verify":
		return runVerify(args[1:])
	default:
		return fmt.Errorf("unknown rasqlmigrate command %q", args[0])
	}
}

func printUsage(output io.Writer) {
	fmt.Fprintln(output, "Usage: rasqlmigrate <command> [flags]")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Commands:")
	fmt.Fprintln(output, "  new      Create a directory for one migration")
	fmt.Fprintln(output, "  diff     Generate a reviewed migration from desired schemas")
	fmt.Fprintln(output, "  plan     Print ordered SQL sources without connecting to a database")
	fmt.Fprintln(output, "  apply    Apply pending migrations")
	fmt.Fprintln(output, "  status   Show applied, pending, changed, and unknown migrations")
	fmt.Fprintln(output, "  verify   Require every supplied migration to be applied unchanged")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "A migration directory contains ordered .sql files. Each file contains one native SQL statement.")
	fmt.Fprintln(output, "Run 'rasqlmigrate <command> -h' for command flags.")
}

func runDiff(args []string) error {
	flags := newFlagSet("diff")
	dialectName := flags.String("dialect", "", "schema dialect; PostgreSQL and MySQL are currently supported")
	fromDirectory := flags.String("from", "", "baseline desired-schema directory")
	toDirectory := flags.String("to", "", "target desired-schema directory")
	outputDirectory := flags.String("output", "", "new migration directory; omit to preview")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dialectName == "" || *fromDirectory == "" || *toDirectory == "" {
		return errors.New("diff requires -dialect, -from, and -to")
	}
	analyzer, err := schemaAnalyzer(*dialectName)
	if err != nil {
		return err
	}
	baselineSources, err := diff.LoadSources(*fromDirectory)
	if err != nil {
		return err
	}
	baseline, err := analyzer.Parse(baselineSources)
	if err != nil {
		return fmt.Errorf("parse baseline desired schema: %w", err)
	}
	targetSources, err := diff.LoadSources(*toDirectory)
	if err != nil {
		return err
	}
	target, err := analyzer.Parse(targetSources)
	if err != nil {
		return fmt.Errorf("parse target desired schema: %w", err)
	}
	plan, err := analyzer.Diff(baseline, target)
	if err != nil {
		return err
	}
	if plan.Empty() {
		fmt.Fprintln(commandOutput, "no schema changes")
		return nil
	}
	if *outputDirectory == "" {
		writeDiffPlan(commandOutput, plan)
		return nil
	}
	if err := diff.WriteMigration(*outputDirectory, plan); err != nil {
		return err
	}
	fmt.Fprintf(commandOutput, "created %s\n", *outputDirectory)
	return nil
}

func schemaAnalyzer(name string) (diff.Analyzer, error) {
	switch name {
	case "postgres", "postgresql":
		return postgresql.New(), nil
	case "mysql":
		return mysql.New(), nil
	default:
		return nil, fmt.Errorf("unsupported schema diff dialect %q", name)
	}
}

func runNew(args []string) error {
	flags := newFlagSet("new")
	directory := flags.String("dir", "", "directory that holds migration directories")
	id := flags.String("id", "", "migration ID")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *directory == "" || *id == "" {
		return errors.New("new requires -dir and -id")
	}
	if strings.HasPrefix(*id, ".") || filepath.Base(*id) != *id {
		return fmt.Errorf("migration ID %q cannot become a directory name", *id)
	}
	output := filepath.Join(*directory, *id)
	if err := os.MkdirAll(*directory, 0o700); err != nil {
		return fmt.Errorf("create migration parent directory: %w", err)
	}
	if err := os.Mkdir(output, 0o700); err != nil {
		return fmt.Errorf("create migration directory %q: %w", output, err)
	}
	fmt.Fprintf(commandOutput, "created %s; add ordered .sql files\n", output)
	return nil
}

func runPlan(args []string) error {
	flags := newFlagSet("plan")
	directory := flags.String("dir", "", "directory that holds migration directories")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *directory == "" {
		return errors.New("plan requires -dir")
	}
	migrations, err := loadMigrations(*directory)
	if err != nil {
		return err
	}
	writePlan(commandOutput, migrations)
	return nil
}

func runApply(args []string) error {
	flags := newFlagSet("apply")
	directory := flags.String("dir", "", "directory that holds migration directories")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	dsn := flags.String("dsn", "", "database connection string")
	historyTable := flags.String("history-table", "", "migration history table name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	runner, migrations, closeDatabase, err := openRunner(context.Background(), *directory, *dialectName, *dsn, *historyTable)
	if err != nil {
		return err
	}
	defer closeDatabase()
	if err := runner.Apply(context.Background(), migrations...); err != nil {
		return redactError(err, *dsn)
	}
	fmt.Fprintln(commandOutput, "migration apply completed")
	return nil
}

func runStatus(args []string) error {
	flags := newFlagSet("status")
	directory := flags.String("dir", "", "directory that holds migration directories")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	dsn := flags.String("dsn", "", "database connection string")
	historyTable := flags.String("history-table", "", "migration history table name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	runner, migrations, closeDatabase, err := openRunner(context.Background(), *directory, *dialectName, *dsn, *historyTable)
	if err != nil {
		return err
	}
	defer closeDatabase()
	entries, err := runner.Status(context.Background(), migrations...)
	if err != nil {
		return redactError(err, *dsn)
	}
	for _, entry := range entries {
		fmt.Fprintf(commandOutput, "%s\t%s\n", entry.State, entry.ID)
	}
	return nil
}

func runVerify(args []string) error {
	flags := newFlagSet("verify")
	directory := flags.String("dir", "", "directory that holds migration directories")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	dsn := flags.String("dsn", "", "database connection string")
	historyTable := flags.String("history-table", "", "migration history table name")
	if err := flags.Parse(args); err != nil {
		return err
	}
	runner, migrations, closeDatabase, err := openRunner(context.Background(), *directory, *dialectName, *dsn, *historyTable)
	if err != nil {
		return err
	}
	defer closeDatabase()
	entries, err := runner.Status(context.Background(), migrations...)
	if err != nil {
		return redactError(err, *dsn)
	}
	for _, entry := range entries {
		if entry.State != migrate.StatusApplied {
			return fmt.Errorf("verify migrations: migration %q is %s", entry.ID, entry.State)
		}
	}
	fmt.Fprintln(commandOutput, "migration verification passed")
	return nil
}

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(commandOutput)
	return flags
}

func openRunner(ctx context.Context, directory string, dialectName string, dsn string, historyTable string) (migrate.Runner, []migrate.Migration, func(), error) {
	if directory == "" || dialectName == "" || dsn == "" {
		return migrate.Runner{}, nil, func() {}, errors.New("database commands require -dir, -dialect, and -dsn")
	}
	d, err := migrationDialect(dialectName)
	if err != nil {
		return migrate.Runner{}, nil, func() {}, err
	}
	migrations, err := loadMigrations(directory)
	if err != nil {
		return migrate.Runner{}, nil, func() {}, err
	}
	driverName, err := driverForDialect(d.Name())
	if err != nil {
		return migrate.Runner{}, nil, func() {}, err
	}
	database, err := openDatabase(driverName, dsn)
	if err != nil {
		return migrate.Runner{}, nil, func() {}, fmt.Errorf("open database: %w", redactError(err, dsn))
	}
	closeDatabase := func() {
		_ = database.Close()
	}
	if err := database.PingContext(ctx); err != nil {
		closeDatabase()
		return migrate.Runner{}, nil, func() {}, fmt.Errorf("connect to database: %w", redactError(err, dsn))
	}
	var runner migrate.Runner
	if historyTable == "" {
		runner, err = migrate.New(database, d)
	} else {
		runner, err = migrate.NewWithHistoryTable(database, d, historyTable)
	}
	if err != nil {
		closeDatabase()
		return migrate.Runner{}, nil, func() {}, err
	}
	return runner, migrations, closeDatabase, nil
}

func migrationDialect(name string) (dialect.Dialect, error) {
	switch name {
	case "postgres", "postgresql":
		return dialect.PostgreSQL(), nil
	case "mysql":
		return dialect.MySQL(), nil
	case "sqlite":
		return dialect.SQLite(), nil
	default:
		return nil, fmt.Errorf("unsupported migration dialect %q", name)
	}
}

func driverForDialect(name string) (string, error) {
	switch name {
	case "postgresql":
		return "pgx", nil
	case "mysql":
		return "mysql", nil
	case "sqlite":
		return "sqlite", nil
	default:
		return "", fmt.Errorf("unsupported migration dialect %q", name)
	}
}

func loadMigrations(directory string) ([]migrate.Migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read migration directory: %w", err)
	}
	directories := make([]string, 0)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if !entry.IsDir() {
			return nil, fmt.Errorf("migration directory %q contains non-directory entry %q", directory, entry.Name())
		}
		directories = append(directories, entry.Name())
	}
	sort.Strings(directories)
	if len(directories) == 0 {
		return nil, fmt.Errorf("migration directory %q has no migration directories", directory)
	}
	migrations := make([]migrate.Migration, len(directories))
	for index, name := range directories {
		migration, err := loadMigration(filepath.Join(directory, name), name)
		if err != nil {
			return nil, err
		}
		migrations[index] = migration
	}
	return migrations, nil
}

func loadMigration(directory string, id string) (migrate.Migration, error) {
	entries, err := os.ReadDir(directory)
	if err != nil {
		return migrate.Migration{}, fmt.Errorf("read migration %q: %w", id, err)
	}
	files := make([]string, 0)
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".sql" {
			return migrate.Migration{}, fmt.Errorf("migration %q contains non-SQL source %q", id, entry.Name())
		}
		files = append(files, entry.Name())
	}
	sort.Strings(files)
	if len(files) == 0 {
		return migrate.Migration{}, fmt.Errorf("migration %q has no SQL sources", id)
	}
	statements := make([]migrate.Statement, len(files))
	for index, filename := range files {
		source := filepath.Join(directory, filename)
		data, err := os.ReadFile(source)
		if err != nil {
			return migrate.Migration{}, fmt.Errorf("read migration %q SQL source %q: %w", id, filename, err)
		}
		statements[index] = migrate.Statement{Source: filename, SQL: string(data)}
	}
	migration := migrate.Migration{ID: id, Statements: statements}
	if err := migration.Validate(); err != nil {
		return migrate.Migration{}, err
	}
	return migration, nil
}

func writePlan(output io.Writer, migrations []migrate.Migration) {
	first := true
	for _, migration := range migrations {
		for _, statement := range migration.Statements {
			if !first {
				fmt.Fprintln(output)
			}
			first = false
			fmt.Fprintf(output, "-- %s/%s\n", migration.ID, statement.Source)
			fmt.Fprint(output, statement.SQL)
			if !strings.HasSuffix(statement.SQL, "\n") {
				fmt.Fprintln(output)
			}
		}
	}
}

func writeDiffPlan(output io.Writer, plan diff.Plan) {
	for index, statement := range plan.Statements {
		if index > 0 {
			fmt.Fprintln(output)
		}
		fmt.Fprintf(output, "-- %s: %s\n", statement.Source, statement.Summary)
		fmt.Fprint(output, statement.SQL)
		if !strings.HasSuffix(statement.SQL, "\n") {
			fmt.Fprintln(output)
		}
	}
}

func redactError(err error, dsn string) error {
	if err == nil || dsn == "" {
		return err
	}
	return errors.New(strings.ReplaceAll(err.Error(), dsn, "[redacted]"))
}
