package rasqlmigrate

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/lestrrat-go/rasql/catalog"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dsnredact"
	"github.com/lestrrat-go/rasql/migrate/diff"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
)

// runDump implements the "dump" migrate subcommand. It writes rasql's own
// schema descriptor for a live PostgreSQL, MySQL, or SQLite database -- the
// same rendering diff-live already builds for one table, swept over a whole
// database and ordered so the result replays -- rather than a faithful
// pg_dump/mysqldump. It never writes SQL an engine would reject, and it
// refuses instead of silently dropping a fact that changes behavior: a
// PostgreSQL identity column, a MySQL AUTO_INCREMENT column, a MySQL
// default rasql cannot re-emit as SQL, and a foreign-key cycle all refuse
// the run by name rather than being written wrong or dropped.
//
// The column type gate (see applyDumpGuards and dumpPostgreSQLTypeAllowed,
// dumpMySQLTypeAllowed, and dumpSQLiteTypeAllowed) closes the widest such
// gap: rasql's portable schema.ColumnType model cannot state the difference
// between, say, a PostgreSQL "smallint" or "date" and the "BIGINT" or
// "TIMESTAMPTZ" it would render instead, so every declared column type not
// on a short, per-dialect allow-list this repository has verified against a
// live server refuses the table by name, fail-closed, rather than being
// silently flattened into a different column. docs/core/07-migrations.md
// carries the full allow-list.
func runDump(args []string) error {
	flags := newFlagSet("dump")
	dialectName := flags.String("dialect", "", "database dialect; PostgreSQL, MySQL, and SQLite are currently supported")
	dsn := flags.String("dsn", "", "database connection string")
	tableNames := flags.String("table", "", "comma-separated tables to dump instead of every base table")
	excludeNames := flags.String("exclude", "", "comma-separated tables to skip during a sweep")
	historyTable := flags.String("history-table", "", "migration history table a sweep skips; default rasql_schema_migrations")
	format := flags.String("format", "schema", "dump format: schema (one file per table) or migration (a numbered migration directory)")
	outputDirectory := flags.String("output", "", "directory to write; omit to preview")
	timeout := flags.Duration("timeout", 30*time.Second, "limit on the whole run")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *dialectName == "" || *dsn == "" {
		return errors.New("dump requires -dialect and -dsn")
	}
	d, err := migrationDialect(*dialectName)
	if err != nil {
		return err
	}
	if *format != "schema" && *format != "migration" {
		return fmt.Errorf("dump: unsupported -format %q; want schema or migration", *format)
	}
	if *timeout <= 0 {
		return fmt.Errorf("dump: -timeout %s must be positive", *timeout)
	}
	includeTables, err := dumpSplitNames("table", *tableNames)
	if err != nil {
		return err
	}
	excludeTables, err := dumpSplitNames("exclude", *excludeNames)
	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
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

	files, err := dumpFilesFromDatabase(ctx, d, database, dumpOptions{
		Include:      includeTables,
		Exclude:      excludeTables,
		HistoryTable: *historyTable,
		Format:       *format,
	})
	if err != nil {
		return dsnredact.Error(err, *dsn)
	}

	if *outputDirectory == "" {
		writeDumpPreview(commandOutput, files)
		return nil
	}
	if err := writeDumpOutput(*outputDirectory, *dialectName, files); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(commandOutput, "created %s\n", *outputDirectory)
	return nil
}

// dumpOptions selects what dumpFilesFromDatabase describes and how it
// renders the result. It is the DSN-free half of runDump's own flags, kept
// separate so a live test can drive the sweep, the fidelity guards, the
// dependency ordering, and the rendering directly against a *sql.DB
// internal/dbtest already opened, without rebuilding a DSN string from the
// parsed connection config dbtest deliberately never hands back (see
// internal/dbtest/postgresql.go's comment on PostgreSQLConfig).
type dumpOptions struct {
	Include      []string
	Exclude      []string
	HistoryTable string
	// Format is "schema" or "migration".
	Format string
}

// dumpFilesFromDatabase reads database inside one read-only transaction,
// applies the fidelity guards from CLAUDE.md design section 4, orders the
// result per design section 5, and renders it into the files runDump would
// preview or write. It is runDump's own implementation with the -dsn open
// and the output step factored out, so both runDump and a live test share
// one code path for the sweep, the guards, the ordering, and the rendering.
func dumpFilesFromDatabase(ctx context.Context, d dialect.Dialect, database *sql.DB, opts dumpOptions) ([]dumpFile, error) {
	transaction, err := runWithHardDeadline(ctx, func() (*sql.Tx, error) {
		return database.BeginTx(ctx, liveInspectionTxOptions(d.Name()))
	})
	if err != nil {
		return nil, fmt.Errorf("begin dump transaction: %w", err)
	}
	defer func() {
		if ctx.Err() == nil {
			_ = transaction.Rollback()
		}
	}()

	tables, err := runWithHardDeadline(ctx, func() ([]schema.TableDef, error) {
		return catalog.FromQueryer(ctx, transaction, catalog.Options{
			Dialect:      d,
			Include:      opts.Include,
			Exclude:      opts.Exclude,
			HistoryTable: opts.HistoryTable,
		})
	})
	if err != nil {
		return nil, err
	}

	tables, err = applyDumpGuards(ctx, transaction, d, tables)
	if err != nil {
		return nil, err
	}

	ordered, err := orderTablesByDependency(tables)
	if err != nil {
		return nil, err
	}

	if opts.Format == "migration" {
		return buildMigrationFormatFiles(d, ordered)
	}
	return buildSchemaFormatFiles(d, ordered)
}

// dumpSplitNames splits a comma-separated -table/-exclude flag value into
// table names, following the same shape as splitTableNames in
// cli/rasqlgen/generate.go: an empty value is "not stated" (nil, no error),
// and an empty element between commas is a typo that refuses the run rather
// than being silently dropped.
func dumpSplitNames(flagName, value string) ([]string, error) {
	if value == "" {
		return nil, nil
	}
	parts := strings.Split(value, ",")
	names := make([]string, 0, len(parts))
	for _, part := range parts {
		name := strings.TrimSpace(part)
		if name == "" {
			return nil, fmt.Errorf("dump: -%s %q holds an empty table name", flagName, value)
		}
		names = append(names, name)
	}
	return names, nil
}

// dumpFile is one file a dump run would write, relative to the output
// directory: "teams.sql" for -format schema, or
// "001_create_teams.up.sql" for -format migration. SQL always ends with a
// trailing newline, so it can be written to disk or printed to a preview
// stream unchanged.
type dumpFile struct {
	Name string
	SQL  string
}

// applyDumpGuards runs the fidelity guards from CLAUDE.md's design section 4
// against tables, before any file is written, and returns the tables a
// dump may actually render. Every dialect also runs the column type gate
// (see dumpColumnTypeAllowed and typeGateError): a declared column type this
// dump has not verified round-trips through render.CreateTable exactly as
// declared refuses the table by name, fail-closed, rather than being
// silently flattened the way schema.IntegerType already flattens every
// integer width into BIGINT. PostgreSQL tables are otherwise checked but
// never rewritten here; MySQL tables are checked and, when a MySQL index
// merely duplicates a unique constraint, rewritten to drop the duplicate.
func applyDumpGuards(ctx context.Context, transaction *sql.Tx, d dialect.Dialect, tables []schema.TableDef) ([]schema.TableDef, error) {
	switch d.Name() {
	case "postgresql":
		var violations []dumpTypeViolation
		for _, table := range tables {
			facts, err := fetchPostgreSQLColumnFacts(ctx, transaction, table.Name)
			if err != nil {
				return nil, err
			}
			for _, fact := range facts {
				if fact.Identity {
					return nil, fmt.Errorf("dump: table %q column %q is an identity column, which rasql can neither describe nor render; rewrite it as a serial column, or dump the other tables with -table", table.Name, fact.Name)
				}
			}
			if violation, bad := firstTypeViolation(table, facts, dumpPostgreSQLTypeAllowed); bad {
				violations = append(violations, violation)
			}
		}
		if len(violations) > 0 {
			return nil, typeGateError(d, violations)
		}
		return tables, nil
	case "mysql":
		var violations []dumpTypeViolation
		result := make([]schema.TableDef, len(tables))
		for i, table := range tables {
			facts, err := fetchMySQLColumnFacts(ctx, transaction, table.Name)
			if err != nil {
				return nil, err
			}
			for _, fact := range facts {
				if fact.AutoIncrement {
					return nil, fmt.Errorf("dump: table %q column %q is AUTO_INCREMENT, which rasql can neither describe nor render", table.Name, fact.Name)
				}
			}
			if err := checkMySQLColumnDefaults(table); err != nil {
				return nil, err
			}
			if violation, bad := firstTypeViolation(table, facts, dumpMySQLTypeAllowed); bad {
				violations = append(violations, violation)
			}
			result[i] = dedupMySQLUniqueIndexes(table)
		}
		if len(violations) > 0 {
			return nil, typeGateError(d, violations)
		}
		return result, nil
	case "sqlite":
		var violations []dumpTypeViolation
		for _, table := range tables {
			facts, err := fetchSQLiteColumnFacts(ctx, transaction, table)
			if err != nil {
				return nil, err
			}
			if violation, bad := firstTypeViolation(table, facts, dumpSQLiteTypeAllowed); bad {
				violations = append(violations, violation)
			}
		}
		if len(violations) > 0 {
			return nil, typeGateError(d, violations)
		}
		return tables, nil
	default:
		return tables, nil
	}
}

// dumpColumnFact is one column's declared type and generation facts, read
// straight from the engine's own catalog rather than from schema.ColumnDef,
// which already flattened the declared type by the time catalog.FromQueryer
// returns it. Identity is meaningful for PostgreSQL only, and AutoIncrement
// for MySQL only; each dialect's fetch function leaves the other false.
type dumpColumnFact struct {
	Name          string
	DeclaredType  string
	Identity      bool
	AutoIncrement bool
}

// fetchPostgreSQLColumnFacts reads tableName's columns' data_type and
// identity status in one round trip -- the same query section 4.2 always
// needed, extended to also carry the declared type the column type gate
// needs, rather than adding a second round trip for it.
func fetchPostgreSQLColumnFacts(ctx context.Context, transaction *sql.Tx, tableName string) ([]dumpColumnFact, error) {
	rows, err := runWithHardDeadline(ctx, func() (*sql.Rows, error) {
		return transaction.QueryContext(ctx,
			`SELECT column_name, data_type, is_identity FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1`,
			tableName)
	})
	if err != nil {
		return nil, fmt.Errorf("dump: read columns for table %q: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()
	var facts []dumpColumnFact
	for rows.Next() {
		var name, declaredType, isIdentity string
		if err := rows.Scan(&name, &declaredType, &isIdentity); err != nil {
			return nil, fmt.Errorf("dump: read columns for table %q: %w", tableName, err)
		}
		facts = append(facts, dumpColumnFact{Name: name, DeclaredType: declaredType, Identity: isIdentity == "YES"})
	}
	return facts, rows.Err()
}

// fetchMySQLColumnFacts reads tableName's columns' column_type and
// AUTO_INCREMENT status in one round trip, the same way
// fetchPostgreSQLColumnFacts does for PostgreSQL.
func fetchMySQLColumnFacts(ctx context.Context, transaction *sql.Tx, tableName string) ([]dumpColumnFact, error) {
	rows, err := runWithHardDeadline(ctx, func() (*sql.Rows, error) {
		return transaction.QueryContext(ctx,
			`SELECT column_name, column_type, extra FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ?`,
			tableName)
	})
	if err != nil {
		return nil, fmt.Errorf("dump: read columns for table %q: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()
	var facts []dumpColumnFact
	for rows.Next() {
		var name, columnType, extra string
		if err := rows.Scan(&name, &columnType, &extra); err != nil {
			return nil, fmt.Errorf("dump: read columns for table %q: %w", tableName, err)
		}
		facts = append(facts, dumpColumnFact{Name: name, DeclaredType: columnType, AutoIncrement: strings.Contains(strings.ToLower(extra), "auto_increment")})
	}
	return facts, rows.Err()
}

// fetchSQLiteColumnFacts reads table's columns' declared type text from
// PRAGMA table_xinfo, the same source inspect itself reads, schema-qualified
// by table.Schema (SQLite's "main", "temp", or an attached database name;
// "main" when table.Schema is somehow empty).
func fetchSQLiteColumnFacts(ctx context.Context, transaction *sql.Tx, table schema.TableDef) ([]dumpColumnFact, error) {
	databaseName := table.Schema
	if databaseName == "" {
		databaseName = "main"
	}
	query := dumpSQLiteQualifiedPragma(databaseName, "table_xinfo", table.Name)
	rows, err := runWithHardDeadline(ctx, func() (*sql.Rows, error) {
		return transaction.QueryContext(ctx, query)
	})
	if err != nil {
		return nil, fmt.Errorf("dump: read columns for table %q: %w", table.Name, err)
	}
	defer func() { _ = rows.Close() }()
	var facts []dumpColumnFact
	for rows.Next() {
		var cid, notNull, primaryKey, hidden int64
		var name, declaredType string
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &declaredType, &notNull, &defaultValue, &primaryKey, &hidden); err != nil {
			return nil, fmt.Errorf("dump: read columns for table %q: %w", table.Name, err)
		}
		facts = append(facts, dumpColumnFact{Name: name, DeclaredType: declaredType})
	}
	return facts, rows.Err()
}

// dumpSQLiteQualifiedPragma builds a schema-qualified PRAGMA call, the same
// shape inspect's own unexported sqliteQualifiedPragma builds, duplicated
// here since that symbol is unexported in its own package.
func dumpSQLiteQualifiedPragma(databaseName, pragmaName, identifier string) string {
	return `PRAGMA "` + strings.ReplaceAll(databaseName, `"`, `""`) + `".` + pragmaName + `("` + strings.ReplaceAll(identifier, `"`, `""`) + `")`
}

// dumpPostgreSQLAllowedTypes are the exact information_schema.columns
// data_type strings CLAUDE.md's design amendment verified render back
// through render.CreateTable exactly as declared, against a live
// PostgreSQL 17 server. bigint covers a BIGSERIAL/IDENTITY column too,
// since PostgreSQL's own catalog reports both as plain bigint; the
// sequence rewrite in renderPostgreSQLCreateTable runs after this gate
// passes.
var dumpPostgreSQLAllowedTypes = map[string]struct{}{
	"bigint":                   {},
	"boolean":                  {},
	"text":                     {},
	"character varying":        {},
	"character":                {},
	"numeric":                  {},
	"double precision":         {},
	"timestamp with time zone": {},
	"bytea":                    {},
	"uuid":                     {},
	"jsonb":                    {},
}

func dumpPostgreSQLTypeAllowed(declaredType string) bool {
	_, ok := dumpPostgreSQLAllowedTypes[declaredType]
	return ok
}

// dumpMySQLAllowedBaseTypes are the base MySQL information_schema.columns
// column_type strings (the text before any "(", lowercased, and
// "bigint unsigned" as one whole string) CLAUDE.md's design amendment
// verified round-trip against a live MySQL 8.4 server. "tinyint(1)" is
// matched as one exact string rather than reduced to "tinyint", since that
// is how MySQL stores a column declared BOOLEAN and the only tinyint width
// this dump accepts.
var dumpMySQLAllowedBaseTypes = map[string]struct{}{
	"bigint":          {},
	"bigint unsigned": {},
	"text":            {},
	"varchar":         {},
	"char":            {},
	"decimal":         {},
	"double":          {},
	"datetime":        {},
	"blob":            {},
	"json":            {},
	"tinyint(1)":      {},
}

func dumpMySQLTypeAllowed(columnType string) bool {
	_, ok := dumpMySQLAllowedBaseTypes[dumpMySQLBaseType(columnType)]
	return ok
}

// dumpMySQLBaseType strips a column_type's precision/scale/display-width
// parenthesized suffix, lowercased, except for the exact string
// "tinyint(1)" which is left whole -- see dumpMySQLAllowedBaseTypes.
func dumpMySQLBaseType(columnType string) string {
	lower := strings.ToLower(strings.TrimSpace(columnType))
	if lower == "tinyint(1)" {
		return lower
	}
	if index := strings.IndexByte(lower, '('); index >= 0 {
		return lower[:index]
	}
	return lower
}

// dumpSQLiteAllowedTypes are the declared type strings, matched
// case-insensitively, CLAUDE.md's design amendment verified inspect and
// render round-trip byte-for-byte against modernc's in-process SQLite.
// Every other declared type SQLite accepts -- VARCHAR(n), DOUBLE, BOOLEAN,
// DATETIME, DATE, and JSON among them -- collapses to one of these four
// through SQLite's own type-affinity rules by the time inspect reads it
// back, and render.CreateTable then writes the affinity name, not the
// original declared text.
var dumpSQLiteAllowedTypes = map[string]struct{}{
	"INTEGER": {},
	"TEXT":    {},
	"REAL":    {},
	"BLOB":    {},
}

func dumpSQLiteTypeAllowed(declaredType string) bool {
	_, ok := dumpSQLiteAllowedTypes[strings.ToUpper(strings.TrimSpace(declaredType))]
	return ok
}

// dumpTypeViolation names one column the type gate refused: table.Column
// still holds the flattened schema.ColumnDef catalog.FromQueryer returned,
// which typeGateError reads to report what rasql would have rendered
// instead of the declared type.
type dumpTypeViolation struct {
	Table        schema.TableDef
	Column       string
	DeclaredType string
}

// firstTypeViolation reports the first column in facts whose DeclaredType
// allowed rejects, so one table refuses on its first offending column
// rather than every one of them.
func firstTypeViolation(table schema.TableDef, facts []dumpColumnFact, allowed func(string) bool) (dumpTypeViolation, bool) {
	for _, fact := range facts {
		if !allowed(fact.DeclaredType) {
			return dumpTypeViolation{Table: table, Column: fact.Name, DeclaredType: fact.DeclaredType}, true
		}
	}
	return dumpTypeViolation{}, false
}

// typeGateError builds the column type gate's refusal, naming every
// offending table rather than stopping at the first, so a developer fixing
// a schema sees the whole list in one run.
func typeGateError(d dialect.Dialect, violations []dumpTypeViolation) error {
	descriptions := make([]string, len(violations))
	for i, violation := range violations {
		descriptions[i] = fmt.Sprintf("table %q column %q is declared %q, which rasql renders as %q",
			violation.Table.Name, violation.Column, violation.DeclaredType, dumpRenderedTypeName(d, violation.Table, violation.Column))
	}
	if len(violations) == 1 {
		return fmt.Errorf("dump: %s; capturing it would build a different column, so this table cannot be dumped", descriptions[0])
	}
	return fmt.Errorf("dump: %s; capturing any of them would build a different column, so these tables cannot be dumped", strings.Join(descriptions, "; "))
}

// dumpRenderedTypeName reports the DDL type name render.CreateTable would
// write for columnName on table, using the dialect's own TypeName rather
// than a hand-maintained mapping. "?" stands in for a lookup failure that
// should never happen for a column the catalog just described.
func dumpRenderedTypeName(d dialect.Dialect, table schema.TableDef, columnName string) string {
	column, ok := table.Column(columnName)
	if !ok {
		return "?"
	}
	typeName, err := d.TypeName(column)
	if err != nil {
		return "?"
	}
	return typeName
}

// mysqlNumericDefaultPattern matches a decimal numeric literal: an optional
// sign, digits, and an optional fractional part.
var mysqlNumericDefaultPattern = regexp.MustCompile(`^[+-]?[0-9]+(\.[0-9]+)?$`)

// mysqlCurrentTimestampDefaultPattern matches CURRENT_TIMESTAMP, optionally
// followed by a parenthesized fractional-seconds argument such as
// CURRENT_TIMESTAMP(3), case-insensitively.
var mysqlCurrentTimestampDefaultPattern = regexp.MustCompile(`(?i)^CURRENT_TIMESTAMP(\([0-9]+\))?$`)

// isAcceptableMySQLDefault reports whether value -- a MySQL
// ColumnDef.Default exactly as information_schema.columns.column_default
// reported it, unquoted even for a string default -- can be re-emitted as a
// SQL DEFAULT expression as written. See CLAUDE.md design section 4.4: the
// whitelist is deliberately short, because a wrong guess either writes a
// file the engine rejects or, worse, one it accepts with a different
// meaning.
func isAcceptableMySQLDefault(value string) bool {
	switch strings.ToUpper(value) {
	case "NULL", "TRUE", "FALSE":
		return true
	}
	if mysqlCurrentTimestampDefaultPattern.MatchString(value) {
		return true
	}
	if mysqlNumericDefaultPattern.MatchString(value) {
		return true
	}
	return len(value) >= 2 && strings.HasPrefix(value, "'") && strings.HasSuffix(value, "'")
}

// checkMySQLColumnDefaults refuses table when a column names a default
// isAcceptableMySQLDefault rejects.
func checkMySQLColumnDefaults(table schema.TableDef) error {
	for _, column := range table.Columns {
		if column.Default == "" {
			continue
		}
		if isAcceptableMySQLDefault(column.Default) {
			continue
		}
		return fmt.Errorf("dump: table %q column %q has default %q, which MySQL reports without quotes and rasql cannot re-emit as SQL", table.Name, column.Name, column.Default)
	}
	return nil
}

// dedupMySQLUniqueIndexes drops an IndexDef from table.Indexes when it is a
// plain unique, non-partial, non-expression index that merely backs a
// UniqueDef already listed in table.UniqueConstraints under the same name
// and column list. See CLAUDE.md design section 4.5.
func dedupMySQLUniqueIndexes(table schema.TableDef) schema.TableDef {
	if len(table.UniqueConstraints) == 0 || len(table.Indexes) == 0 {
		return table
	}
	unique := make(map[string]struct{}, len(table.UniqueConstraints))
	for _, constraint := range table.UniqueConstraints {
		unique[constraint.Name+"\x00"+strings.Join(constraint.Columns, "\x00")] = struct{}{}
	}
	filtered := make([]schema.IndexDef, 0, len(table.Indexes))
	for _, index := range table.Indexes {
		if index.Unique && index.Predicate == "" && len(index.Expressions) == 0 {
			key := index.Name + "\x00" + strings.Join(index.Columns, "\x00")
			if _, duplicate := unique[key]; duplicate {
				continue
			}
		}
		filtered = append(filtered, index)
	}
	table.Indexes = filtered
	return table
}

// tableKey identifies a table by (schema, name), the same pair
// catalog.FromDatabase sorts by and orderTablesByDependency matches foreign
// keys against.
type tableKey struct {
	schema string
	name   string
}

func tableKeyOf(t schema.TableDef) tableKey {
	return tableKey{schema: t.Schema, name: t.Name}
}

// tableLess orders tables by (Schema, Name), the tie-break
// orderTablesByDependency uses so its output is deterministic across runs.
func tableLess(a, b schema.TableDef) bool {
	if a.Schema != b.Schema {
		return a.Schema < b.Schema
	}
	return a.Name < b.Name
}

// orderTablesByDependency topologically sorts tables so that every table
// referencing another table in tables (other than itself) by foreign key
// comes after the table it references, using Kahn's algorithm with ties
// broken by (Schema, Name) so the result is deterministic. A self-reference
// is not a dependency, since the table is created once and its own
// constraint is satisfied within that one CREATE TABLE. A reference to a
// table not in tables is not a dependency either: the dump is a subset by
// the caller's own request (an excluded table, or one in another schema).
// A non-empty remainder after Kahn's algorithm terminates is a foreign-key
// cycle, refused naming the remaining tables sorted by (Schema, Name). See
// CLAUDE.md design section 5.
func orderTablesByDependency(tables []schema.TableDef) ([]schema.TableDef, error) {
	n := len(tables)
	keyIndex := make(map[tableKey]int, n)
	for i, t := range tables {
		keyIndex[tableKeyOf(t)] = i
	}
	dependsOn := make([]map[int]struct{}, n)
	for i, t := range tables {
		dependsOn[i] = make(map[int]struct{})
		for _, fk := range t.ForeignKeys {
			referencedSchema := fk.ReferencedSchema
			if referencedSchema == "" {
				referencedSchema = t.Schema
			}
			j, ok := keyIndex[tableKey{schema: referencedSchema, name: fk.ReferencedTable}]
			if !ok || j == i {
				continue
			}
			dependsOn[i][j] = struct{}{}
		}
	}

	remaining := make(map[int]struct{}, n)
	for i := range tables {
		remaining[i] = struct{}{}
	}
	ordered := make([]schema.TableDef, 0, n)
	for len(remaining) > 0 {
		next := -1
		for i := range remaining {
			ready := true
			for dep := range dependsOn[i] {
				if _, stillRemaining := remaining[dep]; stillRemaining {
					ready = false
					break
				}
			}
			if !ready {
				continue
			}
			if next == -1 || tableLess(tables[i], tables[next]) {
				next = i
			}
		}
		if next == -1 {
			return nil, cycleError(tables, remaining)
		}
		ordered = append(ordered, tables[next])
		delete(remaining, next)
	}
	return ordered, nil
}

// cycleError builds the "reference each other" refusal orderTablesByDependency
// returns when a foreign-key cycle leaves tables in remaining, naming them
// sorted by (Schema, Name).
func cycleError(tables []schema.TableDef, remaining map[int]struct{}) error {
	stuck := make([]schema.TableDef, 0, len(remaining))
	for i := range remaining {
		stuck = append(stuck, tables[i])
	}
	sort.Slice(stuck, func(i, j int) bool { return tableLess(stuck[i], stuck[j]) })
	quoted := make([]string, len(stuck))
	for i, t := range stuck {
		quoted[i] = fmt.Sprintf("%q", t.QualifiedName())
	}
	return fmt.Errorf("dump: tables %s reference each other, so no order of CREATE TABLE statements replays; write this migration by hand", strings.Join(quoted, " and "))
}

// pgSequenceDefaultPattern matches a PostgreSQL sequence-backed default:
// nextval('name'::regclass) or nextval('name'), the two forms
// catalog.FromDatabase's PostgreSQL sweep reports for a BIGSERIAL/IDENTITY
// BY DEFAULT column and a plain integer column wired to a sequence by hand.
var pgSequenceDefaultPattern = regexp.MustCompile(`^nextval\('([^']*)'(?:::regclass)?\)$`)

// renderPostgreSQLCreateTable renders table's CREATE TABLE statement for the
// PostgreSQL dialect, rewriting every column whose Default matches
// pgSequenceDefaultPattern from "BIGINT NOT NULL DEFAULT nextval(...)" to
// "BIGSERIAL", so the statement replays without a CREATE SEQUENCE the dump
// never writes. See CLAUDE.md design section 4.1.
//
// It renders a clone with the matching Default cleared, so the ordinary
// renderer produces the column as "<name> BIGINT NOT NULL", then replaces
// that exact fragment with "<name> BIGSERIAL". The fragment is built from
// the dialect's own quoting and type name rather than guessed, and the
// substitution refuses the run unless it finds exactly one occurrence, so a
// renderer change that moves the text is reported rather than silently
// skipped.
func renderPostgreSQLCreateTable(d dialect.Dialect, table schema.TableDef) (string, error) {
	var serialColumns []string
	clone := table.Clone()
	for i, column := range clone.Columns {
		if pgSequenceDefaultPattern.MatchString(column.Default) {
			clone.Columns[i].Default = ""
			serialColumns = append(serialColumns, column.Name)
		}
	}
	statement, err := render.CreateTable(d, clone)
	if err != nil {
		return "", fmt.Errorf("table %q: %w", table.Name, err)
	}
	sqlText := statement.SQL()
	for _, columnName := range serialColumns {
		quotedName, err := d.QuoteIdentifier(columnName)
		if err != nil {
			return "", fmt.Errorf("table %q: %w", table.Name, err)
		}
		typeName, err := d.TypeName(schema.ColumnDef{Type: schema.IntegerType{}, Nullable: false})
		if err != nil {
			return "", fmt.Errorf("table %q: %w", table.Name, err)
		}
		fragment := quotedName + " " + typeName + " NOT NULL"
		count := strings.Count(sqlText, fragment)
		if count != 1 {
			return "", fmt.Errorf("dump: table %q column %q: expected exactly one occurrence of %q to rewrite as BIGSERIAL, found %d", table.Name, columnName, fragment, count)
		}
		sqlText = strings.Replace(sqlText, fragment, quotedName+" BIGSERIAL", 1)
	}
	return sqlText, nil
}

// renderCreateTableSQL renders table's CREATE TABLE statement text (with no
// trailing ";" or newline), applying renderPostgreSQLCreateTable's sequence
// rewrite on the PostgreSQL dialect and render.CreateTable unchanged on
// every other dialect. A render.CreateTable error is wrapped with the table
// name and returned unchanged (%w), per CLAUDE.md design section 4.7.
func renderCreateTableSQL(d dialect.Dialect, table schema.TableDef) (string, error) {
	if d.Name() == "postgresql" {
		return renderPostgreSQLCreateTable(d, table)
	}
	statement, err := render.CreateTable(d, table)
	if err != nil {
		return "", fmt.Errorf("table %q: %w", table.Name, err)
	}
	return statement.SQL(), nil
}

// renderIndexSQLs renders table's CREATE INDEX statements, in
// table.Indexes order, which inspect already sorts. A render.CreateIndexes
// error is wrapped with the table name and returned unchanged (%w).
func renderIndexSQLs(d dialect.Dialect, table schema.TableDef) ([]string, error) {
	statements, err := render.CreateIndexes(d, table)
	if err != nil {
		return nil, fmt.Errorf("table %q: %w", table.Name, err)
	}
	sqls := make([]string, len(statements))
	for i, statement := range statements {
		sqls[i] = statement.SQL()
	}
	return sqls, nil
}

// dumpFileBaseName returns table's plain name, or "<schema>__<table>" when
// table is schema-qualified, so two tables in different schemas cannot
// collide on one -format schema filename.
func dumpFileBaseName(table schema.TableDef) string {
	if table.Schema == "" {
		return table.Name
	}
	return table.Schema + "__" + table.Name
}

// filenamePart sanitizes value into a lowercase, underscore-separated
// migration filename stem, the same idea filenamePart in
// migrate/diff/postgresql/postgresql.go uses, copied here rather than
// imported since that symbol is unexported in its own package.
func filenamePart(value string) string {
	var result strings.Builder
	previousUnderscore := false
	for _, character := range value {
		if unicode.IsLetter(character) || unicode.IsDigit(character) {
			result.WriteRune(unicode.ToLower(character))
			previousUnderscore = false
			continue
		}
		if !previousUnderscore {
			result.WriteByte('_')
			previousUnderscore = true
		}
	}
	name := strings.Trim(result.String(), "_")
	if name == "" {
		return "object"
	}
	return name
}

// buildSchemaFormatFiles builds one dumpFile per table for -format schema:
// the table's CREATE TABLE statement, then its CREATE INDEX statements, one
// statement per paragraph separated by a blank line, each terminated by ";"
// and a newline.
func buildSchemaFormatFiles(d dialect.Dialect, tables []schema.TableDef) ([]dumpFile, error) {
	files := make([]dumpFile, 0, len(tables))
	for _, table := range tables {
		createSQL, err := renderCreateTableSQL(d, table)
		if err != nil {
			return nil, err
		}
		indexSQLs, err := renderIndexSQLs(d, table)
		if err != nil {
			return nil, err
		}
		parts := make([]string, 0, 1+len(indexSQLs))
		parts = append(parts, createSQL+";")
		for _, indexSQL := range indexSQLs {
			parts = append(parts, indexSQL+";")
		}
		files = append(files, dumpFile{
			Name: dumpFileBaseName(table) + ".sql",
			SQL:  strings.Join(parts, "\n\n") + "\n",
		})
	}
	return files, nil
}

// quoteQualifiedTableName renders table's name for a DROP TABLE statement,
// quoted through d.QuoteIdentifier and schema-qualified when table.Schema is
// set, rather than hand-writing quotes.
func quoteQualifiedTableName(d dialect.Dialect, table schema.TableDef) (string, error) {
	quotedName, err := d.QuoteIdentifier(table.Name)
	if err != nil {
		return "", err
	}
	if table.Schema == "" {
		return quotedName, nil
	}
	quotedSchema, err := d.QuoteIdentifier(table.Schema)
	if err != nil {
		return "", err
	}
	return quotedSchema + "." + quotedName, nil
}

// buildMigrationFormatFiles builds one .up.sql/.down.sql pair per CREATE
// TABLE statement, and one .up.sql per CREATE INDEX statement with no
// matching .down.sql, numbered in dependency order: every step for a table
// (its create, then its own indexes, in table.Indexes order) is numbered
// before the next table's steps begin. Dropping the table undoes its
// indexes too, and reverting an index step ahead of the table drop that
// still references it in a foreign key is what MySQL error 1553 exists to
// prevent -- see internal/migrationdir's doc on reverse sources running in
// descending filename order, and docs/core/07-migrations.md's note that a
// migration may hold fewer reverse sources than forward ones.
func buildMigrationFormatFiles(d dialect.Dialect, tables []schema.TableDef) ([]dumpFile, error) {
	var files []dumpFile
	step := 0
	for _, table := range tables {
		step++
		createSQL, err := renderCreateTableSQL(d, table)
		if err != nil {
			return nil, err
		}
		dropName, err := quoteQualifiedTableName(d, table)
		if err != nil {
			return nil, fmt.Errorf("table %q: %w", table.Name, err)
		}
		stem := fmt.Sprintf("%03d_create_%s", step, filenamePart(dumpFileBaseName(table)))
		files = append(files,
			dumpFile{Name: stem + ".up.sql", SQL: createSQL + ";\n"},
			dumpFile{Name: stem + ".down.sql", SQL: "DROP TABLE " + dropName + ";\n"},
		)

		indexSQLs, err := renderIndexSQLs(d, table)
		if err != nil {
			return nil, err
		}
		for i, indexSQL := range indexSQLs {
			step++
			indexStem := fmt.Sprintf("%03d_create_index_%s", step, filenamePart(table.Indexes[i].Name))
			files = append(files, dumpFile{Name: indexStem + ".up.sql", SQL: indexSQL + ";\n"})
		}
	}
	return files, nil
}

// writeDumpPreview prints one "-- <name>" header per file followed by its
// SQL, with a blank line between files, matching the shape writeDiffPlan
// uses for migrate diff and diff-live. The header is exactly the file the
// run would have written, so the preview reads the same for both -format
// schema and -format migration.
func writeDumpPreview(output io.Writer, files []dumpFile) {
	for i, f := range files {
		if i > 0 {
			_, _ = fmt.Fprintln(output)
		}
		_, _ = fmt.Fprintf(output, "-- %s\n", f.Name)
		_, _ = fmt.Fprint(output, f.SQL)
	}
}

// writeDumpOutput writes files into outputDirectory: missing is created
// with its parents, empty is used, and anything else already there --
// including a dotfile -- refuses the run before anything is written, since a
// dump must never overwrite checked-in DDL and there is no -force flag. The
// actual write goes through diff.WriteMigration, which writes through a
// sibling temporary directory and one os.Rename so a failure partway leaves
// outputDirectory untouched.
func writeDumpOutput(outputDirectory, dialectName string, files []dumpFile) error {
	info, err := os.Stat(outputDirectory)
	switch {
	case err == nil:
		if !info.IsDir() {
			return fmt.Errorf("dump: output path %q is not a directory", outputDirectory)
		}
		entries, err := os.ReadDir(outputDirectory)
		if err != nil {
			return fmt.Errorf("dump: read output directory %q: %w", outputDirectory, err)
		}
		if len(entries) > 0 {
			return fmt.Errorf("dump: output directory %q is not empty", outputDirectory)
		}
		// diff.WriteMigration's own os.Rename target must not already
		// exist: renaming a directory onto an existing empty one fails
		// with EEXIST on this platform, even though POSIX allows it in
		// principle. entries is already confirmed empty above, so
		// removing outputDirectory here loses nothing -- WriteMigration
		// recreates it, through its own temporary-directory-plus-rename
		// path, immediately below.
		if err := os.Remove(outputDirectory); err != nil {
			return fmt.Errorf("dump: remove empty output directory %q: %w", outputDirectory, err)
		}
	case !os.IsNotExist(err):
		return fmt.Errorf("dump: stat output directory %q: %w", outputDirectory, err)
	}

	plan := diff.Plan{Dialect: dialectName, Statements: make([]diff.PlannedStatement, len(files))}
	for i, f := range files {
		plan.Statements[i] = diff.PlannedStatement{Source: f.Name, SQL: f.SQL, Summary: f.Name}
	}
	return diff.WriteMigration(outputDirectory, plan)
}
