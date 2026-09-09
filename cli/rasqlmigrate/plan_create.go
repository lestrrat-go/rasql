package rasqlmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"reflect"
	"sort"

	"github.com/lestrrat-go/rasql/internal/catalogread"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/lestrrat-go/rasql/internal/dsnredact"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

// defaultPlanHistoryTable matches the default rasqlgen and migrate history
// table name, so a plan created without -history-table lines up with the
// catalog scope rasql schema update already excluded when it wrote -lock.
const defaultPlanHistoryTable = "rasql_schema_migrations"

// changePlanProfile adapts an observed engineprofile.Profile to
// changeplan.ProfileSource. plan_test.go reuses it for the same purpose.
type changePlanProfile struct{ value engineprofile.Profile }

func (p changePlanProfile) ID() string                                  { return p.value.ID }
func (p changePlanProfile) Engine() changeplan.EngineID                 { return p.value.Engine }
func (p changePlanProfile) Version() changeplan.EngineVersion           { return p.value.Version }
func (p changePlanProfile) Capabilities() changeplan.EngineCapabilities { return p.value.Capabilities }
func (p changePlanProfile) Limits() changeplan.EngineLimits             { return p.value.Limits }

// runChangePlanCreate implements "plan create". It is the producer the audit
// found missing: everything changeplan needs (a baseline catalog, a lowered
// SQL diff, and per-operation result digests) comes from real inspection of
// -dsn, not from Go the caller writes.
//
// -dsn names a scratch database that already matches -lock's catalog; plan
// create runs every migration in -dir that database's migration history
// table has not recorded, for real, to observe each operation's resulting
// catalog. It never rolls a migration back afterward: MySQL commits DDL
// implicitly regardless, so a rollback would silently lie about what ran.
// A plan's baseline is verified against whatever database -dsn names again
// at check/apply time, so -dsn here may be a disposable copy of the schema
// rather than the database the plan will eventually be applied to.
func runChangePlanCreate(args []string) error {
	flags := newFlagSet("plan create")
	lockFile := addUniqueStringFlag(flags, "lock", "compiler lock file describing the plan's baseline catalog")
	directory := addUniqueStringFlag(flags, "dir", "directory that holds pending migration directories")
	dialectName := flags.String("dialect", "", "postgresql, mysql, or sqlite")
	dsn := flags.String("dsn", "", "connection string for a database that already matches -lock")
	historyTable := flags.String("history-table", "", "migration history table name")
	output := addUniqueStringFlag(flags, "output", "destination migration plan file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if len(flags.Args()) != 0 {
		return errors.New("plan create accepts no positional arguments")
	}
	if lockFile.value == "" || directory.value == "" || *dialectName == "" || *dsn == "" || output.value == "" {
		return errors.New("plan create requires -lock, -dir, -dialect, -dsn, and -output")
	}
	if _, err := os.Stat(output.value); err == nil {
		return fmt.Errorf("plan create: -output %q already exists; move it aside before creating a new plan there", output.value)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("plan create: stat -output: %w", err)
	}
	table := *historyTable
	if table == "" {
		table = defaultPlanHistoryTable
	}
	plan, err := buildChangePlan(context.Background(), lockFile.value, directory.value, *dialectName, *dsn, table)
	if err != nil {
		return dsnredact.Error(err, *dsn)
	}
	if err := changeplan.Write(output.value, plan); err != nil {
		return fmt.Errorf("plan create: write plan: %w", err)
	}
	_, _ = fmt.Fprintf(commandOutput, "created %s\n", output.value)
	return nil
}

func buildChangePlan(ctx context.Context, lockPath, directory, dialectName, dsn, historyTable string) (changeplan.Plan, error) {
	lockBytes, err := os.ReadFile(lockPath)
	if err != nil {
		return changeplan.Plan{}, fmt.Errorf("plan create: read -lock: %w", err)
	}
	lock, err := compilerlock.Decode(lockBytes)
	if err != nil {
		return changeplan.Plan{}, fmt.Errorf("plan create: decode -lock: %w", err)
	}
	baseline, err := changeplan.CatalogFromLock(lockBytes)
	if err != nil {
		return changeplan.Plan{}, fmt.Errorf("plan create: baseline catalog: %w", err)
	}
	migrations, err := migrationdir.Load(directory)
	if err != nil {
		return changeplan.Plan{}, err
	}
	dialectValue, err := migrationDialect(dialectName)
	if err != nil {
		return changeplan.Plan{}, err
	}
	engine, err := engineForDialectName(dialectValue.Name())
	if err != nil {
		return changeplan.Plan{}, err
	}
	driverName, err := driverForDialect(dialectValue.Name())
	if err != nil {
		return changeplan.Plan{}, err
	}
	database, err := openDatabase(driverName, dsn)
	if err != nil {
		return changeplan.Plan{}, fmt.Errorf("plan create: open database: %w", err)
	}
	defer func() { _ = database.Close() }()
	if err := database.PingContext(ctx); err != nil {
		return changeplan.Plan{}, fmt.Errorf("plan create: connect to database: %w", err)
	}

	runner, err := migrate.NewWithHistoryTable(database, dialectValue, historyTable)
	if err != nil {
		return changeplan.Plan{}, err
	}
	pending, err := pendingMigrations(ctx, runner, migrations)
	if err != nil {
		return changeplan.Plan{}, err
	}
	if len(pending) == 0 {
		return changeplan.Plan{}, errors.New("plan create: -dir has no pending migrations")
	}

	profile, err := engineprofile.Discover(ctx, database, engine, lock.Engine.Profile)
	if err != nil {
		return changeplan.Plan{}, fmt.Errorf("plan create: discover engine profile: %w", err)
	}
	source := changePlanProfile{profile}
	// pendingMigrations calls Status just below, which creates historyTable
	// (and, on MySQL always and on PostgreSQL sometimes, its "_progress"
	// companion) for real as a side effect. Both must stay out of the
	// catalog plan create observes, the same way rasqlgen's own schema
	// update scope keeps historyTable out of -lock's.
	scope := catalogread.Scope{
		HistoryTable: schema.ObjectName{Name: historyTable},
		Exclude:      []schema.ObjectName{{Name: historyTable + "_progress"}},
	}

	current, err := catalogObjectsFromLive(ctx, database, profile, scope, baseline)
	if err != nil {
		return changeplan.Plan{}, err
	}
	if err := requireCatalogMatches(baseline, current, "the live -dsn catalog does not match -lock; "+
		"run rasql schema update or apply pending schema changes first"); err != nil {
		return changeplan.Plan{}, err
	}

	history, err := changeplan.NewHistoryIdentity("", historyTable)
	if err != nil {
		return changeplan.Plan{}, err
	}

	steps := make([]changeplan.ResolvedCatalogStep, 0, len(pending))
	operations := make([]changeplan.Operation, 0, len(pending))
	var futureObjects []changeplan.BaselineObject
	var previousID changeplan.OperationID
	sourceIdentity := baseline.SourceIdentity()

	for _, one := range pending {
		operationID := changeplan.OperationID(one.ID)
		after, touched, introduced, err := observeMigration(ctx, database, profile, scope, current, one, sourceIdentity, operationID)
		if err != nil {
			return changeplan.Plan{}, fmt.Errorf("plan create: migration %q: %w", one.ID, err)
		}
		if len(touched) == 0 {
			return changeplan.Plan{}, fmt.Errorf("plan create: migration %q does not change a catalog object; "+
				"plan create cannot represent a data-only migration", one.ID)
		}
		afterCatalog, err := changeplan.NewCatalogLike(baseline, catalogObjectValues(after))
		if err != nil {
			return changeplan.Plan{}, err
		}
		digest, err := changeplan.CatalogDigest(afterCatalog)
		if err != nil {
			return changeplan.Plan{}, err
		}
		kind, err := operationKindFor(one.ID, touched, introduced)
		if err != nil {
			return changeplan.Plan{}, err
		}
		var dependsOn []changeplan.OperationID
		if previousID != "" {
			dependsOn = []changeplan.OperationID{previousID}
		}
		transaction := changeplan.TransactionEngineDefault
		if one.Mode == migrate.ExecutionModeNonTransactional {
			transaction = changeplan.TransactionForbidden
		}
		reversible := len(one.Down) > 0
		var reverse []stmt.Statement
		if reversible {
			reverse = toPlanStatements(one.Down)
		}
		operation, err := changeplan.NewOperation(operationID, kind, dependsOn, touched,
			nil, nil, digest, toPlanStatements(one.Statements), transaction, reversible, reverse)
		if err != nil {
			return changeplan.Plan{}, err
		}
		step, err := changeplan.NewResolvedCatalogStep(operationID, afterCatalog)
		if err != nil {
			return changeplan.Plan{}, err
		}
		operations = append(operations, operation)
		steps = append(steps, step)
		futureObjects = append(futureObjects, introduced...)
		current = after
		previousID = operationID
	}

	resolved, err := changeplan.NewResolvedChanges(baseline, steps, nil, operations, futureObjects, nil)
	if err != nil {
		return changeplan.Plan{}, err
	}
	return changeplan.FromLock(lockBytes, source, history, resolved)
}

// operationKindFor chooses the one changeplan operation kind a directory
// migration, whose statements are raw SQL rasql never parses, can honestly
// claim. changeplan requires that a baseline object introduced by an
// operation name that operation as create_table and as the operation's only
// touched object, so a migration is classified that way only when it
// introduces exactly one table and touches nothing else; any other
// introduction is reported rather than misclassified, since no other kind is
// legal for it. Everything else -- altering, dropping, or renaming existing
// objects, alone or together -- is native_sql: rasql did not decompose the
// SQL into a specific operation, so it does not claim to know which one it
// is.
func operationKindFor(migrationID string, touched []changeplan.ObjectID, introduced []changeplan.BaselineObject) (changeplan.OperationKind, error) {
	if len(introduced) == 0 {
		return changeplan.OperationNativeSQL, nil
	}
	if len(introduced) == 1 && len(touched) == 1 {
		return changeplan.OperationCreateTable, nil
	}
	return "", fmt.Errorf("migration %q introduces %d new table(s) alongside %d touched catalog object(s); "+
		"plan create requires a migration that creates a table to create only that table", migrationID, len(introduced), len(touched))
}

// pendingMigrations reports migrations in the given database's migration
// history table are not yet recorded, in directory order. It refuses a mixed
// state (an unknown, changed, or incomplete migration) rather than silently
// planning around one: whatever caused that state needs its own resolution
// first, through status/reconcile, not a change plan.
func pendingMigrations(ctx context.Context, runner migrate.Runner, migrations []migrate.Migration) ([]migrate.Migration, error) {
	entries, err := runner.Status(ctx, migrations...)
	if err != nil {
		return nil, fmt.Errorf("plan create: migration status: %w", err)
	}
	pendingIDs := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		switch entry.State {
		case migrate.StatusApplied:
		case migrate.StatusPending:
			pendingIDs[entry.ID] = struct{}{}
		default:
			return nil, fmt.Errorf("plan create: migration %q is %s; resolve it before creating a plan", entry.ID, entry.State)
		}
	}
	pending := make([]migrate.Migration, 0, len(migrations))
	for _, one := range migrations {
		if _, ok := pendingIDs[one.ID]; ok {
			pending = append(pending, one)
		}
	}
	return pending, nil
}

// objectKey identifies one catalog object the way changeplan does: by kind,
// schema, and name, never by its assigned ID.
type objectKey struct {
	kind       schema.ObjectKind
	schemaName string
	name       string
}

func keyForTable(table schema.TableDef) objectKey {
	return objectKey{kind: table.EffectiveKind(), schemaName: table.Schema, name: table.Name}
}

func catalogObjectValues(objects map[objectKey]changeplan.CatalogObject) []changeplan.CatalogObject {
	values := make([]changeplan.CatalogObject, 0, len(objects))
	for _, object := range objects {
		values = append(values, object)
	}
	return values
}

// catalogObjectsFromLive reads every catalog object -dsn currently holds and
// pairs each with the ID -lock already assigned it, so later steps can carry
// that same ID forward instead of minting a new one for an unchanged table.
func catalogObjectsFromLive(
	ctx context.Context,
	database *sql.DB,
	profile engineprofile.Profile,
	scope catalogread.Scope,
	baseline changeplan.Catalog,
) (map[objectKey]changeplan.CatalogObject, error) {
	read, err := catalogread.Read(ctx, database, profile, scope)
	if err != nil {
		return nil, fmt.Errorf("plan create: read live catalog: %w", err)
	}
	objects := make(map[objectKey]changeplan.CatalogObject, len(read.Tables))
	for _, table := range read.Tables {
		id, ok := baseline.ObjectID(table.EffectiveKind(), table.Schema, table.Name)
		if !ok {
			return nil, fmt.Errorf("plan create: live table %q is absent from -lock; run rasql schema update first", table.QualifiedName())
		}
		object, err := changeplan.NewCatalogObject(id, table)
		if err != nil {
			return nil, err
		}
		objects[keyForTable(table)] = object
	}
	return objects, nil
}

// requireCatalogMatches confirms observed reproduces baseline's digest
// exactly, so every later step's result digest is measured against a catalog
// changeplan.FromLock will accept as this plan's baseline.
func requireCatalogMatches(baseline changeplan.Catalog, observed map[objectKey]changeplan.CatalogObject, hint string) error {
	observedCatalog, err := changeplan.NewCatalogLike(baseline, catalogObjectValues(observed))
	if err != nil {
		return err
	}
	observedDigest, err := changeplan.CatalogDigest(observedCatalog)
	if err != nil {
		return err
	}
	baselineDigest, err := changeplan.CatalogDigest(baseline)
	if err != nil {
		return err
	}
	if observedDigest != baselineDigest {
		return fmt.Errorf("plan create: %s", hint)
	}
	return nil
}

// observeMigration runs one migration's forward statements against database
// for real and reads back the resulting catalog. It never rolls the
// migration back: see runChangePlanCreate's doc comment for why.
func observeMigration(
	ctx context.Context,
	database *sql.DB,
	profile engineprofile.Profile,
	scope catalogread.Scope,
	before map[objectKey]changeplan.CatalogObject,
	migration migrate.Migration,
	sourceIdentity string,
	operationID changeplan.OperationID,
) (map[objectKey]changeplan.CatalogObject, []changeplan.ObjectID, []changeplan.BaselineObject, error) {
	for _, statement := range migration.Statements {
		if _, err := database.ExecContext(ctx, string(statement.SQL)); err != nil {
			return nil, nil, nil, fmt.Errorf("run %q: %w", statement.Source, err)
		}
	}
	read, err := catalogread.Read(ctx, database, profile, scope)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("read resulting catalog: %w", err)
	}

	after := make(map[objectKey]changeplan.CatalogObject, len(read.Tables))
	touched := make(map[changeplan.ObjectID]struct{})
	var introduced []changeplan.BaselineObject
	seen := make(map[objectKey]struct{}, len(read.Tables))
	for _, table := range read.Tables {
		key := keyForTable(table)
		seen[key] = struct{}{}
		existing, ok := before[key]
		if !ok {
			object, err := changeplan.NewIntroducedBaselineObject(sourceIdentity, operationID, table)
			if err != nil {
				return nil, nil, nil, err
			}
			catalogObject, err := changeplan.NewCatalogObject(object.ID(), table)
			if err != nil {
				return nil, nil, nil, err
			}
			after[key] = catalogObject
			introduced = append(introduced, object)
			touched[object.ID()] = struct{}{}
			continue
		}
		catalogObject, err := changeplan.NewCatalogObject(existing.ID(), table)
		if err != nil {
			return nil, nil, nil, err
		}
		after[key] = catalogObject
		if !reflect.DeepEqual(existing.Definition(), table) {
			touched[existing.ID()] = struct{}{}
		}
	}
	for key, object := range before {
		if _, ok := seen[key]; !ok {
			touched[object.ID()] = struct{}{}
		}
	}
	ids := make([]changeplan.ObjectID, 0, len(touched))
	for id := range touched {
		ids = append(ids, id)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	return after, ids, introduced, nil
}

func toPlanStatements(statements []migrate.Statement) []stmt.Statement {
	out := make([]stmt.Statement, len(statements))
	for i, statement := range statements {
		out[i] = stmt.New(statement.SQL)
	}
	return out
}

func engineForDialectName(name string) (engineprofile.EngineID, error) {
	switch name {
	case "postgresql":
		return engineprofile.PostgreSQL, nil
	case "mysql":
		return engineprofile.MySQL, nil
	case "sqlite":
		return engineprofile.SQLite, nil
	default:
		return 0, fmt.Errorf("unsupported migration dialect %q", name)
	}
}
