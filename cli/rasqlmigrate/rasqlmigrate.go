// Package rasqlmigrate implements the rasql migrate and rasqlmigrate commands.
package rasqlmigrate

import (
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	_ "github.com/go-sql-driver/mysql"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/inspect"
	"github.com/lestrrat-go/rasql/internal/dsnredact"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/migrate/diff/mysql"
	"github.com/lestrrat-go/rasql/migrate/diff/postgresql"
	"github.com/lestrrat-go/rasql/migrate/diff/sqlite"
	"github.com/lestrrat-go/rasql/schema"
	_ "modernc.org/sqlite"
)

var (
	openDatabase                       = sql.Open
	commandOutput            io.Writer = os.Stdout
	commandDiagnostics       io.Writer = os.Stdout
	liveInspectionTimeout              = 30 * time.Second
	inspectSQLiteLiveCatalog           = sqlite.InspectLiveCatalog
	commandMu                sync.Mutex
)

// Run executes the migration subcommands under the unified rasql command.
// Command output goes to output, and what the flag package prints while
// parsing goes to diagnostics, so the unified command can keep the two on
// separate streams.
func Run(args []string, output, diagnostics io.Writer) error {
	if output == nil || diagnostics == nil {
		return errors.New("rasqlmigrate: command output must not be nil")
	}
	return runPublic(args, output, diagnostics, "rasql migrate")
}

// RunLegacy executes the migration subcommands using rasqlmigrate command
// names in usage and error messages, writing everything to writer.
func RunLegacy(args []string, writer io.Writer) error {
	if writer == nil {
		return errors.New("rasqlmigrate: command output must not be nil")
	}
	return runPublic(args, writer, writer, "rasqlmigrate")
}

func runPublic(args []string, output, diagnostics io.Writer, program string) error {
	commandMu.Lock()
	defer commandMu.Unlock()
	previousOutput, previousDiagnostics := commandOutput, commandDiagnostics
	commandOutput, commandDiagnostics = output, diagnostics
	defer func() {
		commandOutput, commandDiagnostics = previousOutput, previousDiagnostics
	}()
	return runNamed(args, program)
}

func run(args []string) error {
	return runNamed(args, "rasqlmigrate")
}

func runNamed(args []string, program string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: %s <diff|diff-live|dump|plan|apply|revert|status|verify|reconcile> [flags]", program)
	}
	switch args[0] {
	case "-h", "-help", "--help":
		printUsage(commandOutput, program)
		return flag.ErrHelp
	case "diff":
		return runDiff(args[1:])
	case "diff-live":
		return runDiffLive(args[1:])
	case "dump":
		return runDump(args[1:])
	case "plan":
		return runPlan(args[1:])
	case "apply":
		return runApply(args[1:])
	case "revert":
		return runRevert(args[1:])
	case "status":
		return runStatus(args[1:])
	case "verify":
		return runVerify(args[1:])
	case "reconcile":
		return runReconcile(args[1:])
	default:
		return fmt.Errorf("unknown %s command %q", program, args[0])
	}
}

func printUsage(output io.Writer, program string) {
	_, _ = fmt.Fprintf(output, "Usage: %s <command> [flags]\n", program)
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "Commands:")
	_, _ = fmt.Fprintln(output, "  diff     Generate a reviewed migration from desired schemas")
	_, _ = fmt.Fprintln(output, "  diff-live Compare one live table with a desired schema")
	_, _ = fmt.Fprintln(output, "  dump     Write rasql's own schema descriptor for a live database")
	_, _ = fmt.Fprintln(output, "  plan     Print directory sources, inspect a saved plan, check live state, or create one")
	_, _ = fmt.Fprintln(output, "  apply    Apply directory migrations or a reviewed plan")
	_, _ = fmt.Fprintln(output, "  revert   Revert applied migrations, newest first")
	_, _ = fmt.Fprintln(output, "  status   Show applied, pending, changed, unknown, and incomplete migrations")
	_, _ = fmt.Fprintln(output, "  verify   Require every supplied migration to be applied unchanged")
	_, _ = fmt.Fprintln(output, "  reconcile Resolve an interrupted migration with a database check")
	_, _ = fmt.Fprintln(output)
	_, _ = fmt.Fprintln(output, "-dir holds one directory per migration, named for its ID, which you create yourself.")
	_, _ = fmt.Fprintln(output, "Each holds .up.sql sources with matching .down.sql files, or .rasql-irreversible with a reason,")
	_, _ = fmt.Fprintln(output, "and one native SQL statement per file. Migrations run in directory-name order,")
	_, _ = fmt.Fprintln(output, "forward sources in ascending filename order and reverse sources in descending order,")
	_, _ = fmt.Fprintln(output, "so pad the numbers you name them with. The forward sources of an applied migration")
	_, _ = fmt.Fprintln(output, "must never change; revert it with revert, or add a new migration.")
	_, _ = fmt.Fprintf(output, "Run '%s <command> -h' for command flags.\n", program)
}

func runDiff(args []string) error {
	flags := newFlagSet("diff")
	dialectName := flags.String("dialect", "", "schema dialect; PostgreSQL, MySQL, and SQLite are currently supported")
	fromDirectory := flags.String("from", "", "baseline desired-schema directory")
	toDirectory := flags.String("to", "", "target desired-schema directory")
	outputDirectory := flags.String("output", "", "new migration directory; omit to preview")
	var resolutions resolutionFlags
	flags.Var(resolutions.forKind("backfill"), "backfill", "resolve a backfill decision as decision-id=sql-file")
	flags.Var(resolutions.forKind("rename"), "rename", "resolve a rename decision as decision-id=baseline-column")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return fmt.Errorf("reconcile accepts no positional arguments")
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
	if len(resolutions.values) > 0 {
		parsed, parseErr := parseResolutions(resolutions.values)
		if parseErr != nil {
			return parseErr
		}
		plan, err = resolveCLIPlan(plan, parsed)
		if err != nil {
			return err
		}
	}
	if plan.Empty() {
		_, _ = fmt.Fprintln(commandOutput, "no schema changes")
		return nil
	}
	if *outputDirectory == "" {
		writeDiffPlan(commandOutput, plan)
		return nil
	}
	if err := diff.WriteMigration(*outputDirectory, plan); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(commandOutput, "created %s\n", *outputDirectory)
	return nil
}

func runDiffLive(args []string) error {
	flags := newFlagSet("diff-live")
	dialectName := flags.String("dialect", "", "database dialect; PostgreSQL, MySQL, and SQLite are currently supported")
	dsn := flags.String("dsn", "", "database connection string")
	tableName := flags.String("table", "", "one live table to inspect")
	targetDirectory := flags.String("to", "", "desired-schema directory for the inspected table")
	outputDirectory := flags.String("output", "", "new migration directory; omit to preview")
	var resolutions resolutionFlags
	flags.Var(resolutions.forKind("backfill"), "backfill", "resolve a backfill decision as decision-id=sql-file")
	flags.Var(resolutions.forKind("rename"), "rename", "resolve a rename decision as decision-id=baseline-column")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dialectName == "" || *dsn == "" || *tableName == "" || *targetDirectory == "" {
		return errors.New("diff-live requires -dialect, -dsn, -table, and -to")
	}
	d, err := migrationDialect(*dialectName)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), liveInspectionTimeout)
	defer cancel()
	database, closeDatabase, err := openMigrationDatabase(ctx, d, *dsn)
	if err != nil {
		return err
	}
	defer func() {
		if ctx.Err() == nil {
			closeDatabase()
		}
	}()
	transaction, err := runWithHardDeadline(ctx, func() (*sql.Tx, error) {
		return database.BeginTx(ctx, liveInspectionTxOptions(d.Name()))
	})
	if err != nil {
		return dsnredact.Error(fmt.Errorf("begin live inspection transaction: %w", err), *dsn)
	}
	defer func() {
		if ctx.Err() == nil {
			_ = transaction.Rollback()
		}
	}()
	analyzer, err := liveSchemaAnalyzer(ctx, transaction, d.Name())
	if err != nil {
		return dsnredact.Error(err, *dsn)
	}
	liveAnalyzer, ok := analyzer.(diff.LiveAnalyzer)
	if !ok {
		return fmt.Errorf("schema diff dialect %q does not support live inspection", *dialectName)
	}
	inspector, err := inspect.New(transaction, d)
	if err != nil {
		return err
	}
	liveTable, err := runWithHardDeadline(ctx, func() (schema.TableDef, error) {
		return inspector.Table(ctx, *tableName)
	})
	if err != nil {
		return dsnredact.Error(err, *dsn)
	}
	liveSources, err := liveAnalyzer.LiveSources(liveTable)
	if err != nil {
		return err
	}
	baseline, err := analyzer.Parse(liveSources)
	if err != nil {
		return fmt.Errorf("parse inspected table %q: %w", *tableName, err)
	}
	baseline, err = attachSQLiteLiveCatalog(ctx, transaction, analyzer, baseline, *tableName)
	if err != nil {
		return err
	}
	targetSources, err := diff.LoadSources(*targetDirectory)
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
	if err := liveAnalyzer.ValidateLivePlan(plan, *tableName); err != nil {
		return err
	}
	if len(resolutions.values) > 0 {
		parsed, parseErr := parseResolutions(resolutions.values)
		if parseErr != nil {
			return parseErr
		}
		plan, err = resolveCLIPlan(plan, parsed)
		if err != nil {
			return err
		}
	}
	if plan.Empty() {
		_, _ = fmt.Fprintln(commandOutput, "no schema changes")
		return nil
	}
	if *outputDirectory == "" {
		writeDiffPlan(commandOutput, plan)
		return nil
	}
	if err := diff.WriteMigration(*outputDirectory, plan); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(commandOutput, "created %s\n", *outputDirectory)
	return nil
}

func resolveCLIPlan(plan diff.Plan, resolutions []diff.Resolution) (diff.Plan, error) {
	resolved, err := plan.Resolve(resolutions...)
	if err != nil && strings.HasPrefix(err.Error(), "migrate diff: backfill resolution ") {
		return diff.Plan{}, fmt.Errorf("resolve -backfill: %w", err)
	}
	return resolved, err
}

type sqliteLiveCatalogQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func attachSQLiteLiveCatalog(ctx context.Context, queryer sqliteLiveCatalogQueryer, analyzer diff.Analyzer, baseline diff.Snapshot, tableName string) (diff.Snapshot, error) {
	sqliteAnalyzer, ok := analyzer.(sqlite.Analyzer)
	if !ok {
		return baseline, nil
	}
	facts, err := inspectSQLiteLiveCatalog(ctx, queryer, tableName)
	if err != nil {
		return nil, fmt.Errorf("inspect SQLite catalog: %w", err)
	}
	return sqliteAnalyzer.AttachLiveCatalog(baseline, facts)
}

func liveSchemaAnalyzer(ctx context.Context, transaction *sql.Tx, dialectName string) (diff.Analyzer, error) {
	if dialectName != "mysql" {
		return schemaAnalyzer(dialectName)
	}
	lowerCaseTableNames, err := runWithHardDeadline(ctx, func() (int64, error) {
		var value int64
		if err := transaction.QueryRowContext(ctx, "SELECT @@lower_case_table_names").Scan(&value); err != nil {
			return 0, err
		}
		return value, nil
	})
	if err != nil {
		return nil, fmt.Errorf("read MySQL lower_case_table_names: %w", err)
	}
	if lowerCaseTableNames < 0 || lowerCaseTableNames > 2 {
		return nil, fmt.Errorf("read MySQL lower_case_table_names: unsupported value %d", lowerCaseTableNames)
	}
	return mysql.NewWithLowerCaseTableNames(mysql.LowerCaseTableNames(lowerCaseTableNames)), nil
}

func liveInspectionTxOptions(dialectName string) *sql.TxOptions {
	options := &sql.TxOptions{ReadOnly: true}
	switch dialectName {
	case "mysql", "postgresql":
		options.Isolation = sql.LevelRepeatableRead
	}
	return options
}

func schemaAnalyzer(name string) (diff.Analyzer, error) {
	switch name {
	case "postgres", "postgresql":
		return postgresql.New(), nil
	case "mysql":
		return mysql.New(), nil
	case "sqlite":
		return sqlite.New(), nil
	default:
		return nil, fmt.Errorf("unsupported schema diff dialect %q", name)
	}
}

func runPlan(args []string) error {
	if len(args) > 0 && args[0] == "check" {
		return runChangePlanCheck(args[1:])
	}
	if len(args) > 0 && args[0] == "create" {
		return runChangePlanCreate(args[1:])
	}
	flags := newFlagSet("plan")
	directory := addUniqueStringFlag(flags, "dir", "directory that holds migration directories")
	file := addUniqueStringFlag(flags, "file", "serialized migration plan file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("plan accepts no positional arguments")
	}
	if directory.set == file.set {
		return errors.New("plan requires exactly one of -dir and -file")
	}
	if directory.set && directory.value == "" {
		return errors.New("plan -dir must not be empty")
	}
	if file.set && file.value == "" {
		return errors.New("plan -file must not be empty")
	}
	if file.set {
		plan, err := changeplan.Read(file.value)
		if err != nil {
			return fmt.Errorf("read migration plan: %w", err)
		}
		output, err := formatChangePlan(plan)
		if err != nil {
			return err
		}
		_, _ = commandOutput.Write(output)
		return nil
	}
	migrations, err := migrationdir.Load(directory.value)
	if err != nil {
		return err
	}
	writePlan(commandOutput, migrations)
	return nil
}

func runApply(args []string) error {
	flags := newFlagSet("apply")
	directory := addUniqueStringFlag(flags, "dir", "directory that holds migration directories")
	planFile := addUniqueStringFlag(flags, "plan", "serialized migration plan file")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	dsn := flags.String("dsn", "", "database connection string")
	historyTable := flags.String("history-table", "", "migration history table name")
	through := flags.String("to", "", "stop at this migration, applying it and every pending migration before it")
	dryRun := flags.Bool("dry-run", false, "print the SQL the apply would run without running it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("apply accepts no positional arguments")
	}
	if directory.set == planFile.set {
		return errors.New("apply requires exactly one of -dir and -plan")
	}
	if directory.set && directory.value == "" {
		return errors.New("apply -dir must not be empty")
	}
	if planFile.set && planFile.value == "" {
		return errors.New("apply -plan must not be empty")
	}
	if planFile.set {
		var directoryOnly string
		flags.Visit(func(flagValue *flag.Flag) {
			if flagValue.Name == "to" || flagValue.Name == "dry-run" {
				directoryOnly = flagValue.Name
			}
		})
		if directoryOnly != "" {
			return fmt.Errorf("apply -%s is valid only with -dir", directoryOnly)
		}
		return runChangePlanApply(planFile.value, *dialectName, *dsn, *historyTable)
	}
	target := migrate.AllPending()
	if *through != "" {
		target = migrate.ApplyThrough(*through)
	}
	runner, migrations, closeDatabase, err := openRunner(context.Background(), directory.value, *dialectName, *dsn, *historyTable)
	if err != nil {
		return err
	}
	defer closeDatabase()

	if *dryRun {
		plan, err := runner.ApplyPlan(context.Background(), target, migrations...)
		if err != nil {
			return dsnredact.Error(err, *dsn)
		}
		writePlan(commandOutput, plan)
		return nil
	}
	applied, err := runner.Apply(context.Background(), target, migrations...)
	if err != nil {
		return dsnredact.Error(err, *dsn)
	}
	for _, migration := range applied {
		_, _ = fmt.Fprintf(commandOutput, "applied\t%s\n", migration.ID)
	}
	_, _ = fmt.Fprintf(commandOutput, "migration apply completed: %d applied\n", len(applied))
	return nil
}

// runRevert implements the "revert" command. It requires exactly one of -to
// and -steps, so a run that names no target reverts nothing rather than
// defaulting to some depth a user did not ask for.
func runRevert(args []string) error {
	flags := newFlagSet("revert")
	directory := flags.String("dir", "", "directory that holds migration directories")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	dsn := flags.String("dsn", "", "database connection string")
	historyTable := flags.String("history-table", "", "migration history table name")
	through := flags.String("to", "", "leave the database where this migration was applied, reverting every migration after it")
	steps := flags.Int("steps", 0, "revert this many of the newest applied migrations")
	dryRun := flags.Bool("dry-run", false, "print the SQL the revert would run without running it")
	if err := flags.Parse(args); err != nil {
		return err
	}
	target, err := revertTarget(*through, *steps)
	if err != nil {
		return err
	}
	runner, migrations, closeDatabase, err := openRunner(context.Background(), *directory, *dialectName, *dsn, *historyTable)
	if err != nil {
		return err
	}
	defer closeDatabase()

	if *dryRun {
		plan, err := runner.RevertPlan(context.Background(), target, migrations...)
		if err != nil {
			return dsnredact.Error(err, *dsn)
		}
		writeRevertPlan(commandOutput, plan)
		return nil
	}
	reverted, err := runner.Revert(context.Background(), target, migrations...)
	if err != nil {
		return dsnredact.Error(err, *dsn)
	}
	for _, migration := range reverted {
		_, _ = fmt.Fprintf(commandOutput, "reverted\t%s\n", migration.ID)
	}
	_, _ = fmt.Fprintf(commandOutput, "migration revert completed: %d reverted\n", len(reverted))
	return nil
}

// revertTarget turns the -to and -steps flags into one target, refusing the
// run when neither or both are given. "Neither" is the case worth refusing
// loudest: a revert command with no target that reverted everything would be
// one keystroke away from an empty database.
func revertTarget(through string, steps int) (migrate.RevertTarget, error) {
	switch {
	case through != "" && steps != 0:
		return migrate.RevertTarget{}, errors.New("revert accepts -to or -steps, not both")
	case through != "":
		return migrate.Through(through), nil
	case steps != 0:
		return migrate.Steps(steps), nil
	default:
		return migrate.RevertTarget{}, errors.New("revert requires -to or -steps")
	}
}

// writeRevertPlan prints the reverse sources a revert would run, in the
// order it would run them, in the same form writePlan uses for forward
// sources.
func writeRevertPlan(output io.Writer, plan []migrate.Migration) {
	first := true
	for _, migration := range plan {
		if migration.Mode == migrate.ExecutionModeNonTransactional {
			if !first {
				_, _ = fmt.Fprintln(output)
			}
			first = false
			_, _ = fmt.Fprintf(output, "-- %s mode: nontransactional\n", migration.ID)
		}
		for _, statement := range migration.Down {
			if !first {
				_, _ = fmt.Fprintln(output)
			}
			first = false
			_, _ = fmt.Fprintf(output, "-- %s/%s\n", migration.ID, statement.Source)
			_, _ = fmt.Fprint(output, statement.SQL)
			if !strings.HasSuffix(string(statement.SQL), "\n") {
				_, _ = fmt.Fprintln(output)
			}
		}
	}
}

func runReconcile(args []string) error {
	flags := newFlagSet("reconcile")
	directory := flags.String("dir", "", "directory that holds migration directories")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	dsn := flags.String("dsn", "", "database connection string")
	historyTable := flags.String("history-table", "", "migration history table name")
	id := flags.String("id", "", "incomplete migration ID")
	query := flags.String("check", "", "query returning exactly one non-NULL boolean")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("reconcile accepts no positional arguments")
	}
	if *id == "" || *query == "" {
		return errors.New("reconcile requires -id and -check")
	}
	runner, migrations, closeDatabase, err := openRunner(context.Background(), *directory, *dialectName, *dsn, *historyTable)
	if err != nil {
		return err
	}
	defer closeDatabase()
	check := &sqlReconcileCheck{id: *id, query: *query}
	if err := runner.Reconcile(context.Background(), check, migrations...); err != nil {
		return dsnredact.Error(err, *dsn)
	}
	entries, err := runner.Status(context.Background(), migrations...)
	if err != nil {
		return dsnredact.Error(err, *dsn)
	}
	for _, entry := range entries {
		if entry.ID == *id {
			_, _ = fmt.Fprintf(commandOutput, "reconciled\t%s\t%s\t%s\n", *id, check.observed, entry.State)
			return nil
		}
	}
	return fmt.Errorf("reconcile migration %q is absent after reconciliation", *id)
}

type sqlReconcileCheck struct {
	id       string
	query    string
	observed migrate.ReconcileDecision
}

func (c *sqlReconcileCheck) Check(ctx context.Context, connection *sql.Conn, incomplete migrate.IncompleteMigration) (migrate.ReconcileDecision, error) {
	if incomplete.ID != c.id {
		return "", fmt.Errorf("reconcile migration ID %q does not match incomplete migration %q", c.id, incomplete.ID)
	}
	query := strings.TrimSpace(c.query)
	if query == "" {
		return "", errors.New("reconcile check must contain exactly one SQL statement")
	}
	transaction, err := connection.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return "", err
	}
	defer func() { _ = transaction.Rollback() }()
	rows, err := transaction.QueryContext(ctx, query)
	if err != nil {
		return "", err
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", err
		}
		return "", errors.New("reconcile check returned no rows")
	}
	var executed sql.NullBool
	if err := rows.Scan(&executed); err != nil {
		return "", err
	}
	if !executed.Valid {
		return "", errors.New("reconcile check returned NULL")
	}
	if rows.Next() {
		return "", errors.New("reconcile check returned more than one row")
	}
	if err := rows.Err(); err != nil {
		return "", err
	}
	if executed.Bool {
		c.observed = migrate.ReconcileExecuted
		return migrate.ReconcileExecuted, nil
	}
	c.observed = migrate.ReconcileNotExecuted
	return migrate.ReconcileNotExecuted, nil
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
		return dsnredact.Error(err, *dsn)
	}
	for _, entry := range entries {
		_, _ = fmt.Fprintf(commandOutput, "%s\t%s\n", entry.State, entry.ID)
		if entry.Incomplete != nil {
			_, _ = fmt.Fprintf(commandOutput, "  source=%s direction=%s index=%d\n", entry.Incomplete.Source, entry.Incomplete.Direction, entry.Incomplete.SourceIndex)
		}
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
		return dsnredact.Error(err, *dsn)
	}
	for _, entry := range entries {
		if entry.State != migrate.StatusApplied {
			if entry.Incomplete != nil {
				return fmt.Errorf("verify migrations: migration %q is incomplete at %s (%s source %d)", entry.ID, entry.Incomplete.Source, entry.Incomplete.Direction, entry.Incomplete.SourceIndex)
			}
			return fmt.Errorf("verify migrations: migration %q is %s", entry.ID, entry.State)
		}
	}
	_, _ = fmt.Fprintln(commandOutput, "migration verification passed")
	return nil
}

// newFlagSet builds a subcommand's flag set. A flag set has one writer for
// both a parse diagnostic and a help listing, so everything it prints goes to
// the diagnostic stream and the caller sorts the two out by the error the run
// returned; the standalone rasqlmigrate binary points both streams at one
// writer and so keeps printing where it always did.
//
// Every migration subcommand parses with this flag set and returns whatever
// Parse returns, so `-h` reports flag.ErrHelp and exits 0 with any later
// argument unread: `plan -h -unknown` prints the plan usage and says nothing
// about -unknown. That is inherited behavior, not something the unified
// command introduced. Verified against origin/main (5ecf71e) by building both
// trees: the base rasqlmigrate binary prints the same bytes and exits 0 for
// the same arguments, because base cmd/rasqlmigrate/main.go's runPlan is this
// same code. cli/rasqlgen rejects leftover arguments through
// parseCommandFlags, and that asymmetry predates this package too.
func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(commandDiagnostics)
	return flags
}

type resolutionFlagValue struct {
	kind   string
	values *[]resolutionFlag
}

type resolutionFlag struct {
	kind, value string
}

type resolutionFlags struct{ values []resolutionFlag }

func (f *resolutionFlags) forKind(kind string) *resolutionFlagValue {
	return &resolutionFlagValue{kind: kind, values: &f.values}
}

func (f *resolutionFlagValue) String() string { return "" }
func (f *resolutionFlagValue) Set(value string) error {
	*f.values = append(*f.values, resolutionFlag{kind: f.kind, value: value})
	return nil
}

func parseResolutions(flags []resolutionFlag) ([]diff.Resolution, error) {
	resolutions := make([]diff.Resolution, 0, len(flags))
	for _, flag := range flags {
		parts := strings.SplitN(flag.value, "=", 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			return nil, fmt.Errorf("diff -%s requires decision-id=value", flag.kind)
		}
		resolution := diff.Resolution{DecisionID: parts[0]}
		switch flag.kind {
		case "backfill":
			source, err := os.ReadFile(parts[1])
			if err != nil {
				return nil, fmt.Errorf("read -backfill %q for decision %q: %w", parts[1], parts[0], err)
			}
			resolution.BackfillSQL = string(source)
		case "rename":
			resolution.RenameFrom = parts[1]
		default:
			return nil, fmt.Errorf("unsupported resolution flag %q", flag.kind)
		}
		resolutions = append(resolutions, resolution)
	}
	return resolutions, nil
}

func openRunner(ctx context.Context, directory string, dialectName string, dsn string, historyTable string) (migrate.Runner, []migrate.Migration, func(), error) {
	if directory == "" || dialectName == "" || dsn == "" {
		return migrate.Runner{}, nil, func() {}, errors.New("database commands require -dir, -dialect, and -dsn")
	}
	d, err := migrationDialect(dialectName)
	if err != nil {
		return migrate.Runner{}, nil, func() {}, err
	}
	migrations, err := migrationdir.Load(directory)
	if err != nil {
		return migrate.Runner{}, nil, func() {}, err
	}
	database, closeDatabase, err := openMigrationDatabase(ctx, d, dsn)
	if err != nil {
		return migrate.Runner{}, nil, func() {}, err
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

func openMigrationDatabase(ctx context.Context, d dialect.Dialect, dsn string) (*sql.DB, func(), error) {
	driverName, err := driverForDialect(d.Name())
	if err != nil {
		return nil, func() {}, err
	}
	database, err := openDatabase(driverName, dsn)
	if err != nil {
		return nil, func() {}, fmt.Errorf("open database: %w", dsnredact.Error(err, dsn))
	}
	closeDatabase := func() {
		_ = database.Close()
	}
	if _, err := runWithHardDeadline(ctx, func() (struct{}, error) {
		return struct{}{}, database.PingContext(ctx)
	}); err != nil {
		if ctx.Err() == nil {
			closeDatabase()
		}
		return nil, func() {}, fmt.Errorf("connect to database: %w", dsnredact.Error(err, dsn))
	}
	return database, closeDatabase, nil
}

func runWithHardDeadline[T any](ctx context.Context, operation func() (T, error)) (T, error) {
	type result struct {
		value T
		err   error
	}
	done := make(chan result, 1)
	go func() {
		value, err := operation()
		done <- result{value: value, err: err}
	}()
	select {
	case <-ctx.Done():
		var zero T
		return zero, ctx.Err()
	case completed := <-done:
		if err := ctx.Err(); err != nil {
			var zero T
			return zero, err
		}
		return completed.value, completed.err
	}
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

func writePlan(output io.Writer, migrations []migrate.Migration) {
	first := true
	for _, migration := range migrations {
		if migration.Mode == migrate.ExecutionModeNonTransactional {
			_, _ = fmt.Fprintf(output, "-- %s mode: nontransactional\n", migration.ID)
		}
		for _, statement := range migration.Statements {
			if !first {
				_, _ = fmt.Fprintln(output)
			}
			first = false
			_, _ = fmt.Fprintf(output, "-- %s/%s\n", migration.ID, statement.Source)
			_, _ = fmt.Fprint(output, statement.SQL)
			if !strings.HasSuffix(string(statement.SQL), "\n") {
				_, _ = fmt.Fprintln(output)
			}
		}
	}
}

func writeDiffPlan(output io.Writer, plan diff.Plan) {
	for _, operation := range plan.Operations {
		_, _ = fmt.Fprintf(output, "-- operation %s (%s): %s\n", operation.ID, operation.Kind, operation.Summary)
	}
	for _, decision := range plan.Decisions {
		_, _ = fmt.Fprintf(output, "-- decision %s (%s): %s\n", decision.ID, decision.Kind, decision.Reason)
	}
	if len(plan.Decisions) > 0 {
		return
	}
	if plan.Mode == migrate.ExecutionModeNonTransactional {
		_, _ = fmt.Fprintln(output, "-- mode: nontransactional")
	}
	for index, statement := range plan.Statements {
		if index > 0 {
			_, _ = fmt.Fprintln(output)
		}
		_, _ = fmt.Fprintf(output, "-- %s: %s\n", statement.Source, statement.Summary)
		_, _ = fmt.Fprint(output, statement.SQL)
		if !strings.HasSuffix(statement.SQL, "\n") {
			_, _ = fmt.Fprintln(output)
		}
		if statement.ReverseSQL != "" {
			_, _ = fmt.Fprintf(output, "-- reverse %s\n", strings.TrimSuffix(statement.Source, ".sql")+".down.sql")
			_, _ = fmt.Fprint(output, statement.ReverseSQL)
			if !strings.HasSuffix(statement.ReverseSQL, "\n") {
				_, _ = fmt.Fprintln(output)
			}
		}
	}
	if plan.IrreversibleReason != "" {
		_, _ = fmt.Fprintf(output, "irreversible: %s\n", plan.IrreversibleReason)
	}
}
