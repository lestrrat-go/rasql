//go:build unix

package rasqlmigrate

// dump_integration_test.go pins what dump.go's design claims actually do
// against live PostgreSQL 17 and MySQL 8.4 servers, the way
// inspect/text_width_integration_test.go pins live behavior for the text
// width work: sqlmock and other fixture tests assert rasql's own output
// back to itself, never against a real engine.
//
// Most tests here drive dumpFilesFromDatabase directly against a *sql.DB.
// Refusal tests that claim atomic disk behavior invoke runDump with the
// parsed dbtest connection configuration, so they exercise the real output
// command without reconstructing a DSN by hand.
import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	gomysql "github.com/go-sql-driver/mysql"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/migrate"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func runPostgreSQLDumpCommand(t *testing.T, outputDirectory string) error {
	t.Helper()
	config := dbtest.PostgreSQLConfig(t).Copy()
	parsed, err := url.Parse(config.ConnString())
	require.NoError(t, err)
	parsed.Path = "/" + config.Database
	query := parsed.Query()
	query.Set("options", "-c search_path=public")
	parsed.RawQuery = query.Encode()
	dsn := parsed.String()
	return runDump([]string{"-dialect", "postgresql", "-dsn", dsn, "-table", "sequence_cases", "-format", "schema", "-output", outputDirectory})
}

func TestDumpPostgreSQLSequenceExportRefusesAmbiguousDefaults(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE SEQUENCE ambiguous_first`)
	dumpMustExec(t, ctx, source, `CREATE SEQUENCE ambiguous_second`)
	dumpMustExec(t, ctx, source, `CREATE TABLE sequence_cases (id BIGINT NOT NULL DEFAULT (nextval('ambiguous_first') + nextval('ambiguous_second')))`)

	outputDirectory := filepath.Join(t.TempDir(), "schema")
	err := runPostgreSQLDumpCommand(t, outputDirectory)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sequence_cases")
	require.Contains(t, err.Error(), "ambiguous_first")
	_, statErr := os.Stat(outputDirectory)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestDumpPostgreSQLSequenceExportRefusesRenamedOwnedSequence(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE SEQUENCE renamed_sequence`)
	dumpMustExec(t, ctx, source, `CREATE TABLE sequence_cases (id BIGINT NOT NULL DEFAULT nextval('renamed_sequence'))`)
	dumpMustExec(t, ctx, source, `ALTER SEQUENCE renamed_sequence OWNED BY sequence_cases.id`)
	outputDirectory := filepath.Join(t.TempDir(), "schema")
	err := runPostgreSQLDumpCommand(t, outputDirectory)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sequence_cases")
	require.Contains(t, err.Error(), "renamed_sequence")
	_, statErr := os.Stat(outputDirectory)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

func TestDumpPostgreSQLSequenceExportRefusesUnownedExclusiveSequence(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE SEQUENCE sequence_cases_id_seq`)
	dumpMustExec(t, ctx, source, `CREATE TABLE sequence_cases (id BIGINT NOT NULL DEFAULT nextval('sequence_cases_id_seq'))`)
	outputDirectory := filepath.Join(t.TempDir(), "schema")
	err := runPostgreSQLDumpCommand(t, outputDirectory)
	require.Error(t, err)
	require.Contains(t, err.Error(), "sequence_cases")
	require.Contains(t, err.Error(), "sequence_cases_id_seq")
	_, statErr := os.Stat(outputDirectory)
	require.ErrorIs(t, statErr, os.ErrNotExist)
}

type postgresSequenceState struct {
	OID                 int64
	Schema              string
	Name                string
	ReferencingDefaults int64
	Start               int64
	Increment           int64
	Minimum             int64
	Maximum             int64
	Cache               int64
	Cycle               bool
	Owned               bool
}

func postgresSequenceStateFor(t *testing.T, ctx context.Context, database *sql.DB, tableName, columnName string) postgresSequenceState {
	t.Helper()
	var state postgresSequenceState
	err := database.QueryRowContext(ctx, `
		SELECT seq.oid, seqns.nspname, seq.relname,
			(SELECT count(DISTINCT ad2.oid) FROM pg_attrdef ad2
			 JOIN pg_depend dep2 ON dep2.classid = 'pg_attrdef'::regclass AND dep2.objid = ad2.oid
			  AND dep2.refclassid = 'pg_class'::regclass AND dep2.deptype = 'n'
			 WHERE dep2.refobjid = seq.oid),
			ps.seqstart, ps.seqincrement, ps.seqmin, ps.seqmax, ps.seqcache, ps.seqcycle,
			EXISTS (
				SELECT 1 FROM pg_depend own
				WHERE own.classid = 'pg_class'::regclass AND own.objid = seq.oid
				  AND own.refclassid = 'pg_class'::regclass AND own.refobjid = rel.oid
				  AND own.refobjsubid = att.attnum AND own.deptype = 'a'
			) AS owned
		FROM pg_class rel
		JOIN pg_namespace ns ON ns.oid = rel.relnamespace AND ns.nspname = current_schema()
		JOIN pg_attribute att ON att.attrelid = rel.oid AND att.attname = $2 AND NOT att.attisdropped
		JOIN pg_attrdef ad ON ad.adrelid = att.attrelid AND ad.adnum = att.attnum
		JOIN pg_depend dep ON dep.classid = 'pg_attrdef'::regclass AND dep.objid = ad.oid
		JOIN pg_class seq ON seq.oid = dep.refobjid AND seq.relkind = 'S'
		JOIN pg_namespace seqns ON seqns.oid = seq.relnamespace
		JOIN pg_sequence ps ON ps.seqrelid = seq.oid
		WHERE rel.relname = $1`, tableName, columnName).Scan(&state.OID, &state.Schema, &state.Name, &state.ReferencingDefaults, &state.Start, &state.Increment, &state.Minimum, &state.Maximum, &state.Cache, &state.Cycle, &state.Owned)
	require.NoError(t, err)
	return state
}

func TestDumpPostgreSQLSequenceExportReplayProof(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE SEQUENCE shared_sequence START WITH 10 INCREMENT BY 1`)
	dumpMustExec(t, ctx, source, `CREATE SEQUENCE custom_sequence START WITH 10 INCREMENT BY 5`)
	dumpMustExec(t, ctx, source, `CREATE TABLE sequence_cases (
		shared_first BIGINT NOT NULL DEFAULT nextval('shared_sequence'),
		shared_second BIGINT NOT NULL DEFAULT nextval('shared_sequence'),
		custom_value BIGINT NOT NULL DEFAULT nextval('custom_sequence'),
		control BIGSERIAL NOT NULL
	)`)
	dumpMustExec(t, ctx, source, `ALTER SEQUENCE custom_sequence OWNED BY sequence_cases.custom_value`)

	sourceTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.PostgreSQL()})
	require.NoError(t, err)
	require.Len(t, sourceTables, 1)
	statement, err := renderPostgreSQLCreateTable(dialect.PostgreSQL(), sourceTables[0])
	require.NoError(t, err)

	sharedFirst := postgresSequenceStateFor(t, ctx, source, "sequence_cases", "shared_first")
	sharedSecond := postgresSequenceStateFor(t, ctx, source, "sequence_cases", "shared_second")
	custom := postgresSequenceStateFor(t, ctx, source, "sequence_cases", "custom_value")
	require.Equal(t, sharedFirst.OID, sharedSecond.OID)
	require.Equal(t, int64(1), sharedFirst.Increment)
	require.Equal(t, int64(5), custom.Increment)
	require.False(t, sharedFirst.Owned)
	require.True(t, custom.Owned)

	var sourceFirst, sourceSecond, sourceCustom int64
	dumpMustExec(t, ctx, source, `INSERT INTO sequence_cases DEFAULT VALUES`)
	require.NoError(t, source.QueryRowContext(ctx, `SELECT shared_first, shared_second, custom_value FROM sequence_cases`).Scan(&sourceFirst, &sourceSecond, &sourceCustom))
	require.Equal(t, int64(10), sourceFirst)
	require.Equal(t, int64(11), sourceSecond)
	require.Equal(t, int64(10), sourceCustom)

	dumpMustExec(t, ctx, source, `DROP TABLE sequence_cases CASCADE`)
	dumpMustExec(t, ctx, source, `DROP SEQUENCE shared_sequence`)
	target := dbtest.PostgreSQLDB(t)
	_, err = target.ExecContext(ctx, statement+";")
	require.NoError(t, err)
	targetFirst := postgresSequenceStateFor(t, ctx, target, "sequence_cases", "shared_first")
	targetSecond := postgresSequenceStateFor(t, ctx, target, "sequence_cases", "shared_second")
	targetCustom := postgresSequenceStateFor(t, ctx, target, "sequence_cases", "custom_value")
	require.NotEqual(t, targetFirst.OID, targetSecond.OID)
	require.Equal(t, int64(1), targetFirst.Increment)
	require.Equal(t, int64(1), targetCustom.Increment)
	require.True(t, targetFirst.Owned)
	require.True(t, targetCustom.Owned)

	var targetFirstValue, targetSecondValue, targetCustomValue int64
	dumpMustExec(t, ctx, target, `INSERT INTO sequence_cases DEFAULT VALUES`)
	require.NoError(t, target.QueryRowContext(ctx, `SELECT shared_first, shared_second, custom_value FROM sequence_cases`).Scan(&targetFirstValue, &targetSecondValue, &targetCustomValue))
	require.Equal(t, int64(1), targetFirstValue)
	require.Equal(t, int64(1), targetSecondValue)
	require.Equal(t, int64(1), targetCustomValue)
}

// dumpMustExec runs statement against database and fails the test on error.
func dumpMustExec(t *testing.T, ctx context.Context, database *sql.DB, statement string) {
	t.Helper()
	_, err := database.ExecContext(ctx, statement)
	require.NoError(t, err, statement)
}

// dumpApplySQLFiles applies every file's SQL against database, in the order
// files was built in -- the same order a dump wrote them in, and the order
// -format schema and -format migration both promise replays.
func dumpApplySQLFiles(t *testing.T, ctx context.Context, database *sql.DB, files []dumpFile) {
	t.Helper()
	for _, f := range files {
		_, err := database.ExecContext(ctx, f.SQL)
		require.NoErrorf(t, err, "replay %s:\n%s", f.Name, f.SQL)
	}
}

func TestDumpPostgreSQLRoundTripsAgainstLiveDatabase(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE teams (
		id BIGSERIAL PRIMARY KEY,
		name VARCHAR(120) NOT NULL,
		UNIQUE (name)
	)`)
	dumpMustExec(t, ctx, source, `CREATE TABLE members (
		id BIGSERIAL PRIMARY KEY,
		team_id BIGINT NOT NULL REFERENCES teams(id) ON DELETE CASCADE ON UPDATE RESTRICT,
		nickname VARCHAR(60) NOT NULL,
		balance NUMERIC(12,2) NOT NULL DEFAULT 0 CHECK (balance >= 0),
		UNIQUE (team_id, nickname)
	)`)
	dumpMustExec(t, ctx, source, `CREATE INDEX members_team_id_idx ON members (team_id)`)
	dumpMustExec(t, ctx, source, `CREATE TABLE audits (
		id BIGSERIAL PRIMARY KEY,
		member_id BIGINT REFERENCES members(id) ON DELETE SET NULL,
		note TEXT
	)`)

	files, err := dumpFilesFromDatabase(ctx, dialect.PostgreSQL(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)
	require.Len(t, files, 3)

	sourceTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.PostgreSQL()})
	require.NoError(t, err)

	t.Run("replay", func(t *testing.T) {
		target := dbtest.PostgreSQLDB(t)
		dumpApplySQLFiles(t, ctx, target, files)
		targetTables, err := catalog.FromDatabase(ctx, target, catalog.Options{Dialect: dialect.PostgreSQL()})
		require.NoError(t, err)
		require.Equal(t, sourceTables, targetTables, "replayed descriptors must match the source descriptors")
	})
}

// TestDumpPostgreSQLSerialColumnReplaysAsBigserial pins the sequence
// rewrite from CLAUDE.md design section 4.1: the rewritten BIGSERIAL column
// replays and keeps its sequence, and the UNREWRITTEN render.CreateTable
// output for the same descriptor is what the rewrite exists to avoid --
// PostgreSQL answers SQLSTATE 42P01 ("relation ... does not exist") for it,
// because the dumped DEFAULT nextval(...) names a sequence this dump never
// creates.
func TestDumpPostgreSQLSerialColumnReplaysAsBigserial(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE teams (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL)`)

	files, err := dumpFilesFromDatabase(ctx, dialect.PostgreSQL(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Contains(t, files[0].SQL, "BIGSERIAL")

	t.Run("rewritten replays and keeps its sequence", func(t *testing.T) {
		target := dbtest.PostgreSQLDB(t)
		dumpApplySQLFiles(t, ctx, target, files)
		sourceState := postgresSequenceStateFor(t, ctx, source, "teams", "id")
		targetState := postgresSequenceStateFor(t, ctx, target, "teams", "id")
		require.NotEqual(t, sourceState.OID, targetState.OID)
		sourceState.OID = 0
		targetState.OID = 0
		require.Equal(t, sourceState, targetState)
		require.Equal(t, "public", targetState.Schema)
		require.Equal(t, "teams_id_seq", targetState.Name)
		require.Equal(t, int64(1), targetState.ReferencingDefaults)
		require.True(t, targetState.Owned)
		var sourceValue, targetValue int64
		dumpMustExec(t, ctx, source, `INSERT INTO teams (name) VALUES ('source')`)
		dumpMustExec(t, ctx, target, `INSERT INTO teams (name) VALUES ('source')`)
		require.NoError(t, source.QueryRowContext(ctx, `SELECT id FROM teams`).Scan(&sourceValue))
		require.NoError(t, target.QueryRowContext(ctx, `SELECT id FROM teams`).Scan(&targetValue))
		require.Equal(t, sourceValue, targetValue)
		require.Equal(t, int64(1), targetValue)
	})

	t.Run("unrewritten fails with SQLSTATE 42P01", func(t *testing.T) {
		sourceTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.PostgreSQL()})
		require.NoError(t, err)
		require.Len(t, sourceTables, 1)
		statement, err := render.CreateTable(dialect.PostgreSQL(), sourceTables[0])
		require.NoError(t, err)
		require.Contains(t, statement.SQL(), "nextval")

		target := dbtest.PostgreSQLDB(t)
		_, err = target.ExecContext(ctx, statement.SQL())
		require.Error(t, err)
		var pgErr *pgconn.PgError
		require.True(t, errors.As(err, &pgErr), "expected a *pgconn.PgError, got %T: %v", err, err)
		require.Equal(t, "42P01", pgErr.Code, "the render-time sequence text names a sequence this run never created")
	})
}

// TestDumpPostgreSQLRoundTripsIdentityColumn proves that a PostgreSQL
// identity column with a default sequence now dumps and replays, rather
// than refusing the run: render now emits GENERATED BY DEFAULT AS
// IDENTITY, so applyDumpGuards' PostgreSQL branch only refuses a
// non-default sequence (see TestDumpPostgreSQLRefusesNonDefaultIdentitySequence),
// never an identity column outright.
func TestDumpPostgreSQLRoundTripsIdentityColumn(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE teams (id BIGINT GENERATED BY DEFAULT AS IDENTITY PRIMARY KEY, name TEXT NOT NULL)`)

	files, err := dumpFilesFromDatabase(ctx, dialect.PostgreSQL(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Contains(t, files[0].SQL, "GENERATED BY DEFAULT AS IDENTITY")

	sourceTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.PostgreSQL()})
	require.NoError(t, err)

	t.Run("replay", func(t *testing.T) {
		target := dbtest.PostgreSQLDB(t)
		dumpApplySQLFiles(t, ctx, target, files)
		targetTables, err := catalog.FromDatabase(ctx, target, catalog.Options{Dialect: dialect.PostgreSQL()})
		require.NoError(t, err)
		require.Equal(t, sourceTables, targetTables, "replayed descriptors must match the source descriptors")
	})
}

// TestDumpPostgreSQLRefusesNonDefaultIdentitySequence proves that an
// identity column whose sequence departs from bigint's own defaults (start
// 1, increment 1, minimum 1, maximum 9223372036854775807, cycle NO) still
// refuses the run by name: render emits a bare GENERATED ... AS IDENTITY
// clause with no START WITH or INCREMENT BY, so a START WITH 100 INCREMENT
// BY 5 identity column would otherwise be silently dropped on replay.
func TestDumpPostgreSQLRefusesNonDefaultIdentitySequence(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE teams (id BIGINT GENERATED ALWAYS AS IDENTITY (START WITH 100 INCREMENT BY 5) PRIMARY KEY, name TEXT NOT NULL)`)

	_, err := dumpFilesFromDatabase(ctx, dialect.PostgreSQL(), source, dumpOptions{Format: "schema"})
	require.ErrorContains(t, err, `table "teams" column "id" is an identity column with a non-default sequence`)
}

// TestDumpPostgreSQLRoundTripsWalkthroughSchema dumps and replays the
// members/projects/tasks schema the identity-and-predicates design names
// directly: three identity primary keys, foreign keys carrying both
// referential actions render supports (CASCADE and NO ACTION), a boolean
// DEFAULT, a timestamptz DEFAULT now(), and a partial index. Every one of
// those facts must survive the round trip, not merely the identity columns
// this file's other tests already cover in isolation.
func TestDumpPostgreSQLRoundTripsWalkthroughSchema(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE members (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		name text NOT NULL
	)`)
	dumpMustExec(t, ctx, source, `CREATE TABLE projects (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		name text NOT NULL
	)`)
	dumpMustExec(t, ctx, source, `CREATE TABLE tasks (
		id bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
		project_id bigint NOT NULL REFERENCES projects (id) ON DELETE CASCADE,
		assignee_id bigint NOT NULL REFERENCES members (id) ON DELETE NO ACTION,
		title text NOT NULL,
		is_open boolean NOT NULL DEFAULT true,
		created_at timestamptz NOT NULL DEFAULT now()
	)`)
	dumpMustExec(t, ctx, source, `CREATE INDEX tasks_open_by_project ON tasks (project_id, id) WHERE is_open`)

	files, err := dumpFilesFromDatabase(ctx, dialect.PostgreSQL(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)
	require.Len(t, files, 3)

	sourceTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.PostgreSQL()})
	require.NoError(t, err)

	t.Run("replay", func(t *testing.T) {
		target := dbtest.PostgreSQLDB(t)
		dumpApplySQLFiles(t, ctx, target, files)
		targetTables, err := catalog.FromDatabase(ctx, target, catalog.Options{Dialect: dialect.PostgreSQL()})
		require.NoError(t, err)
		require.Equal(t, sourceTables, targetTables, "replayed descriptors must match the source descriptors")
	})
}

// TestDumpPostgreSQLMigrationFormatApplies drives -format migration through
// internal/migrationdir and migrate.Runner exactly as a user would: load the
// directory a dump wrote, apply it, and require every migration reports
// applied and the resulting schema matches the source.
func TestDumpPostgreSQLMigrationFormatApplies(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE teams (id BIGSERIAL PRIMARY KEY, name TEXT NOT NULL)`)
	dumpMustExec(t, ctx, source, `CREATE TABLE members (id BIGSERIAL PRIMARY KEY, team_id BIGINT NOT NULL REFERENCES teams(id))`)
	dumpMustExec(t, ctx, source, `CREATE INDEX members_team_id_idx ON members (team_id)`)

	files, err := dumpFilesFromDatabase(ctx, dialect.PostgreSQL(), source, dumpOptions{Format: "migration"})
	require.NoError(t, err)

	root := t.TempDir()
	migrationDirectory := filepath.Join(root, "001_initial")
	require.NoError(t, writeDumpOutput(migrationDirectory, "postgresql", files))

	migrations, err := migrationdir.Load(root)
	require.NoError(t, err)

	t.Run("apply", func(t *testing.T) {
		target := dbtest.PostgreSQLDB(t)
		runner, err := migrate.New(target, dialect.PostgreSQL())
		require.NoError(t, err)
		applied, err := runner.Apply(ctx, migrate.AllPending(), migrations...)
		require.NoError(t, err)
		require.Len(t, applied, 1)

		statuses, err := runner.Status(ctx, migrations...)
		require.NoError(t, err)
		for _, status := range statuses {
			require.Equal(t, migrate.StatusApplied, status.State)
		}

		sourceTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.PostgreSQL()})
		require.NoError(t, err)
		targetTables, err := catalog.FromDatabase(ctx, target, catalog.Options{Dialect: dialect.PostgreSQL(), Exclude: []string{"rasql_schema_migrations"}})
		require.NoError(t, err)
		require.Equal(t, sourceTables, targetTables)
	})
}

// TestDumpPostgreSQLForeignKeyOrderIsDependencyOrder pins CLAUDE.md design
// section 1.9 and section 5 together: catalog.FromDatabase's own sweep order
// is alphabetical (audits, members, teams -- the reverse of what the foreign
// keys require), and the dump's own -format migration filenames must not
// follow that order.
func TestDumpPostgreSQLForeignKeyOrderIsDependencyOrder(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE teams (id BIGSERIAL PRIMARY KEY)`)
	dumpMustExec(t, ctx, source, `CREATE TABLE members (id BIGSERIAL PRIMARY KEY, team_id BIGINT NOT NULL REFERENCES teams(id))`)
	dumpMustExec(t, ctx, source, `CREATE TABLE audits (id BIGSERIAL PRIMARY KEY, member_id BIGINT REFERENCES members(id))`)

	sweptTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.PostgreSQL()})
	require.NoError(t, err)
	require.Equal(t, []string{"audits", "members", "teams"}, dumpTableNamesOf(sweptTables), "catalog.FromDatabase's own sweep order is alphabetical")

	files, err := dumpFilesFromDatabase(ctx, dialect.PostgreSQL(), source, dumpOptions{Format: "migration"})
	require.NoError(t, err)
	require.Equal(t, []string{
		"001_create_teams.up.sql", "001_create_teams.down.sql",
		"002_create_members.up.sql", "002_create_members.down.sql",
		"003_create_audits.up.sql", "003_create_audits.down.sql",
	}, dumpFileNames(files))
}

func dumpTableNamesOf(tables []schema.TableDef) []string {
	names := make([]string, len(tables))
	for i, table := range tables {
		names[i] = table.Name
	}
	return names
}

// TestDumpMySQLRoundTripsAutoIncrementColumn proves that a MySQL
// AUTO_INCREMENT column now dumps and replays, rather than refusing the
// run: render now emits AUTO_INCREMENT, and the render-time keyness check
// refuses any shape MySQL itself would reject, so applyDumpGuards' MySQL
// branch no longer needs a blanket refusal of its own.
func TestDumpMySQLRoundTripsAutoIncrementColumn(t *testing.T) {
	ctx := t.Context()
	source := dbtest.MySQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE teams (id BIGINT AUTO_INCREMENT PRIMARY KEY, name VARCHAR(120) NOT NULL)`)

	files, err := dumpFilesFromDatabase(ctx, dialect.MySQL(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Contains(t, files[0].SQL, "AUTO_INCREMENT")

	sourceTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.MySQL()})
	require.NoError(t, err)

	t.Run("replay", func(t *testing.T) {
		target := dbtest.MySQLDB(t)
		dumpApplySQLFiles(t, ctx, target, files)
		targetTables, err := catalog.FromDatabase(ctx, target, catalog.Options{Dialect: dialect.MySQL()})
		require.NoError(t, err)
		require.Equal(t, sourceTables, targetTables, "replayed descriptors must match the source descriptors")
	})
}

// TestDumpMySQLUniqueConstraintDumpsOnce pins CLAUDE.md design section 4.5
// and 1.4 together: a table with a UNIQUE KEY and no AUTO_INCREMENT dumps
// one unique clause and replays, while the unguarded pair -- the CREATE
// TABLE plus the duplicate CREATE UNIQUE INDEX MySQL's own inspector
// reports -- makes MySQL answer error 1061, pinned through
// *mysql.MySQLError.Number rather than matched against the message text.
func TestDumpMySQLUniqueConstraintDumpsOnce(t *testing.T) {
	ctx := t.Context()
	source := dbtest.MySQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE teams (id BIGINT PRIMARY KEY, name VARCHAR(120) NOT NULL, UNIQUE KEY teams_name_key (name))`)

	files, err := dumpFilesFromDatabase(ctx, dialect.MySQL(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)
	require.Len(t, files, 1)
	require.Equal(t, 1, dumpCountOccurrences(files[0].SQL, "UNIQUE"), "exactly one UNIQUE clause: the table-level constraint, with no duplicate CREATE UNIQUE INDEX")

	t.Run("dumped text replays", func(t *testing.T) {
		target := dbtest.MySQLDB(t)
		dumpApplySQLFiles(t, ctx, target, files)
	})

	t.Run("unguarded pair fails with error 1061", func(t *testing.T) {
		sourceTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.MySQL()})
		require.NoError(t, err)
		require.Len(t, sourceTables, 1)
		require.NotEmpty(t, sourceTables[0].Indexes, "MySQL backs the UNIQUE KEY with an index too")

		createStatement, err := render.CreateTable(dialect.MySQL(), sourceTables[0])
		require.NoError(t, err)
		indexStatements, err := render.CreateIndexes(dialect.MySQL(), sourceTables[0])
		require.NoError(t, err)
		require.Len(t, indexStatements, 1)

		target := dbtest.MySQLDB(t)
		dumpMustExec(t, ctx, target, createStatement.SQL())
		_, err = target.ExecContext(ctx, indexStatements[0].SQL())
		require.Error(t, err)
		var mysqlErr *gomysql.MySQLError
		require.True(t, errors.As(err, &mysqlErr), "expected a *mysql.MySQLError, got %T: %v", err, err)
		require.EqualValues(t, 1061, mysqlErr.Number, "duplicate key name, the error the dedup guard exists to pre-empt")
	})
}

// TestDumpMySQLRefusesUnquotedStringDefault pins CLAUDE.md design section
// 4.4 and 1.3 together: rasql refuses to re-emit a MySQL string default it
// cannot safely quote, and the verbatim, unquoted DEFAULT text MySQL's own
// information_schema reports makes MySQL answer error 1064, pinned through
// *mysql.MySQLError.Number.
func TestDumpMySQLRefusesUnquotedStringDefault(t *testing.T) {
	ctx := t.Context()
	source := dbtest.MySQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE members (id BIGINT PRIMARY KEY, role VARCHAR(32) NOT NULL DEFAULT 'member')`)

	_, err := dumpFilesFromDatabase(ctx, dialect.MySQL(), source, dumpOptions{Format: "schema"})
	require.ErrorContains(t, err, `table "members" column "role" has default "member"`)

	t.Run("verbatim unquoted default fails with error 1064", func(t *testing.T) {
		target := dbtest.MySQLDB(t)
		_, err := target.ExecContext(ctx, "CREATE TABLE members (id BIGINT PRIMARY KEY, role VARCHAR(32) NOT NULL DEFAULT member)")
		require.Error(t, err)
		var mysqlErr *gomysql.MySQLError
		require.True(t, errors.As(err, &mysqlErr), "expected a *mysql.MySQLError, got %T: %v", err, err)
		require.EqualValues(t, 1064, mysqlErr.Number, "the syntax error the default whitelist exists to pre-empt")
	})
}

// TestDumpPostgreSQLRefusesLossyColumnTypes pins the live catalog's own
// data_type spellings behind CLAUDE.md's column type gate: one one-column
// table per declared type this repository's own probe found
// render.CreateTable flattening, and the dump must refuse and name every
// one of them. Each declared type gets its own table because the gate
// reports only the first offending column per table (see
// firstTypeViolation), by design, so naming every table is how this test
// observes every declared type's own live spelling in one run.
func TestDumpPostgreSQLRefusesLossyColumnTypes(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	lossyColumns := []struct{ table, declaredType string }{
		{table: "t_smallint", declaredType: "smallint"},
		{table: "t_integer", declaredType: "integer"},
		{table: "t_real", declaredType: "real"},
		{table: "t_timestamp", declaredType: "timestamp"},
		{table: "t_date", declaredType: "date"},
		{table: "t_time", declaredType: "time"},
		{table: "t_json", declaredType: "json"},
	}
	for _, c := range lossyColumns {
		dumpMustExec(t, ctx, source, fmt.Sprintf(`CREATE TABLE %s (id BIGINT PRIMARY KEY, value %s)`, c.table, c.declaredType))
	}

	_, err := dumpFilesFromDatabase(ctx, dialect.PostgreSQL(), source, dumpOptions{Format: "schema"})
	require.Error(t, err)
	for _, c := range lossyColumns {
		require.ErrorContains(t, err, fmt.Sprintf(`table %q column "value"`, c.table))
	}
}

// TestDumpPostgreSQLCapturesFaithfulColumnTypes dumps a table built only
// from the column type gate's allowed list, plus a BIGSERIAL key, replays
// it into a second fresh database, and compares the two databases' own
// information_schema.columns rows -- data_type, character_maximum_length,
// numeric_precision, and numeric_scale -- rather than rasql descriptors,
// which already flatten identically on both sides and would agree even if
// the two databases disagreed. This is the assertion that proves the gate
// captures what it claims to.
func TestDumpPostgreSQLCapturesFaithfulColumnTypes(t *testing.T) {
	ctx := t.Context()
	source := dbtest.PostgreSQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE widgets (
		id BIGSERIAL PRIMARY KEY,
		active BOOLEAN NOT NULL,
		label TEXT NOT NULL,
		code VARCHAR(12) NOT NULL,
		flag CHAR(1) NOT NULL,
		price NUMERIC(10,2) NOT NULL,
		weight DOUBLE PRECISION NOT NULL,
		seen_at TIMESTAMPTZ NOT NULL,
		payload BYTEA NOT NULL,
		external_id UUID NOT NULL,
		attributes JSONB NOT NULL
	)`)

	files, err := dumpFilesFromDatabase(ctx, dialect.PostgreSQL(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)
	require.Len(t, files, 1)

	t.Run("replay", func(t *testing.T) {
		target := dbtest.PostgreSQLDB(t)
		dumpApplySQLFiles(t, ctx, target, files)
		sourceColumns := dumpPostgreSQLColumnTypeRows(t, ctx, source, "widgets")
		targetColumns := dumpPostgreSQLColumnTypeRows(t, ctx, target, "widgets")
		require.NotEmpty(t, sourceColumns)
		require.Equal(t, sourceColumns, targetColumns)
	})
}

type dumpPostgreSQLColumnTypeRow struct {
	Name         string
	DataType     string
	CharMaxLen   sql.NullInt64
	NumPrecision sql.NullInt64
	NumScale     sql.NullInt64
}

func dumpPostgreSQLColumnTypeRows(t *testing.T, ctx context.Context, database *sql.DB, tableName string) []dumpPostgreSQLColumnTypeRow {
	t.Helper()
	rows, err := database.QueryContext(ctx,
		`SELECT column_name, data_type, character_maximum_length, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position`,
		tableName)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var result []dumpPostgreSQLColumnTypeRow
	for rows.Next() {
		var row dumpPostgreSQLColumnTypeRow
		require.NoError(t, rows.Scan(&row.Name, &row.DataType, &row.CharMaxLen, &row.NumPrecision, &row.NumScale))
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	return result
}

// TestDumpMySQLRefusesLossyColumnTypes is
// TestDumpPostgreSQLRefusesLossyColumnTypes's MySQL counterpart, pinning
// the live column_type spellings MySQL 8.4 itself reports for the
// declared types this repository's own probe found flattened.
func TestDumpMySQLRefusesLossyColumnTypes(t *testing.T) {
	ctx := t.Context()
	source := dbtest.MySQLDB(t)
	lossyColumns := []struct{ table, declaredType string }{
		{table: "t_tinyint", declaredType: "TINYINT"},
		{table: "t_smallint", declaredType: "SMALLINT"},
		{table: "t_mediumint", declaredType: "MEDIUMINT"},
		{table: "t_int", declaredType: "INT"},
		{table: "t_float", declaredType: "FLOAT"},
		{table: "t_date", declaredType: "DATE"},
		{table: "t_time", declaredType: "TIME"},
	}
	for _, c := range lossyColumns {
		dumpMustExec(t, ctx, source, fmt.Sprintf("CREATE TABLE %s (id BIGINT PRIMARY KEY, value %s)", c.table, c.declaredType))
	}

	_, err := dumpFilesFromDatabase(ctx, dialect.MySQL(), source, dumpOptions{Format: "schema"})
	require.Error(t, err)
	for _, c := range lossyColumns {
		require.ErrorContains(t, err, fmt.Sprintf(`table %q column "value"`, c.table))
	}
}

// TestDumpMySQLCapturesFaithfulColumnTypes is
// TestDumpPostgreSQLCapturesFaithfulColumnTypes's MySQL counterpart,
// including a BOOLEAN column so the tinyint(1) special case is pinned
// against the real server, not just the fixture test in dump_test.go.
func TestDumpMySQLCapturesFaithfulColumnTypes(t *testing.T) {
	ctx := t.Context()
	source := dbtest.MySQLDB(t)
	dumpMustExec(t, ctx, source, `CREATE TABLE widgets (
		id BIGINT PRIMARY KEY,
		active BOOLEAN NOT NULL,
		label TEXT NOT NULL,
		code VARCHAR(12) NOT NULL,
		flag CHAR(1) NOT NULL,
		price DECIMAL(10,2) NOT NULL,
		weight DOUBLE NOT NULL,
		seen_at DATETIME NOT NULL,
		payload BLOB NOT NULL,
		attributes JSON NOT NULL
	)`)

	files, err := dumpFilesFromDatabase(ctx, dialect.MySQL(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)
	require.Len(t, files, 1)

	t.Run("replay", func(t *testing.T) {
		target := dbtest.MySQLDB(t)
		dumpApplySQLFiles(t, ctx, target, files)
		sourceColumns := dumpMySQLColumnTypeRows(t, ctx, source, "widgets")
		targetColumns := dumpMySQLColumnTypeRows(t, ctx, target, "widgets")
		require.NotEmpty(t, sourceColumns)
		require.Equal(t, sourceColumns, targetColumns)
	})
}

func dumpMySQLColumnTypeRows(t *testing.T, ctx context.Context, database *sql.DB, tableName string) []string {
	t.Helper()
	rows, err := database.QueryContext(ctx,
		`SELECT column_type FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position`,
		tableName)
	require.NoError(t, err)
	defer func() { _ = rows.Close() }()
	var result []string
	for rows.Next() {
		var columnType string
		require.NoError(t, rows.Scan(&columnType))
		result = append(result, columnType)
	}
	require.NoError(t, rows.Err())
	return result
}

func dumpCountOccurrences(haystack, needle string) int {
	count := 0
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			count++
		}
	}
	return count
}
