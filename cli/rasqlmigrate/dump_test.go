package rasqlmigrate

// dump_test.go is package rasqlmigrate (internal), not rasqlmigrate_test,
// unlike most of this repository's other packages. rasqlmigrate_test.go
// already established that convention for this package -- diff-live's own
// tests reach the unexported openDatabase swap the same way -- and most of
// what dump.go needs covered (orderTablesByDependency, the PostgreSQL
// serial rewrite, the MySQL guards, the file-layout builders) is unexported
// on purpose, per CLAUDE.md's instruction not to add a new exported API to
// this package just for tests.

import (
	"database/sql"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/migrationdir"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

// dumpTestTable builds a minimal valid TableDef named name with a single
// bigint primary key column "id" and, when references is non-empty, a
// foreign key from "id" to references' "id" column.
func dumpTestTable(schemaName, name string, references ...string) schema.TableDef {
	table := schema.TableDef{
		Schema:     schemaName,
		Name:       name,
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		PrimaryKey: []string{"id"},
	}
	for _, referenced := range references {
		table.ForeignKeys = append(table.ForeignKeys, schema.ForeignKeyDef{
			Name:              name + "_" + referenced + "_fkey",
			Columns:           []string{"id"},
			ReferencedTable:   referenced,
			ReferencedColumns: []string{"id"},
		})
	}
	return table
}

func TestRunDumpFlagValidation(t *testing.T) {
	testCases := []struct {
		name     string
		args     []string
		expected string
	}{
		{
			name:     "missing dialect",
			args:     []string{"dump", "-dsn", "irrelevant"},
			expected: "dump requires -dialect and -dsn",
		},
		{
			name:     "missing dsn",
			args:     []string{"dump", "-dialect", "sqlite"},
			expected: "dump requires -dialect and -dsn",
		},
		{
			name:     "unknown dialect",
			args:     []string{"dump", "-dialect", "oracle", "-dsn", "irrelevant"},
			expected: `unsupported migration dialect "oracle"`,
		},
		{
			name:     "unknown format",
			args:     []string{"dump", "-dialect", "sqlite", "-dsn", "irrelevant", "-format", "json"},
			expected: `dump: unsupported -format "json"; want schema or migration`,
		},
		{
			name:     "non-positive timeout",
			args:     []string{"dump", "-dialect", "sqlite", "-dsn", "irrelevant", "-timeout", "0s"},
			expected: "dump: -timeout 0s must be positive",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			setCommandOutput(t)
			err := run(testCase.args)
			require.ErrorContains(t, err, testCase.expected)
		})
	}
}

// TestRunDumpRejectsTableAndExcludeTogether reaches catalog's own combined
// validation, which only runs once a database connection is open, so this
// drives it through an ordinary SQLite database rather than a flag-only
// check.
func TestRunDumpRejectsTableAndExcludeTogether(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (id INTEGER PRIMARY KEY)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	setCommandOutput(t)
	err = run([]string{"dump", "-dialect", "sqlite", "-dsn", dsn, "-table", "members", "-exclude", "other"})
	require.ErrorContains(t, err, "options.Include and options.Exclude must not both be set")
}

func TestOrderTablesByDependencyOrdersByForeignKey(t *testing.T) {
	tables := []schema.TableDef{
		dumpTestTable("", "audits", "members"),
		dumpTestTable("", "members", "teams"),
		dumpTestTable("", "teams"),
	}
	ordered, err := orderTablesByDependency(tables)
	require.NoError(t, err)
	require.Equal(t, []string{"teams", "members", "audits"}, tableNames(ordered))
}

func TestOrderTablesByDependencySelfReferenceIsNotADependency(t *testing.T) {
	tables := []schema.TableDef{
		dumpTestTable("", "categories", "categories"),
		dumpTestTable("", "teams"),
	}
	ordered, err := orderTablesByDependency(tables)
	require.NoError(t, err)
	require.Equal(t, []string{"categories", "teams"}, tableNames(ordered))
}

func TestOrderTablesByDependencyReferenceOutsideDumpIsNotADependency(t *testing.T) {
	tables := []schema.TableDef{
		dumpTestTable("", "members", "teams"),
	}
	ordered, err := orderTablesByDependency(tables)
	require.NoError(t, err)
	require.Equal(t, []string{"members"}, tableNames(ordered))
}

func TestOrderTablesByDependencyCycleRefuses(t *testing.T) {
	tables := []schema.TableDef{
		dumpTestTable("", "a", "b"),
		dumpTestTable("", "b", "a"),
	}
	_, err := orderTablesByDependency(tables)
	require.ErrorContains(t, err, `tables "a" and "b" reference each other`)
	require.ErrorContains(t, err, "write this migration by hand")
}

func tableNames(tables []schema.TableDef) []string {
	names := make([]string, len(tables))
	for i, table := range tables {
		names[i] = table.Name
	}
	return names
}

func TestBuildSchemaFormatFilesLayout(t *testing.T) {
	members := schema.TableDef{
		Name: "members",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "team_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{
			{Name: "members_team_id_idx", Columns: []string{"team_id"}},
		},
	}
	audit := schema.TableDef{Schema: "audit", Name: "events", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}}

	files, err := buildSchemaFormatFiles(dialect.PostgreSQL(), []schema.TableDef{members, audit})
	require.NoError(t, err)
	require.Len(t, files, 2)

	require.Equal(t, "members.sql", files[0].Name)
	require.Contains(t, files[0].SQL, `CREATE TABLE "members"`)
	require.Contains(t, files[0].SQL, "\n\nCREATE INDEX \"members_team_id_idx\" ON \"members\" (\"team_id\");\n")

	require.Equal(t, "audit__events.sql", files[1].Name, "a schema-qualified table is named <schema>__<table>")
}

func TestBuildMigrationFormatFilesLayout(t *testing.T) {
	teams := schema.TableDef{Name: "teams", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}, PrimaryKey: []string{"id"}}
	members := schema.TableDef{
		Name: "members",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "team_id", Type: schema.IntegerType{}},
		},
		PrimaryKey: []string{"id"},
		Indexes: []schema.IndexDef{
			{Name: "members_team_id_idx", Columns: []string{"team_id"}},
		},
	}

	files, err := buildMigrationFormatFiles(dialect.PostgreSQL(), []schema.TableDef{teams, members})
	require.NoError(t, err)

	require.Equal(t, []string{
		"001_create_teams.up.sql",
		"001_create_teams.down.sql",
		"002_create_members.up.sql",
		"002_create_members.down.sql",
		"003_create_index_members_team_id_idx.up.sql",
	}, dumpFileNames(files), "index steps are numbered after every table step, with no .down.sql")

	root := t.TempDir()
	migrationDirectory := filepath.Join(root, "001_initial")
	require.NoError(t, os.MkdirAll(migrationDirectory, 0o700))
	for _, f := range files {
		require.NoError(t, os.WriteFile(filepath.Join(migrationDirectory, f.Name), []byte(f.SQL), 0o600))
	}
	loaded, err := migrationdir.Load(root)
	require.NoError(t, err, "migrationdir.Load must accept the directory a dump writes")
	require.Len(t, loaded, 1)
	require.Len(t, loaded[0].Statements, 3, "two CREATE TABLE statements and one CREATE INDEX statement")
	require.Len(t, loaded[0].Down, 2, "only the two CREATE TABLE steps have a reverse source")
}

func dumpFileNames(files []dumpFile) []string {
	names := make([]string, len(files))
	for i, f := range files {
		names[i] = f.Name
	}
	return names
}

func TestRunDumpPreviewWritesNothingToDisk(t *testing.T) {
	dsn := filepath.Join(t.TempDir(), "application.db")
	database, err := sql.Open("sqlite", dsn)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE members (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	require.NoError(t, err)
	require.NoError(t, database.Close())

	outputBuffer := setCommandOutput(t)
	require.NoError(t, run([]string{"dump", "-dialect", "sqlite", "-dsn", dsn}))
	// SQLite inspection reports the table qualified by its "main" schema,
	// so the dumped file is named "main__members.sql", not "members.sql".
	require.Contains(t, outputBuffer.String(), "-- main__members.sql\n")
	require.Contains(t, outputBuffer.String(), `CREATE TABLE "main"."members"`)
}

func TestWriteDumpOutputDirectoryHandling(t *testing.T) {
	files := []dumpFile{{Name: "teams.sql", SQL: "CREATE TABLE teams (id INTEGER);\n"}}

	t.Run("missing directory is created with parents", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "nested", "db", "schema")
		require.NoError(t, writeDumpOutput(target, "sqlite", files))
		contents, err := os.ReadFile(filepath.Join(target, "teams.sql"))
		require.NoError(t, err)
		require.Equal(t, files[0].SQL, string(contents))
	})

	t.Run("empty directory is used", func(t *testing.T) {
		target := t.TempDir()
		require.NoError(t, writeDumpOutput(target, "sqlite", files))
		contents, err := os.ReadFile(filepath.Join(target, "teams.sql"))
		require.NoError(t, err)
		require.Equal(t, files[0].SQL, string(contents))
	})

	t.Run("non-empty directory refuses and is left untouched", func(t *testing.T) {
		target := t.TempDir()
		require.NoError(t, os.WriteFile(filepath.Join(target, ".keep"), []byte(""), 0o600))
		err := writeDumpOutput(target, "sqlite", files)
		require.ErrorContains(t, err, `output directory "`+target+`" is not empty`)
		entries, err := os.ReadDir(target)
		require.NoError(t, err)
		require.Len(t, entries, 1)
		require.Equal(t, ".keep", entries[0].Name())
	})
}

func TestRenderPostgreSQLCreateTableRewritesSequenceDefaultToBigserial(t *testing.T) {
	table := schema.TableDef{
		Name: "teams",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}, Default: "nextval('teams_id_seq'::regclass)"},
			{Name: "name", Type: schema.TextType{}, Nullable: false},
		},
		PrimaryKey: []string{"id"},
	}
	sqlText, err := renderPostgreSQLCreateTable(dialect.PostgreSQL(), table)
	require.NoError(t, err)
	require.Contains(t, sqlText, `"id" BIGSERIAL`)
	require.NotContains(t, sqlText, "nextval")
	require.NotContains(t, sqlText, `"id" BIGINT`)
}

func TestRenderPostgreSQLCreateTableSequenceRewriteRefusesWithNoMatch(t *testing.T) {
	// A text column carrying a nextval-shaped default is contrived -- a
	// real catalog never reports one -- but it is exactly what makes the
	// occurrence-count guard observable: the rewrite's search fragment is
	// built for a non-null IntegerType ("BIGINT NOT NULL"), which a TEXT
	// column's rendered definition never contains, so the count is 0
	// instead of 1 and the run must refuse rather than silently skip it.
	table := schema.TableDef{
		Name: "teams",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.TextType{}, Default: "nextval('teams_id_seq'::regclass)"},
		},
	}
	_, err := renderPostgreSQLCreateTable(dialect.PostgreSQL(), table)
	require.ErrorContains(t, err, "expected exactly one occurrence")
}

func TestDedupMySQLUniqueIndexes(t *testing.T) {
	table := schema.TableDef{
		Name: "teams",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.TextType{}},
		},
		PrimaryKey: []string{"id"},
		UniqueConstraints: []schema.UniqueDef{
			{Name: "teams_name_key", Columns: []string{"name"}},
		},
		Indexes: []schema.IndexDef{
			{Name: "teams_name_key", Columns: []string{"name"}, Unique: true},
			{Name: "teams_name_other_idx", Columns: []string{"name"}, Unique: true},
		},
	}
	result := dedupMySQLUniqueIndexes(table)
	require.Len(t, result.Indexes, 1, "the index matching the unique constraint's name and columns is dropped")
	require.Equal(t, "teams_name_other_idx", result.Indexes[0].Name, "an index with the same columns but a different name is kept")
}

func TestIsAcceptableMySQLDefault(t *testing.T) {
	testCases := []struct {
		name     string
		value    string
		accepted bool
	}{
		{name: "integer literal", value: "0", accepted: true},
		{name: "negative integer literal", value: "-1", accepted: true},
		{name: "decimal literal", value: "12.50", accepted: true},
		{name: "null", value: "NULL", accepted: true},
		{name: "true", value: "true", accepted: true},
		{name: "current timestamp", value: "CURRENT_TIMESTAMP", accepted: true},
		{name: "current timestamp with fractional seconds", value: "CURRENT_TIMESTAMP(3)", accepted: true},
		{name: "already quoted text", value: "'member'", accepted: true},
		{name: "unquoted text", value: "member", accepted: false},
		{name: "unquoted text with a space", value: "not a keyword", accepted: false},
		{name: "empty string is not stated as unquoted text here", value: "''", accepted: true},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			require.Equal(t, testCase.accepted, isAcceptableMySQLDefault(testCase.value))
		})
	}
}

func TestRunHelpListsDumpCommand(t *testing.T) {
	outputBuffer := setCommandOutput(t)
	err := run([]string{"-h"})
	require.ErrorIs(t, err, flag.ErrHelp)
	require.Contains(t, outputBuffer.String(), "dump")
}

// TestDumpPostgreSQLTypeAllowed is table-driven over the allowed
// declared-type list from CLAUDE.md's design amendment: every listed
// data_type string passes the gate, and a handful this repository's own
// live-server probe found flattened (see dump_integration_test.go) refuse
// it, alongside an unrecognized string, since the gate is fail-closed.
func TestDumpPostgreSQLTypeAllowed(t *testing.T) {
	for declaredType := range dumpPostgreSQLAllowedTypes {
		t.Run("allowed/"+declaredType, func(t *testing.T) {
			require.True(t, dumpPostgreSQLTypeAllowed(declaredType))
		})
	}
	testCases := []string{"smallint", "integer", "real", "timestamp without time zone", "date", "time without time zone", "json", "made up type"}
	for _, declaredType := range testCases {
		t.Run("refused/"+declaredType, func(t *testing.T) {
			require.False(t, dumpPostgreSQLTypeAllowed(declaredType), "an unrecognized or lossy declared type must refuse, fail-closed")
		})
	}
}

func TestDumpMySQLTypeAllowed(t *testing.T) {
	for baseType := range dumpMySQLAllowedBaseTypes {
		t.Run("allowed/"+baseType, func(t *testing.T) {
			require.True(t, dumpMySQLTypeAllowed(baseType))
		})
	}
	require.True(t, dumpMySQLTypeAllowed("varchar(120)"), "a parenthesized width still matches its base type")
	require.True(t, dumpMySQLTypeAllowed("decimal(12,2)"))

	testCases := []string{"tinyint", "tinyint(4)", "smallint", "mediumint", "int", "float", "date", "time", "made up type"}
	for _, columnType := range testCases {
		t.Run("refused/"+columnType, func(t *testing.T) {
			require.False(t, dumpMySQLTypeAllowed(columnType), "an unrecognized or lossy column_type must refuse, fail-closed")
		})
	}
}

func TestDumpSQLiteTypeAllowed(t *testing.T) {
	for declaredType := range dumpSQLiteAllowedTypes {
		t.Run("allowed/"+declaredType, func(t *testing.T) {
			require.True(t, dumpSQLiteTypeAllowed(declaredType))
			require.True(t, dumpSQLiteTypeAllowed(strings.ToLower(declaredType)), "the comparison is case-insensitive")
		})
	}
	testCases := []string{"VARCHAR(120)", "CHAR(8)", "DOUBLE", "BOOLEAN", "DATETIME", "DATE", "JSON", "made up type"}
	for _, declaredType := range testCases {
		t.Run("refused/"+declaredType, func(t *testing.T) {
			require.False(t, dumpSQLiteTypeAllowed(declaredType), "an unrecognized or lossy declared type must refuse, fail-closed")
		})
	}
}

// TestDumpColumnFactHasDefaultIdentitySequence pins the guard
// applyDumpGuards' PostgreSQL branch uses to decide whether an identity
// column's sequence is safe to dump: render emits a bare
// GENERATED ... AS IDENTITY clause with no START WITH or INCREMENT BY, so
// only bigint's own defaults (start 1, increment 1, minimum 1, maximum
// 9223372036854775807, cycle NO) replay identically. Any one fact stated
// differently, such as a START WITH 100 INCREMENT BY 5 identity column,
// must refuse.
func TestDumpColumnFactHasDefaultIdentitySequence(t *testing.T) {
	defaultSequence := dumpColumnFact{
		IdentityStart:     sql.NullString{String: "1", Valid: true},
		IdentityIncrement: sql.NullString{String: "1", Valid: true},
		IdentityMinimum:   sql.NullString{String: "1", Valid: true},
		IdentityMaximum:   sql.NullString{String: "9223372036854775807", Valid: true},
		IdentityCycle:     sql.NullString{String: "NO", Valid: true},
	}
	require.True(t, defaultSequence.hasDefaultIdentitySequence())

	nonDefaultCases := map[string]dumpColumnFact{
		"start":     {IdentityStart: sql.NullString{String: "100", Valid: true}, IdentityIncrement: defaultSequence.IdentityIncrement, IdentityMinimum: defaultSequence.IdentityMinimum, IdentityMaximum: defaultSequence.IdentityMaximum, IdentityCycle: defaultSequence.IdentityCycle},
		"increment": {IdentityStart: defaultSequence.IdentityStart, IdentityIncrement: sql.NullString{String: "5", Valid: true}, IdentityMinimum: defaultSequence.IdentityMinimum, IdentityMaximum: defaultSequence.IdentityMaximum, IdentityCycle: defaultSequence.IdentityCycle},
		"minimum":   {IdentityStart: defaultSequence.IdentityStart, IdentityIncrement: defaultSequence.IdentityIncrement, IdentityMinimum: sql.NullString{String: "0", Valid: true}, IdentityMaximum: defaultSequence.IdentityMaximum, IdentityCycle: defaultSequence.IdentityCycle},
		"maximum":   {IdentityStart: defaultSequence.IdentityStart, IdentityIncrement: defaultSequence.IdentityIncrement, IdentityMinimum: defaultSequence.IdentityMinimum, IdentityMaximum: sql.NullString{String: "1000", Valid: true}, IdentityCycle: defaultSequence.IdentityCycle},
		"cycle":     {IdentityStart: defaultSequence.IdentityStart, IdentityIncrement: defaultSequence.IdentityIncrement, IdentityMinimum: defaultSequence.IdentityMinimum, IdentityMaximum: defaultSequence.IdentityMaximum, IdentityCycle: sql.NullString{String: "YES", Valid: true}},
		"unset":     {},
	}
	for name, fact := range nonDefaultCases {
		t.Run("refused/"+name, func(t *testing.T) {
			require.False(t, fact.hasDefaultIdentitySequence())
		})
	}
}

func TestFirstTypeViolationAndTypeGateError(t *testing.T) {
	teams := schema.TableDef{
		Name: "teams",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "born_on", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
	}
	members := schema.TableDef{
		Name: "members",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "seen_on", Type: schema.TimeType{}},
		},
		PrimaryKey: []string{"id"},
	}

	teamsFacts := []dumpColumnFact{{Name: "id", DeclaredType: "bigint"}, {Name: "born_on", DeclaredType: "date"}}
	violation, bad := firstTypeViolation(teams, teamsFacts, dumpPostgreSQLTypeAllowed)
	require.True(t, bad)
	require.Equal(t, "born_on", violation.Column)
	require.Equal(t, "date", violation.DeclaredType)

	membersFacts := []dumpColumnFact{{Name: "id", DeclaredType: "bigint"}, {Name: "seen_on", DeclaredType: "time without time zone"}}
	otherViolation, bad := firstTypeViolation(members, membersFacts, dumpPostgreSQLTypeAllowed)
	require.True(t, bad)

	err := typeGateError(dialect.PostgreSQL(), []dumpTypeViolation{violation, otherViolation})
	require.ErrorContains(t, err, `table "teams" column "born_on" is declared "date"`)
	require.ErrorContains(t, err, `which rasql renders as "TIMESTAMPTZ"`)
	require.ErrorContains(t, err, `table "members" column "seen_on" is declared "time without time zone"`)
	require.ErrorContains(t, err, "these tables cannot be dumped")
}

func TestDumpMySQLBaseType(t *testing.T) {
	testCases := []struct {
		columnType string
		base       string
	}{
		{columnType: "bigint", base: "bigint"},
		{columnType: "bigint unsigned", base: "bigint unsigned"},
		{columnType: "varchar(120)", base: "varchar"},
		{columnType: "decimal(12,2)", base: "decimal"},
		{columnType: "tinyint(1)", base: "tinyint(1)"},
		{columnType: "tinyint(4)", base: "tinyint"},
		{columnType: "TINYINT(1)", base: "tinyint(1)"},
	}
	for _, testCase := range testCases {
		t.Run(testCase.columnType, func(t *testing.T) {
			require.Equal(t, testCase.base, dumpMySQLBaseType(testCase.columnType))
		})
	}
}

func TestDumpSQLiteQualifiedPragma(t *testing.T) {
	require.Equal(t, `PRAGMA "main".table_xinfo("members")`, dumpSQLiteQualifiedPragma("main", "table_xinfo", "members"))
}

// TestDumpSQLiteRoundTrips needs no live server: modernc's SQLite runs
// in-process. It sweeps a small SQLite database, dumps it with -format
// schema, replays every file into a second fresh database, and requires
// the descriptors catalog.FromDatabase reports for each side to match --
// the same shape as the PostgreSQL and MySQL live round-trip tests in
// dump_integration_test.go, since SQLite needs no //go:build unix guard or
// internal/dbtest skip to run anywhere.
func TestDumpSQLiteRoundTrips(t *testing.T) {
	ctx := t.Context()
	sourceDSN := filepath.Join(t.TempDir(), "source.db")
	source, err := sql.Open("sqlite", sourceDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = source.Close() })
	_, err = source.ExecContext(ctx, `CREATE TABLE teams (id INTEGER PRIMARY KEY, name TEXT NOT NULL)`)
	require.NoError(t, err)
	_, err = source.ExecContext(ctx, `CREATE TABLE members (
		id INTEGER PRIMARY KEY,
		team_id INTEGER NOT NULL REFERENCES teams(id) ON DELETE CASCADE,
		nickname TEXT NOT NULL
	)`)
	require.NoError(t, err)
	_, err = source.ExecContext(ctx, `CREATE INDEX members_team_id_idx ON members (team_id)`)
	require.NoError(t, err)

	files, err := dumpFilesFromDatabase(ctx, dialect.SQLite(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)
	require.Len(t, files, 2)

	sourceTables, err := catalog.FromDatabase(ctx, source, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)

	targetDSN := filepath.Join(t.TempDir(), "target.db")
	target, err := sql.Open("sqlite", targetDSN)
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })
	for _, f := range files {
		_, err := target.ExecContext(ctx, f.SQL)
		require.NoErrorf(t, err, "replay %s:\n%s", f.Name, f.SQL)
	}
	targetTables, err := catalog.FromDatabase(ctx, target, catalog.Options{Dialect: dialect.SQLite()})
	require.NoError(t, err)
	require.Equal(t, sourceTables, targetTables)
}

// sqliteDeclaredColumnTypes reads every table's declared column types back
// out of a SQLite database through the same PRAGMA the column type gate
// reads, keyed "table.column". It reads the engine's own declared type text
// rather than a schema.ColumnDef, because a descriptor has already
// flattened the declared type by the time catalog returns it: comparing
// descriptors across a dump and its replay would agree even where the two
// databases genuinely differ.
func sqliteDeclaredColumnTypes(t *testing.T, database *sql.DB) map[string]string {
	t.Helper()
	ctx := t.Context()
	tableRows, err := database.QueryContext(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' AND name NOT LIKE 'sqlite_%' ORDER BY name`)
	require.NoError(t, err)
	var tableNames []string
	for tableRows.Next() {
		var name string
		require.NoError(t, tableRows.Scan(&name))
		tableNames = append(tableNames, name)
	}
	require.NoError(t, tableRows.Err())
	require.NoError(t, tableRows.Close())

	declared := make(map[string]string)
	for _, tableName := range tableNames {
		rows, err := database.QueryContext(ctx, dumpSQLiteQualifiedPragma("main", "table_xinfo", tableName))
		require.NoError(t, err)
		for rows.Next() {
			var cid, notNull, primaryKey, hidden int64
			var columnName, declaredType string
			var defaultValue sql.NullString
			require.NoError(t, rows.Scan(&cid, &columnName, &declaredType, &notNull, &defaultValue, &primaryKey, &hidden))
			declared[tableName+"."+columnName] = declaredType
		}
		require.NoError(t, rows.Err())
		require.NoError(t, rows.Close())
	}
	return declared
}

// TestDumpSQLiteCapturesDeclaredColumnTypes requires that replaying a dump
// of a SQLite database whose columns all pass the type gate reproduces the
// engine's own declared column type text exactly, column for column. This
// is the assertion that proves the gate: TestDumpSQLiteRoundTrips compares
// rasql descriptors, which flatten identically on both sides and so would
// still agree if the replayed database had different declared types.
func TestDumpSQLiteCapturesDeclaredColumnTypes(t *testing.T) {
	ctx := t.Context()
	source, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "source.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = source.Close() })
	_, err = source.ExecContext(ctx, `CREATE TABLE teams (
		id INTEGER PRIMARY KEY,
		name TEXT NOT NULL,
		ratio REAL NOT NULL,
		avatar BLOB
	)`)
	require.NoError(t, err)

	files, err := dumpFilesFromDatabase(ctx, dialect.SQLite(), source, dumpOptions{Format: "schema"})
	require.NoError(t, err)

	target, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "target.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = target.Close() })
	for _, f := range files {
		_, err := target.ExecContext(ctx, f.SQL)
		require.NoErrorf(t, err, "replay %s:\n%s", f.Name, f.SQL)
	}

	require.Equal(t, sqliteDeclaredColumnTypes(t, source), sqliteDeclaredColumnTypes(t, target),
		"replaying the dump must reproduce every declared column type SQLite itself reports")
}

// TestDumpSQLiteRefusesLossyColumnTypes requires that a SQLite column whose
// declared type SQLite's own type affinity would collapse -- VARCHAR(n) to
// TEXT here -- refuses the table by name instead of being captured as the
// affinity name. Pinned against the engine rather than a fixture, because
// the claim under test is what SQLite reports back through PRAGMA
// table_xinfo, not what rasql told itself.
func TestDumpSQLiteRefusesLossyColumnTypes(t *testing.T) {
	ctx := t.Context()
	source, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "source.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = source.Close() })
	_, err = source.ExecContext(ctx, `CREATE TABLE teams (id INTEGER PRIMARY KEY, name VARCHAR(120) NOT NULL)`)
	require.NoError(t, err)

	_, err = dumpFilesFromDatabase(ctx, dialect.SQLite(), source, dumpOptions{Format: "schema"})
	require.Error(t, err)
	require.Contains(t, err.Error(), `column "name" is declared "VARCHAR(120)"`)
	require.Contains(t, err.Error(), `rasql renders as "TEXT"`)
}
