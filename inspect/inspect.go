// Package inspect reads live database metadata into schema descriptors.
//
// For PostgreSQL and SQLite, a table descriptor is either complete or an
// error: this package never returns a schema.Table silently missing columns
// or a primary key. PostgreSQL's information_schema is filtered per column
// and per table by the inspecting role's privileges while pg_catalog is not;
// this package cross-checks the two and reports [IncompleteMetadataError] or
// [TableNotFoundError] instead of guessing. MySQL has the same
// information_schema privilege filtering but no unfiltered catalog
// equivalent to cross-check against, so a MySQL inspection under a
// restricted grant can silently under-report a table's columns or primary
// key with no way for this package to detect it. SQLite has no such
// filtering to begin with.
package inspect

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strconv"
	"strings"

	sqlitequery "github.com/lestrrat-go/rasql-sqlite/query"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
)

// ErrTableNotFound is the sentinel wrapped by every [TableNotFoundError], so
// callers that only need a presence check can use errors.Is instead of
// errors.As.
var ErrTableNotFound = errors.New("inspect: table not found")

// TableNotFoundError reports that a requested table has no metadata in the
// inspected scope. It distinguishes a lookup miss (misspelled or wrong-schema
// table name) from a malformed table descriptor.
type TableNotFoundError struct {
	// Table is the requested table name.
	Table string
	// Scope describes where the lookup searched, such as "the current schema".
	Scope string
}

func (e *TableNotFoundError) Error() string {
	return fmt.Sprintf("inspect: table %q not found in %s", e.Table, e.Scope)
}

// Unwrap exposes ErrTableNotFound so errors.Is(err, ErrTableNotFound) works
// alongside errors.As against *TableNotFoundError.
func (e *TableNotFoundError) Unwrap() error {
	return ErrTableNotFound
}

// ErrIncompleteMetadata is the sentinel wrapped by every
// [IncompleteMetadataError], so callers can use errors.Is instead of
// errors.As when they only need to detect a metadata mismatch.
var ErrIncompleteMetadata = errors.New("inspect: incomplete table metadata")

// IncompleteMetadataError reports that the metadata query sees a different
// number of columns than the database catalog reports. PostgreSQL can produce this when
// information_schema.columns filters rows by has_column_privilege. SQLite can
// produce this when table_list and table_xinfo disagree. Visible and Actual
// identify the two counts.
type IncompleteMetadataError struct {
	// Table is the requested table name.
	Table string
	// Visible is the column count the metadata query exposed.
	Visible int
	// Actual is the true column count reported by the database catalog.
	Actual int
}

func (e *IncompleteMetadataError) Error() string {
	return fmt.Sprintf("inspect: table %q column metadata could not be read: the catalog reports %d columns but the metadata query exposed %d", e.Table, e.Actual, e.Visible)
}

// Unwrap exposes ErrIncompleteMetadata so errors.Is(err, ErrIncompleteMetadata)
// works alongside errors.As against *IncompleteMetadataError.
func (e *IncompleteMetadataError) Unwrap() error {
	return ErrIncompleteMetadata
}

// Queryer is implemented by *sql.DB and *sql.Tx.
type Queryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

// Inspector reads schema metadata for one SQL dialect.
// Its methods are safe for concurrent use when its Queryer is safe for concurrent use.
type Inspector struct {
	queryer Queryer
	dialect dialect.Dialect
}

// New creates an Inspector. It does not open a connection or start a transaction.
func New(queryer Queryer, d dialect.Dialect) (Inspector, error) {
	if isNil(queryer) {
		return Inspector{}, fmt.Errorf("inspect: queryer must not be nil")
	}
	if isNil(d) {
		return Inspector{}, fmt.Errorf("inspect: dialect must not be nil")
	}
	return Inspector{queryer: queryer, dialect: d}, nil
}

// Table reads the supported schema metadata for tableName. For PostgreSQL it
// returns [TableNotFoundError] when tableName does not exist and
// [IncompleteMetadataError] when the inspecting role's privileges hide some
// or all of the table's columns. SQLite returns [TableNotFoundError] when the
// table is absent and [IncompleteMetadataError] when its catalog column count
// disagrees with the rows returned by table_xinfo. See the package doc for the
// MySQL limitation, which this method cannot detect.
func (i Inspector) Table(ctx context.Context, tableName string) (schema.Table, error) {
	if err := schema.ValidateIdentifier(tableName); err != nil {
		return schema.Table{}, fmt.Errorf("inspect: invalid table name: %w", err)
	}
	if isNil(i.queryer) || isNil(i.dialect) {
		return schema.Table{}, fmt.Errorf("inspect: invalid inspector")
	}
	if i.dialect.Name() == "sqlite" {
		return i.sqliteTable(ctx, tableName)
	}
	return i.informationSchemaTable(ctx, tableName)
}

func (i Inspector) informationSchemaTable(ctx context.Context, tableName string) (schema.Table, error) {
	queries, err := i.informationSchemaQueries(ctx)
	if err != nil {
		return schema.Table{}, err
	}
	columns, err := i.readColumns(ctx, queries.columns, queries.argument(tableName))
	if err != nil {
		return schema.Table{}, err
	}
	if i.dialect.Name() == "postgresql" {
		if err := i.postgreSQLCheckColumnVisibility(ctx, tableName, len(columns)); err != nil {
			return schema.Table{}, err
		}
	} else if len(columns) == 0 {
		return schema.Table{}, &TableNotFoundError{Table: tableName, Scope: "the current database"}
	}
	primaryKey, err := i.readPrimaryKey(ctx, queries.primaryKey, queries.argument(tableName))
	if err != nil {
		return schema.Table{}, err
	}
	table := schema.Table{Name: tableName, Columns: columns, PrimaryKey: primaryKey}
	if queries.uniqueConstraints != "" {
		table.UniqueConstraints, err = i.readUniqueConstraints(ctx, queries.uniqueConstraints, queries.argument(tableName))
		if err != nil {
			return schema.Table{}, err
		}
	}
	if queries.checks != "" {
		table.Checks, err = i.readChecks(ctx, queries.checks, queries.argument(tableName))
		if err != nil {
			return schema.Table{}, err
		}
	}
	if queries.unsupportedExclusionConstraints != "" {
		if err := i.rejectUnsupportedExclusionConstraints(ctx, queries.unsupportedExclusionConstraints, queries.argument(tableName)); err != nil {
			return schema.Table{}, err
		}
	}
	if queries.unsupportedIndexes != "" {
		if err := i.rejectUnsupportedIndexes(ctx, queries.unsupportedIndexes, queries.argument(tableName)); err != nil {
			return schema.Table{}, err
		}
	}
	if queries.indexes != "" {
		table.Indexes, err = i.readIndexes(ctx, queries.indexes, queries.argument(tableName))
		if err != nil {
			return schema.Table{}, err
		}
	}
	if queries.foreignKeys != "" {
		table.ForeignKeys, err = i.readForeignKeys(ctx, queries.foreignKeys, queries.argument(tableName))
		if err != nil {
			return schema.Table{}, err
		}
	}
	if err := table.Validate(); err != nil {
		return schema.Table{}, fmt.Errorf("inspect: normalize table %q: %w", tableName, err)
	}
	return table, nil
}

func (i Inspector) informationSchemaQueries(ctx context.Context) (informationQueries, error) {
	if i.dialect.Name() != "postgresql" {
		return informationSchemaQueries(i.dialect.Name())
	}
	version, err := i.postgreSQLServerVersion(ctx)
	if err != nil {
		return informationQueries{}, err
	}
	return postgreSQLInformationQueries(version), nil
}

func (i Inspector) postgreSQLServerVersion(ctx context.Context) (int, error) {
	rows, err := i.queryer.QueryContext(ctx, "SHOW server_version_num")
	if err != nil {
		return 0, fmt.Errorf("inspect: read PostgreSQL server version: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return 0, fmt.Errorf("inspect: iterate PostgreSQL server version: %w", err)
		}
		return 0, fmt.Errorf("inspect: read PostgreSQL server version: no result")
	}
	var value string
	if err := rows.Scan(&value); err != nil {
		return 0, fmt.Errorf("inspect: scan PostgreSQL server version: %w", err)
	}
	version, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("inspect: parse PostgreSQL server version %q: %w", value, err)
	}
	if err := rows.Err(); err != nil {
		return 0, fmt.Errorf("inspect: iterate PostgreSQL server version: %w", err)
	}
	return version, nil
}

// postgreSQLCheckColumnVisibility compares visible, the column count
// readColumns actually saw, against the same relation's column count from
// pg_catalog. information_schema.columns filters each row by
// has_column_privilege, so a role granted SELECT on only some columns of an
// existing table — or none at all — gets a short or empty result with no
// error of its own; that silent gap is what this check closes. It runs for
// every PostgreSQL lookup, not only when visible is zero, because a
// nonzero-but-short result (e.g. two of three columns) is exactly as
// undetectable from information_schema alone as an empty one.
//
// The pg_catalog query resolves three questions at once: whether tableName
// exists, whether it genuinely has zero columns (which CREATE TABLE t ()
// permits), or how many columns actually exist to compare against visible.
//
// Concurrent DDL between the readColumns query and this one can make the two
// counts disagree even with no privilege problem involved; cmd/rasqlgen/main.go
// already runs inspection inside a transaction, which keeps that window
// small, so it is noted here rather than eliminated.
func (i Inspector) postgreSQLCheckColumnVisibility(ctx context.Context, tableName string, visible int) error {
	exists, catalogColumns, err := i.postgreSQLCatalogColumnCount(ctx, tableName)
	if err != nil {
		return err
	}
	if !exists {
		return &TableNotFoundError{Table: tableName, Scope: "the current schema"}
	}
	if catalogColumns == 0 {
		return fmt.Errorf("inspect: table %q cannot be represented: rasql does not support zero-column tables", tableName)
	}
	if int64(visible) != catalogColumns {
		return &IncompleteMetadataError{Table: tableName, Visible: visible, Actual: int(catalogColumns)}
	}
	return nil
}

// postgreSQLCatalogColumnCountQuery answers absence, a genuine zero-column
// table, and the true column count in one round trip. GROUP BY table_data.oid
// is load-bearing: the LEFT JOIN to pg_attribute means a table with zero live
// columns still contributes one group, so zero result rows can only mean the
// relation itself is absent; without the GROUP BY the aggregate always
// returns exactly one row regardless of whether the relation exists.
// relkind IN ('r','p','v','f') restricts to the same relation kinds
// information_schema.columns itself considers (ordinary and partitioned
// tables, views, foreign tables), so a name that resolves to a sequence or an
// index does not count attributes that are not comparable to visible.
// pg_class, pg_attribute and pg_namespace carry no per-row privilege filter
// and stay readable by PUBLIC, unlike information_schema.columns and
// information_schema.tables.
const postgreSQLCatalogColumnCountQuery = "SELECT count(attribute.attnum) FROM pg_catalog.pg_class AS table_data JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace LEFT JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = table_data.oid AND attribute.attnum > 0 AND NOT attribute.attisdropped WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND table_data.relkind IN ('r','p','v','f') GROUP BY table_data.oid"

// postgreSQLCatalogColumnCount runs postgreSQLCatalogColumnCountQuery and
// reports whether tableName exists and, if so, its true column count.
func (i Inspector) postgreSQLCatalogColumnCount(ctx context.Context, tableName string) (exists bool, count int64, err error) {
	rows, err := i.queryer.QueryContext(ctx, postgreSQLCatalogColumnCountQuery, tableName)
	if err != nil {
		return false, 0, fmt.Errorf("inspect: count table %q catalog columns: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return false, 0, fmt.Errorf("inspect: iterate table %q catalog columns: %w", tableName, err)
		}
		return false, 0, nil
	}
	if err := rows.Scan(&count); err != nil {
		return false, 0, fmt.Errorf("inspect: scan table %q catalog column count: %w", tableName, err)
	}
	if err := rows.Err(); err != nil {
		return false, 0, fmt.Errorf("inspect: iterate table %q catalog columns: %w", tableName, err)
	}
	return true, count, nil
}

func (i Inspector) sqliteTable(ctx context.Context, tableName string) (schema.Table, error) {
	options, err := i.sqliteTableOptions(ctx, tableName)
	if err != nil {
		return schema.Table{}, err
	}
	tableName = options.name

	query := sqliteQualifiedPragma(options.database, "table_xinfo", tableName)
	rows, err := i.queryer.QueryContext(ctx, query)
	if err != nil {
		return schema.Table{}, fmt.Errorf("inspect: read SQLite columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type primaryColumn struct {
		position int64
		name     string
	}
	columns := make([]schema.Column, 0)
	primaryColumns := make([]primaryColumn, 0)
	var metadataRows int64
	for rows.Next() {
		metadataRows++
		var ordinal int64
		var name string
		var databaseType string
		var notNull int64
		var defaultValue any
		var primaryPosition int64
		var hidden int64
		if err := rows.Scan(&ordinal, &name, &databaseType, &notNull, &defaultValue, &primaryPosition, &hidden); err != nil {
			return schema.Table{}, fmt.Errorf("inspect: scan SQLite column: %w", err)
		}
		if hidden != 0 {
			return schema.Table{}, fmt.Errorf("inspect: SQLite table %q cannot be represented: hidden or generated column %q is not supported", tableName, name)
		}
		// The signedness result is discarded: SQLite's declared type carries
		// none this package can trust, because an INTEGER column stores a
		// signed 64-bit value however it was declared, so even a column
		// declared UNSIGNED BIG INT is signed storage. The descriptor records
		// that truth rather than the declaration.
		columnType, err := normalizeType(i.dialect.Name(), databaseType)
		if err != nil {
			return schema.Table{}, fmt.Errorf("inspect: column %q: %w", name, err)
		}
		column := schema.Column{Name: name, Type: columnType, Nullable: notNull == 0 && primaryPosition == 0, Default: text(defaultValue)}
		columns = append(columns, column)
		if primaryPosition > 0 {
			primaryColumns = append(primaryColumns, primaryColumn{position: primaryPosition, name: name})
		}
	}
	if err := rows.Err(); err != nil {
		return schema.Table{}, fmt.Errorf("inspect: iterate SQLite columns: %w", err)
	}
	if metadataRows != options.columnCount {
		return schema.Table{}, &IncompleteMetadataError{Table: tableName, Visible: int(metadataRows), Actual: int(options.columnCount)}
	}
	if len(columns) == 0 {
		return schema.Table{}, &TableNotFoundError{Table: tableName, Scope: "the connection's attached databases"}
	}
	sort.Slice(primaryColumns, func(left, right int) bool {
		return primaryColumns[left].position < primaryColumns[right].position
	})
	primaryKey := make([]string, len(primaryColumns))
	for index, column := range primaryColumns {
		primaryKey[index] = column.name
	}
	if options.withoutRowID || options.strict {
		return schema.Table{}, fmt.Errorf("inspect: SQLite table %q cannot be represented: STRICT and WITHOUT ROWID table options are unsupported", tableName)
	}
	definition, err := i.sqliteTableDefinition(ctx, options.database, tableName)
	if err != nil {
		return schema.Table{}, err
	}
	if err := validateSQLitePrimaryKey(definition, tableName); err != nil {
		return schema.Table{}, err
	}
	indexes, uniqueConstraints, err := i.sqliteIndexes(ctx, options.database, tableName)
	if err != nil {
		return schema.Table{}, err
	}
	if definition != nil {
		uniqueConstraints, err = sqliteUniqueConstraints(definition, tableName)
		if err != nil {
			return schema.Table{}, err
		}
	} else if len(uniqueConstraints) > 0 {
		return schema.Table{}, fmt.Errorf("inspect: SQLite table %q cannot be represented: UNIQUE constraint definitions are unavailable", tableName)
	}
	checks, err := sqliteChecks(definition, tableName)
	if err != nil {
		return schema.Table{}, err
	}
	foreignKeys, err := i.sqliteForeignKeys(ctx, options.database, tableName)
	if err != nil {
		return schema.Table{}, err
	}
	table := schema.Table{
		Name:              tableName,
		Columns:           columns,
		PrimaryKey:        primaryKey,
		UniqueConstraints: uniqueConstraints,
		Checks:            checks,
		Indexes:           indexes,
		ForeignKeys:       foreignKeys,
	}
	if err := table.Validate(); err != nil {
		return schema.Table{}, fmt.Errorf("inspect: normalize table %q: %w", tableName, err)
	}
	return table, nil
}

type sqliteTableOptions struct {
	database     string
	name         string
	columnCount  int64
	withoutRowID bool
	strict       bool
}

func (i Inspector) sqliteTableOptions(ctx context.Context, tableName string) (sqliteTableOptions, error) {
	query := "PRAGMA table_list(\"" + sqlitePragmaIdentifier(tableName) + "\")"
	rows, err := i.queryer.QueryContext(ctx, query)
	if err != nil {
		return sqliteTableOptions{}, fmt.Errorf("inspect: read SQLite table options: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return sqliteTableOptions{}, fmt.Errorf("inspect: iterate SQLite table options: %w", err)
		}
		return sqliteTableOptions{}, &TableNotFoundError{Table: tableName, Scope: "the connection's attached databases"}
	}
	var databaseName string
	var name string
	var kind string
	var columnCount int64
	var withoutRowID int64
	var strict int64
	if err := rows.Scan(&databaseName, &name, &kind, &columnCount, &withoutRowID, &strict); err != nil {
		return sqliteTableOptions{}, fmt.Errorf("inspect: scan SQLite table options: %w", err)
	}
	if err := rows.Err(); err != nil {
		return sqliteTableOptions{}, fmt.Errorf("inspect: iterate SQLite table options: %w", err)
	}
	if !strings.EqualFold(kind, "table") {
		return sqliteTableOptions{}, fmt.Errorf("inspect: SQLite table %q cannot be represented: table kind %q is unsupported", tableName, kind)
	}
	return sqliteTableOptions{database: databaseName, name: name, columnCount: columnCount, withoutRowID: withoutRowID != 0, strict: strict != 0}, nil
}

func (i Inspector) sqliteTableDefinition(ctx context.Context, databaseName, tableName string) (*sqlitequery.CreateTableStatement, error) {
	query := `SELECT sql FROM "` + sqlitePragmaIdentifier(databaseName) + `".sqlite_master WHERE type = 'table' AND name = ?`
	rows, err := i.queryer.QueryContext(ctx, query, tableName)
	if err != nil {
		return nil, fmt.Errorf("inspect: read SQLite table definition: %w", err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return nil, fmt.Errorf("inspect: iterate SQLite table definition: %w", err)
		}
		return nil, &TableNotFoundError{Table: tableName, Scope: "the connection's attached databases"}
	}
	var definition sql.NullString
	if err := rows.Scan(&definition); err != nil {
		return nil, fmt.Errorf("inspect: scan SQLite table definition: %w", err)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect: iterate SQLite table definition: %w", err)
	}
	if !definition.Valid || definition.String == "" {
		return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: its CREATE TABLE definition is unavailable", tableName)
	}
	if sqliteDefinitionContainsVirtualTableKeyword(definition.String) {
		return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: CREATE VIRTUAL TABLE definitions are unsupported", tableName)
	}
	if sqliteDefinitionContainsForeignKeyKeyword(definition.String) {
		return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: DEFERRABLE and INITIALLY foreign-key clauses are unsupported", tableName)
	}
	statement, err := sqlitequery.ParseStatement(sqliteNormalizeForeignKeyActions(definition.String))
	if err != nil {
		if strings.Contains(strings.ToUpper(definition.String), "CHECK") {
			return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: its CREATE TABLE definition contains an unsupported CHECK form: %w", tableName, err)
		}
		return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: its CREATE TABLE definition is unsupported: %w", tableName, err)
	}
	createTable, ok := statement.(*sqlitequery.CreateTableStatement)
	if !ok || !strings.EqualFold(createTable.Name.String(), tableName) {
		return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: its CREATE TABLE definition has an unexpected shape", tableName)
	}
	if createTable.As != nil {
		return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: CREATE TABLE AS SELECT definitions are unsupported", tableName)
	}
	return createTable, nil
}

func sqliteDefinitionContainsVirtualTableKeyword(definition string) bool {
	index, ok := sqliteMatchKeyword(definition, sqliteSkipSpaceAndComments(definition, 0), "CREATE")
	if !ok {
		return false
	}
	index, ok = sqliteMatchKeyword(definition, sqliteSkipSpaceAndComments(definition, index), "VIRTUAL")
	if !ok {
		return false
	}
	_, ok = sqliteMatchKeyword(definition, sqliteSkipSpaceAndComments(definition, index), "TABLE")
	return ok
}

func sqliteNormalizeForeignKeyActions(definition string) string {
	var normalized strings.Builder
	normalized.Grow(len(definition))
	references := false
	parentheses := 0
	copied := 0
	for index := 0; index < len(definition); {
		switch definition[index] {
		case '\'', '"', '`':
			index = skipSQLiteQuoted(definition, index, definition[index])
			continue
		case '[':
			index++
			for index < len(definition) {
				if definition[index] == ']' {
					index++
					break
				}
				index++
			}
			continue
		case '-':
			if index+1 < len(definition) && definition[index+1] == '-' {
				index += 2
				for index < len(definition) && definition[index] != '\n' {
					index++
				}
				continue
			}
		case '/':
			if index+1 < len(definition) && definition[index+1] == '*' {
				index += 2
				for index+1 < len(definition) && (definition[index] != '*' || definition[index+1] != '/') {
					index++
				}
				if index+1 < len(definition) {
					index += 2
				}
				continue
			}
		case '(':
			parentheses++
			index++
			continue
		case ')':
			if parentheses > 0 {
				parentheses--
			}
			index++
			continue
		case ',':
			if parentheses == 1 {
				references = false
			}
			index++
			continue
		}
		if !sqliteIdentifierStart(definition[index]) {
			index++
			continue
		}
		start := index
		index++
		for index < len(definition) && sqliteIdentifierPart(definition[index]) {
			index++
		}
		token := definition[start:index]
		if references && strings.EqualFold(token, "ON") {
			if end, ok := sqliteForeignKeyActionEnd(definition, start); ok {
				normalized.WriteString(definition[copied:start])
				normalized.WriteByte(' ')
				index = end
				copied = index
				continue
			}
		}
		if strings.EqualFold(token, "REFERENCES") && parentheses == 1 {
			references = true
		}
	}
	normalized.WriteString(definition[copied:])
	return normalized.String()
}

func sqliteForeignKeyActionEnd(definition string, index int) (int, bool) {
	index, ok := sqliteMatchKeyword(definition, index, "ON")
	if !ok {
		return 0, false
	}
	action := index
	index, ok = sqliteMatchKeyword(definition, action, "DELETE")
	if !ok {
		index, ok = sqliteMatchKeyword(definition, action, "UPDATE")
		if !ok {
			return 0, false
		}
	}
	if next, ok := sqliteMatchKeyword(definition, index, "NO"); ok {
		return sqliteMatchKeyword(definition, next, "ACTION")
	}
	if next, ok := sqliteMatchKeyword(definition, index, "SET"); ok {
		if next, ok = sqliteMatchKeyword(definition, next, "NULL"); ok {
			return next, true
		}
		return sqliteMatchKeyword(definition, next, "DEFAULT")
	}
	if next, ok := sqliteMatchKeyword(definition, index, "RESTRICT"); ok {
		return next, true
	}
	return sqliteMatchKeyword(definition, index, "CASCADE")
}

func sqliteMatchKeyword(value string, index int, keyword string) (int, bool) {
	index = sqliteSkipSpaceAndComments(value, index)
	if index+len(keyword) > len(value) || !strings.EqualFold(value[index:index+len(keyword)], keyword) {
		return 0, false
	}
	end := index + len(keyword)
	if end < len(value) && sqliteIdentifierPart(value[end]) {
		return 0, false
	}
	return end, true
}

func sqliteSkipSpaceAndComments(value string, index int) int {
	for {
		start := index
		for index < len(value) {
			switch value[index] {
			case ' ', '\t', '\n', '\r', '\f':
				index++
			default:
				goto comments
			}
		}
	comments:
		if index+1 < len(value) && value[index] == '-' && value[index+1] == '-' {
			index += 2
			for index < len(value) && value[index] != '\n' {
				index++
			}
			continue
		}
		if index+1 < len(value) && value[index] == '/' && value[index+1] == '*' {
			index += 2
			for index+1 < len(value) && (value[index] != '*' || value[index+1] != '/') {
				index++
			}
			if index+1 < len(value) {
				index += 2
			}
			continue
		}
		if index == start {
			return index
		}
	}
}

func sqliteDefinitionContainsForeignKeyKeyword(definition string) bool {
	references := false
	parentheses := 0
	for index := 0; index < len(definition); {
		switch definition[index] {
		case '\'', '"', '`':
			index = skipSQLiteQuoted(definition, index, definition[index])
			continue
		case '[':
			index++
			for index < len(definition) {
				if definition[index] == ']' {
					index++
					break
				}
				index++
			}
			continue
		case '-':
			if index+1 < len(definition) && definition[index+1] == '-' {
				index += 2
				for index < len(definition) && definition[index] != '\n' {
					index++
				}
				continue
			}
		case '/':
			if index+1 < len(definition) && definition[index+1] == '*' {
				index += 2
				for index+1 < len(definition) && (definition[index] != '*' || definition[index+1] != '/') {
					index++
				}
				if index+1 < len(definition) {
					index += 2
				}
				continue
			}
		case '(':
			parentheses++
			index++
			continue
		case ')':
			if parentheses > 0 {
				parentheses--
			}
			index++
			continue
		case ',':
			if parentheses == 1 {
				references = false
			}
			index++
			continue
		}
		if !sqliteIdentifierStart(definition[index]) {
			index++
			continue
		}
		start := index
		index++
		for index < len(definition) && sqliteIdentifierPart(definition[index]) {
			index++
		}
		token := definition[start:index]
		if strings.EqualFold(token, "REFERENCES") {
			references = true
		}
		if references && (strings.EqualFold(token, "DEFERRABLE") || strings.EqualFold(token, "INITIALLY")) {
			return true
		}
	}
	return false
}

func skipSQLiteQuoted(value string, index int, quote byte) int {
	index++
	for index < len(value) {
		if value[index] != quote {
			index++
			continue
		}
		if index+1 < len(value) && value[index+1] == quote {
			index += 2
			continue
		}
		return index + 1
	}
	return len(value)
}

func sqliteIdentifierStart(value byte) bool {
	return value == '_' || value >= 'A' && value <= 'Z' || value >= 'a' && value <= 'z'
}

func sqliteIdentifierPart(value byte) bool {
	return sqliteIdentifierStart(value) || value >= '0' && value <= '9' || value == '$'
}

func validateSQLitePrimaryKey(statement *sqlitequery.CreateTableStatement, tableName string) error {
	if statement == nil {
		return nil
	}
	for _, column := range statement.Columns {
		for _, constraint := range column.Constraints {
			if constraint.Kind != sqlitequery.ConstraintPrimaryKey {
				continue
			}
			if constraint.Autoincrement || constraint.Conflict != sqlitequery.ConflictDefault || constraint.Direction != sqlitequery.SortDefault {
				return fmt.Errorf("inspect: SQLite table %q cannot be represented: AUTOINCREMENT, primary-key conflict resolution, and primary-key ordering are unsupported", tableName)
			}
		}
	}
	for _, constraint := range statement.Constraints {
		if constraint.Kind != sqlitequery.ConstraintPrimaryKey {
			continue
		}
		if constraint.Conflict != sqlitequery.ConflictDefault {
			return fmt.Errorf("inspect: SQLite table %q cannot be represented: AUTOINCREMENT and primary-key conflict resolution are unsupported", tableName)
		}
		for _, column := range constraint.Columns {
			if column.Collation != nil || column.Direction != sqlitequery.SortDefault {
				return fmt.Errorf("inspect: SQLite table %q cannot be represented: primary-key ordering and collations are unsupported", tableName)
			}
		}
	}
	return nil
}

func sqliteUniqueConstraints(statement *sqlitequery.CreateTableStatement, tableName string) ([]schema.UniqueConstraint, error) {
	if statement == nil {
		return nil, nil
	}
	constraints := make([]schema.UniqueConstraint, 0)
	for _, column := range statement.Columns {
		for _, constraint := range column.Constraints {
			if constraint.Kind != sqlitequery.ConstraintUnique {
				continue
			}
			if constraint.Conflict != sqlitequery.ConflictDefault {
				return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: UNIQUE conflict resolution is unsupported", tableName)
			}
			constraints = append(constraints, schema.UniqueConstraint{Name: sqliteIdentifierName(constraint.Name), Columns: []string{column.Name.Name}})
		}
	}
	for _, constraint := range statement.Constraints {
		if constraint.Kind != sqlitequery.ConstraintUnique {
			continue
		}
		if constraint.Conflict != sqlitequery.ConflictDefault {
			return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: UNIQUE conflict resolution is unsupported", tableName)
		}
		columns := make([]string, len(constraint.Columns))
		for index, column := range constraint.Columns {
			expression, ok := column.Expression.(*sqlitequery.IdentifierExpression)
			if !ok || expression == nil || len(expression.Name) != 1 || column.Collation != nil || column.Direction != sqlitequery.SortDefault {
				return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: UNIQUE constraints with expressions, collations, or ordering are unsupported", tableName)
			}
			columns[index] = expression.Name[0].Name
		}
		constraints = append(constraints, schema.UniqueConstraint{Name: sqliteIdentifierName(constraint.Name), Columns: columns})
	}
	return constraints, nil
}

func sqliteChecks(statement *sqlitequery.CreateTableStatement, tableName string) ([]schema.CheckConstraint, error) {
	if statement == nil {
		return nil, nil
	}
	checks := make([]schema.CheckConstraint, 0)
	for _, column := range statement.Columns {
		for _, constraint := range column.Constraints {
			if constraint.Kind != sqlitequery.ConstraintCheck {
				continue
			}
			expression, err := sqliteExpressionSQL(constraint.Expression)
			if err != nil {
				return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: serialize CHECK constraint: %w", tableName, err)
			}
			checks = append(checks, schema.CheckConstraint{Name: sqliteIdentifierName(constraint.Name), Expression: expression})
		}
	}
	for _, constraint := range statement.Constraints {
		if constraint.Kind != sqlitequery.ConstraintCheck {
			continue
		}
		expression, err := sqliteExpressionSQL(constraint.Expression)
		if err != nil {
			return nil, fmt.Errorf("inspect: SQLite table %q cannot be represented: serialize CHECK constraint: %w", tableName, err)
		}
		checks = append(checks, schema.CheckConstraint{Name: sqliteIdentifierName(constraint.Name), Expression: expression})
	}
	return checks, nil
}

func sqliteExpressionSQL(expression sqlitequery.Expression) (string, error) {
	statement := &sqlitequery.CreateTableStatement{
		Name:        sqlitequery.QualifiedName{{Name: "rasql_check"}},
		Columns:     []sqlitequery.ColumnDefinition{{Name: sqlitequery.Identifier{Name: "value"}}},
		Constraints: []sqlitequery.TableConstraint{{Kind: sqlitequery.ConstraintCheck, Expression: expression}},
	}
	serialized, err := sqlitequery.SerializeStatement(statement)
	if err != nil {
		return "", err
	}
	const prefix = "CREATE TABLE rasql_check (value, CHECK ("
	const suffix = "))"
	if !strings.HasPrefix(serialized, prefix) || !strings.HasSuffix(serialized, suffix) {
		return "", fmt.Errorf("unexpected serialized CHECK shape %q", serialized)
	}
	return strings.TrimSuffix(strings.TrimPrefix(serialized, prefix), suffix), nil
}

func sqliteIdentifierName(identifier *sqlitequery.Identifier) string {
	if identifier == nil {
		return ""
	}
	return identifier.Name
}

func (i Inspector) sqliteForeignKeys(ctx context.Context, databaseName, tableName string) ([]schema.ForeignKey, error) {
	query := sqliteQualifiedPragma(databaseName, "foreign_key_list", tableName)
	rows, err := i.queryer.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("inspect: read SQLite foreign keys: %w", err)
	}
	defer func() { _ = rows.Close() }()
	type foreignKey struct {
		id             int64
		key            schema.ForeignKey
		referencedRows int64
	}
	keys := make([]foreignKey, 0)
	for rows.Next() {
		var id, sequence int64
		var referencedTable, column string
		var referencedColumn sql.NullString
		var onUpdate, onDelete, match string
		if err := rows.Scan(&id, &sequence, &referencedTable, &column, &referencedColumn, &onUpdate, &onDelete, &match); err != nil {
			return nil, fmt.Errorf("inspect: scan SQLite foreign key: %w", err)
		}
		if match != "" && !strings.EqualFold(match, "NONE") && !strings.EqualFold(match, "SIMPLE") {
			return nil, fmt.Errorf("inspect: SQLite foreign key on table %q cannot be represented: MATCH %s is unsupported", tableName, match)
		}
		deleteAction, err := sqliteReferenceAction(onDelete)
		if err != nil {
			return nil, fmt.Errorf("inspect: SQLite foreign key on table %q: %w", tableName, err)
		}
		updateAction, err := sqliteReferenceAction(onUpdate)
		if err != nil {
			return nil, fmt.Errorf("inspect: SQLite foreign key on table %q: %w", tableName, err)
		}
		index := -1
		for candidate := range keys {
			if keys[candidate].id == id {
				index = candidate
				break
			}
		}
		if index < 0 {
			keys = append(keys, foreignKey{
				id: id,
				key: schema.ForeignKey{
					ReferencedTable: referencedTable,
					OnDelete:        deleteAction,
					OnUpdate:        updateAction,
				},
			})
			index = len(keys) - 1
		}
		key := &keys[index].key
		if key.ReferencedTable != referencedTable || key.OnDelete != deleteAction || key.OnUpdate != updateAction || sequence != keys[index].referencedRows {
			return nil, fmt.Errorf("inspect: SQLite foreign key on table %q has inconsistent metadata", tableName)
		}
		key.Columns = append(key.Columns, column)
		if !referencedColumn.Valid {
			return nil, fmt.Errorf("inspect: SQLite foreign key on table %q cannot be represented: the referenced column is implicit", tableName)
		}
		key.ReferencedColumns = append(key.ReferencedColumns, referencedColumn.String)
		keys[index].referencedRows++
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect: iterate SQLite foreign keys: %w", err)
	}
	result := make([]schema.ForeignKey, len(keys))
	for index := range keys {
		result[index] = keys[index].key
	}
	return result, nil
}

func sqliteReferenceAction(action string) (schema.ReferenceAction, error) {
	switch strings.ToUpper(action) {
	case "", "NO ACTION":
		return schema.ReferenceActionNoAction, nil
	case "RESTRICT":
		return schema.ReferenceActionRestrict, nil
	case "CASCADE":
		return schema.ReferenceActionCascade, nil
	case "SET NULL":
		return schema.ReferenceActionSetNull, nil
	case "SET DEFAULT":
		return schema.ReferenceActionSetDefault, nil
	default:
		return "", fmt.Errorf("unsupported reference action %q", action)
	}
}

func (i Inspector) sqliteIndexes(ctx context.Context, databaseName, tableName string) ([]schema.Index, []schema.UniqueConstraint, error) {
	query := sqliteQualifiedPragma(databaseName, "index_list", tableName)
	rows, err := i.queryer.QueryContext(ctx, query)
	if err != nil {
		return nil, nil, fmt.Errorf("inspect: read SQLite indexes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	indexes := make([]schema.Index, 0)
	uniqueConstraints := make([]schema.UniqueConstraint, 0)
	for rows.Next() {
		var sequence int64
		var name string
		var unique bool
		var origin string
		var partial bool
		if err := rows.Scan(&sequence, &name, &unique, &origin, &partial); err != nil {
			return nil, nil, fmt.Errorf("inspect: scan SQLite index: %w", err)
		}
		if origin != "c" && origin != "u" {
			continue
		}
		if partial {
			return nil, nil, fmt.Errorf("inspect: SQLite index %q cannot be represented: partial indexes are unsupported", name)
		}
		if origin == "c" {
			if err := schema.ValidateIdentifier(name); err != nil {
				return nil, nil, fmt.Errorf("inspect: SQLite index %q: %w", name, err)
			}
		}
		if origin == "u" {
			uniqueConstraints = append(uniqueConstraints, schema.UniqueConstraint{})
			continue
		}
		columns, err := i.sqliteIndexColumns(ctx, databaseName, name)
		if err != nil {
			return nil, nil, err
		}
		indexes = append(indexes, schema.Index{Name: name, Columns: columns, Unique: unique})
	}
	if err := rows.Err(); err != nil {
		return nil, nil, fmt.Errorf("inspect: iterate SQLite indexes: %w", err)
	}
	return indexes, uniqueConstraints, nil
}

func (i Inspector) sqliteIndexColumns(ctx context.Context, databaseName, indexName string) ([]string, error) {
	query := sqliteQualifiedPragma(databaseName, "index_xinfo", indexName)
	rows, err := i.queryer.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("inspect: read SQLite index %q columns: %w", indexName, err)
	}
	defer func() { _ = rows.Close() }()

	columns := make([]string, 0)
	for rows.Next() {
		var sequence, tableColumn, descending, keyColumn int64
		var name sql.NullString
		var collation string
		if err := rows.Scan(&sequence, &tableColumn, &name, &descending, &collation, &keyColumn); err != nil {
			return nil, fmt.Errorf("inspect: scan SQLite index %q column: %w", indexName, err)
		}
		if keyColumn == 0 {
			continue
		}
		if !name.Valid {
			return nil, fmt.Errorf("inspect: SQLite index %q cannot be represented: expression indexes are unsupported", indexName)
		}
		if descending != 0 {
			return nil, fmt.Errorf("inspect: SQLite index %q cannot be represented: descending columns are unsupported", indexName)
		}
		if !strings.EqualFold(collation, "BINARY") {
			return nil, fmt.Errorf("inspect: SQLite index %q cannot be represented: nondefault collations are unsupported", indexName)
		}
		columns = append(columns, name.String)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect: iterate SQLite index %q columns: %w", indexName, err)
	}
	if len(columns) == 0 {
		return nil, fmt.Errorf("inspect: SQLite index %q has no columns", indexName)
	}
	return columns, nil
}

func sqlitePragmaIdentifier(value string) string {
	return strings.ReplaceAll(value, `"`, `""`)
}

func sqliteQualifiedPragma(databaseName, pragmaName, identifier string) string {
	return `PRAGMA "` + sqlitePragmaIdentifier(databaseName) + `".` + pragmaName + `("` + sqlitePragmaIdentifier(identifier) + `")`
}

func (i Inspector) readColumns(ctx context.Context, query string, argument any) ([]schema.Column, error) {
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("inspect: read columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	columns := make([]schema.Column, 0)
	for rows.Next() {
		var name string
		var databaseType string
		var nullable string
		var defaultValue any
		var numericPrecision sql.NullInt64
		var numericScale sql.NullInt64
		if err := rows.Scan(&name, &databaseType, &nullable, &defaultValue, &numericPrecision, &numericScale); err != nil {
			return nil, fmt.Errorf("inspect: scan column: %w", err)
		}
		columnType, err := normalizeType(i.dialect.Name(), databaseType)
		if err != nil {
			return nil, fmt.Errorf("inspect: column %q: %w", name, err)
		}
		column := schema.Column{
			Name:     name,
			Type:     columnType,
			Nullable: strings.EqualFold(nullable, "YES"),
			Default:  text(defaultValue),
		}
		if _, ok := columnType.(schema.DecimalType); ok {
			if !numericPrecision.Valid {
				return nil, fmt.Errorf("inspect: column %q: unconstrained NUMERIC has no precision to record: declare it as NUMERIC(precision, scale)", name)
			}
			if !numericScale.Valid {
				return nil, fmt.Errorf("inspect: column %q: decimal column reports no scale to record: declare it as NUMERIC(precision, scale)", name)
			}
			column.Type = schema.DecimalType{
				Precision: int(numericPrecision.Int64),
				Scale:     schema.NewDecimalScale(int(numericScale.Int64)),
			}
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect: iterate columns: %w", err)
	}
	return columns, nil
}

func (i Inspector) readPrimaryKey(ctx context.Context, query string, argument any) ([]string, error) {
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("inspect: read primary key: %w", err)
	}
	defer func() { _ = rows.Close() }()

	columns := make([]string, 0)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("inspect: scan primary-key column: %w", err)
		}
		columns = append(columns, name)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect: iterate primary-key columns: %w", err)
	}
	return columns, nil
}

func (i Inspector) readUniqueConstraints(ctx context.Context, query string, argument any) ([]schema.UniqueConstraint, error) {
	// PostgreSQL 18 permits NOT ENFORCED only for CHECK and foreign-key constraints, so a UNIQUE NOT ENFORCED catalog row cannot exist.
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("inspect: read unique constraints: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var constraints []schema.UniqueConstraint
	for rows.Next() {
		var name string
		var column string
		var deferrable bool
		var initiallyDeferred bool
		var nullsNotDistinct bool
		var includesColumns bool
		var temporal bool
		var unsupportedIndexMetadata bool
		if err := rows.Scan(&name, &column, &deferrable, &initiallyDeferred, &nullsNotDistinct, &includesColumns, &temporal, &unsupportedIndexMetadata); err != nil {
			return nil, fmt.Errorf("inspect: scan unique constraint: %w", err)
		}
		if deferrable || initiallyDeferred || nullsNotDistinct {
			return nil, fmt.Errorf("inspect: unique constraint %q cannot be represented: rasql supports only non-deferrable unique constraints with distinct nulls", name)
		}
		if includesColumns {
			return nil, fmt.Errorf("inspect: unique constraint %q cannot be represented: rasql does not support unique constraints with included columns", name)
		}
		if temporal {
			return nil, fmt.Errorf("inspect: unique constraint %q cannot be represented: rasql does not support temporal unique constraints", name)
		}
		if unsupportedIndexMetadata {
			return nil, fmt.Errorf("inspect: unique constraint %q cannot be represented: rasql does not support unique constraints whose backing indexes use nondefault collations, storage options or tablespaces, or replica identity", name)
		}
		if len(constraints) == 0 || constraints[len(constraints)-1].Name != name {
			constraints = append(constraints, schema.UniqueConstraint{Name: name})
		}
		constraints[len(constraints)-1].Columns = append(constraints[len(constraints)-1].Columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect: iterate unique constraints: %w", err)
	}
	return constraints, nil
}

func (i Inspector) readChecks(ctx context.Context, query string, argument any) ([]schema.CheckConstraint, error) {
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("inspect: read check constraints: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var checks []schema.CheckConstraint
	for rows.Next() {
		var name string
		var expression string
		var noInherit bool
		var validated bool
		var enforced bool
		if err := rows.Scan(&name, &expression, &noInherit, &validated, &enforced); err != nil {
			return nil, fmt.Errorf("inspect: scan check constraint: %w", err)
		}
		if noInherit {
			return nil, fmt.Errorf("inspect: check constraint %q cannot be represented: rasql does not support NO INHERIT check constraints", name)
		}
		if !validated {
			return nil, fmt.Errorf("inspect: check constraint %q cannot be represented: rasql does not support NOT VALID check constraints", name)
		}
		if !enforced {
			return nil, fmt.Errorf("inspect: check constraint %q cannot be represented: rasql does not support NOT ENFORCED check constraints", name)
		}
		checks = append(checks, schema.CheckConstraint{Name: name, Expression: expression})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect: iterate check constraints: %w", err)
	}
	return checks, nil
}

func (i Inspector) rejectUnsupportedIndexes(ctx context.Context, query string, argument any) error {
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return fmt.Errorf("inspect: read unsupported indexes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("inspect: iterate unsupported indexes: %w", err)
		}
		return nil
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		return fmt.Errorf("inspect: scan unsupported index: %w", err)
	}
	return fmt.Errorf("inspect: index %q cannot be represented: rasql supports only valid, non-partial B-tree indexes with simple ascending columns, no included columns, default operator classes and collations, default persistent storage options and tablespaces, distinct nulls, and no replica identity", name)
}

func (i Inspector) rejectUnsupportedExclusionConstraints(ctx context.Context, query string, argument any) error {
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return fmt.Errorf("inspect: read unsupported exclusion constraints: %w", err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return fmt.Errorf("inspect: iterate unsupported exclusion constraints: %w", err)
		}
		return nil
	}
	var name string
	if err := rows.Scan(&name); err != nil {
		return fmt.Errorf("inspect: scan unsupported exclusion constraint: %w", err)
	}
	return fmt.Errorf("inspect: exclusion constraint %q cannot be represented: rasql does not support exclusion constraints", name)
}

func (i Inspector) readIndexes(ctx context.Context, query string, argument any) ([]schema.Index, error) {
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("inspect: read indexes: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var indexes []schema.Index
	for rows.Next() {
		var name string
		var unique bool
		var column string
		if err := rows.Scan(&name, &unique, &column); err != nil {
			return nil, fmt.Errorf("inspect: scan index: %w", err)
		}
		if len(indexes) == 0 || indexes[len(indexes)-1].Name != name {
			indexes = append(indexes, schema.Index{Name: name, Unique: unique})
		}
		indexes[len(indexes)-1].Columns = append(indexes[len(indexes)-1].Columns, column)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect: iterate indexes: %w", err)
	}
	return indexes, nil
}

func (i Inspector) readForeignKeys(ctx context.Context, query string, argument any) ([]schema.ForeignKey, error) {
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("inspect: read foreign keys: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var keys []schema.ForeignKey
	for rows.Next() {
		var name string
		var column string
		var referencedTable string
		var referencedColumn string
		var deleteAction string
		var updateAction string
		var matchType string
		var referencedInCurrentSchema bool
		var deferrable bool
		var initiallyDeferred bool
		var deleteSetColumns bool
		var validated bool
		var enforced bool
		var temporal bool
		if err := rows.Scan(&name, &column, &referencedTable, &referencedColumn, &deleteAction, &updateAction, &matchType, &referencedInCurrentSchema, &deferrable, &initiallyDeferred, &deleteSetColumns, &validated, &enforced, &temporal); err != nil {
			return nil, fmt.Errorf("inspect: scan foreign key: %w", err)
		}
		if matchType != "s" {
			return nil, fmt.Errorf("inspect: foreign key %q cannot be represented: rasql supports only MATCH SIMPLE foreign keys", name)
		}
		if !referencedInCurrentSchema {
			return nil, fmt.Errorf("inspect: foreign key %q cannot be represented: rasql supports references only in the current schema", name)
		}
		if deferrable || initiallyDeferred {
			return nil, fmt.Errorf("inspect: foreign key %q cannot be represented: rasql supports only non-deferrable foreign keys", name)
		}
		if deleteSetColumns {
			return nil, fmt.Errorf("inspect: foreign key %q cannot be represented: rasql does not support column lists for ON DELETE SET NULL or SET DEFAULT", name)
		}
		if !validated {
			return nil, fmt.Errorf("inspect: foreign key %q cannot be represented: rasql does not support NOT VALID foreign keys", name)
		}
		if !enforced {
			return nil, fmt.Errorf("inspect: foreign key %q cannot be represented: rasql does not support NOT ENFORCED foreign keys", name)
		}
		if temporal {
			return nil, fmt.Errorf("inspect: foreign key %q cannot be represented: rasql does not support temporal foreign keys", name)
		}
		onDelete, err := referenceAction(deleteAction)
		if err != nil {
			return nil, fmt.Errorf("inspect: foreign key %q: %w", name, err)
		}
		onUpdate, err := referenceAction(updateAction)
		if err != nil {
			return nil, fmt.Errorf("inspect: foreign key %q: %w", name, err)
		}
		if len(keys) == 0 || keys[len(keys)-1].Name != name {
			keys = append(keys, schema.ForeignKey{
				Name:            name,
				ReferencedTable: referencedTable,
				OnDelete:        onDelete,
				OnUpdate:        onUpdate,
			})
		}
		key := &keys[len(keys)-1]
		if key.ReferencedTable != referencedTable || key.OnDelete != onDelete || key.OnUpdate != onUpdate {
			return nil, fmt.Errorf("inspect: foreign key %q has inconsistent metadata", name)
		}
		key.Columns = append(key.Columns, column)
		key.ReferencedColumns = append(key.ReferencedColumns, referencedColumn)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("inspect: iterate foreign keys: %w", err)
	}
	return keys, nil
}

func referenceAction(code string) (schema.ReferenceAction, error) {
	switch code {
	case "a":
		return schema.ReferenceActionNoAction, nil
	case "r":
		return schema.ReferenceActionRestrict, nil
	case "c":
		return schema.ReferenceActionCascade, nil
	case "n":
		return schema.ReferenceActionSetNull, nil
	case "d":
		return schema.ReferenceActionSetDefault, nil
	default:
		return "", fmt.Errorf("unsupported reference action %q", code)
	}
}

type informationQueries struct {
	columns                         string
	primaryKey                      string
	uniqueConstraints               string
	checks                          string
	unsupportedExclusionConstraints string
	unsupportedIndexes              string
	indexes                         string
	foreignKeys                     string
}

func (informationQueries) argument(tableName string) any {
	return tableName
}

// informationSchemaQueries returns the metadata queries for dialects that
// have no unfiltered catalog to fall back on. MySQL's information_schema.columns
// and information_schema.key_column_usage carry the same per-column privilege
// filter PostgreSQL's do, but MySQL exposes no pg_catalog equivalent that is
// both unfiltered and reveals the true column count. A role with a partial
// column grant can therefore make MySQL inspection silently under-report a
// table's columns or primary key, with no query this package can run to
// detect it. This is a known limitation, not something planned here.
func informationSchemaQueries(name string) (informationQueries, error) {
	switch name {
	case "mysql":
		return informationQueries{
			columns:           "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position",
			primaryKey:        "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position",
			uniqueConstraints: "SELECT key_column_usage.constraint_name, key_column_usage.column_name, FALSE, FALSE, FALSE, FALSE, FALSE, FALSE FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'UNIQUE' ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position",
			checks:            "SELECT check_constraints.constraint_name, check_constraints.check_clause, FALSE, TRUE, table_constraints.enforced = 'YES' FROM information_schema.check_constraints JOIN information_schema.table_constraints ON table_constraints.constraint_name = check_constraints.constraint_name AND table_constraints.table_schema = check_constraints.constraint_schema WHERE check_constraints.constraint_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'CHECK' ORDER BY check_constraints.constraint_name",
			indexes:           "SELECT index_name, 0, column_name FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name <> 'PRIMARY' AND non_unique = 1 ORDER BY index_name, seq_in_index",
			foreignKeys:       "SELECT key_column_usage.constraint_name, key_column_usage.column_name, key_column_usage.referenced_table_name, key_column_usage.referenced_column_name, CASE referential_constraints.delete_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.delete_rule END, CASE referential_constraints.update_rule WHEN 'NO ACTION' THEN 'a' WHEN 'RESTRICT' THEN 'r' WHEN 'CASCADE' THEN 'c' WHEN 'SET NULL' THEN 'n' WHEN 'SET DEFAULT' THEN 'd' ELSE referential_constraints.update_rule END, CASE referential_constraints.match_option WHEN 'NONE' THEN 's' ELSE referential_constraints.match_option END, key_column_usage.referenced_table_schema = DATABASE(), FALSE, FALSE, FALSE, TRUE, TRUE, FALSE FROM information_schema.key_column_usage JOIN information_schema.referential_constraints ON referential_constraints.constraint_schema = key_column_usage.constraint_schema AND referential_constraints.constraint_name = key_column_usage.constraint_name AND referential_constraints.table_name = key_column_usage.table_name WHERE key_column_usage.constraint_schema = DATABASE() AND key_column_usage.table_name = ? AND key_column_usage.referenced_table_name IS NOT NULL ORDER BY key_column_usage.constraint_name, key_column_usage.ordinal_position",
		}, nil
	default:
		return informationQueries{}, fmt.Errorf("inspect: unsupported dialect %q", name)
	}
}

const (
	postgreSQL15Version = 150000
	postgreSQL16Version = 160000
	postgreSQL18Version = 180000
)

// postgreSQLInformationQueries builds the PostgreSQL metadata queries for one
// server version. The primary-key query reads pg_catalog.pg_constraint
// instead of information_schema.table_constraints /
// information_schema.key_column_usage: per the SQL standard,
// information_schema.table_constraints exposes a constraint only to a role
// with INSERT, UPDATE, DELETE, TRUNCATE, REFERENCES or TRIGGER on the table,
// deliberately omitting SELECT, and key_column_usage carries the same
// per-column has_column_privilege filter as information_schema.columns. A
// plain read-only "GRANT SELECT" role therefore sees every column through
// information_schema.columns but an empty primary key through that path,
// with no error. pg_catalog.pg_constraint carries no such filter and is
// readable by PUBLIC, like the unique-constraint, check, index and
// foreign-key queries below. unnest(conkey) WITH ORDINALITY preserves the
// same column order information_schema.key_column_usage.ordinal_position
// gave: both come from the position of each column within the constraint's
// key array, which matters because a primary key's column order is part of
// its identity.
func postgreSQLInformationQueries(version int) informationQueries {
	nullsNotDistinct := postgreSQLCatalogBoolean(version, postgreSQL15Version, "index_metadata.indnullsnotdistinct")
	deleteSetColumns := postgreSQLCatalogBoolean(version, postgreSQL16Version, "constraint_data.confdelsetcols IS NOT NULL")
	enforced := "TRUE"
	if version >= postgreSQL18Version {
		enforced = "constraint_data.conenforced"
	}
	temporal := postgreSQLCatalogBoolean(version, postgreSQL18Version, "constraint_data.conperiod")

	return informationQueries{
		columns:                         "SELECT column_name, data_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position",
		primaryKey:                      "SELECT attribute.attname FROM pg_catalog.pg_constraint AS constraint_data JOIN pg_catalog.pg_class AS table_data ON table_data.oid = constraint_data.conrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace JOIN LATERAL unnest(constraint_data.conkey) WITH ORDINALITY AS key_column(attribute_number, ordinal_position) ON TRUE JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = constraint_data.conrelid AND attribute.attnum = key_column.attribute_number WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND constraint_data.contype = 'p' ORDER BY key_column.ordinal_position",
		uniqueConstraints:               "SELECT constraint_data.conname, attribute.attname, constraint_data.condeferrable, constraint_data.condeferred, " + nullsNotDistinct + ", index_metadata.indnkeyatts <> index_metadata.indnatts, " + temporal + ", index_data.reloptions IS NOT NULL OR index_data.reltablespace <> 0 OR index_metadata.indisreplident OR index_collation.collation_oid <> attribute.attcollation OR attribute.attcollation <> type_data.typcollation FROM pg_catalog.pg_constraint AS constraint_data JOIN pg_catalog.pg_class AS table_data ON table_data.oid = constraint_data.conrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace JOIN pg_catalog.pg_index AS index_metadata ON index_metadata.indexrelid = constraint_data.conindid JOIN pg_catalog.pg_class AS index_data ON index_data.oid = index_metadata.indexrelid JOIN LATERAL unnest(constraint_data.conkey) WITH ORDINALITY AS key_column(attribute_number, ordinal_position) ON TRUE JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = constraint_data.conrelid AND attribute.attnum = key_column.attribute_number JOIN pg_catalog.pg_type AS type_data ON type_data.oid = attribute.atttypid JOIN LATERAL unnest(index_metadata.indcollation::oid[]) WITH ORDINALITY AS index_collation(collation_oid, ordinal_position) ON index_collation.ordinal_position = key_column.ordinal_position WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND constraint_data.contype = 'u' ORDER BY constraint_data.conname, key_column.ordinal_position",
		checks:                          "SELECT constraint_data.conname, pg_catalog.pg_get_expr(constraint_data.conbin, constraint_data.conrelid, true), constraint_data.connoinherit, constraint_data.convalidated, " + enforced + " FROM pg_catalog.pg_constraint AS constraint_data JOIN pg_catalog.pg_class AS table_data ON table_data.oid = constraint_data.conrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND constraint_data.contype = 'c' ORDER BY constraint_data.conname",
		unsupportedExclusionConstraints: "SELECT constraint_data.conname FROM pg_catalog.pg_constraint AS constraint_data JOIN pg_catalog.pg_class AS table_data ON table_data.oid = constraint_data.conrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND constraint_data.contype = 'x' ORDER BY constraint_data.conname",
		unsupportedIndexes:              "SELECT index_data.relname FROM pg_catalog.pg_index AS index_metadata JOIN pg_catalog.pg_class AS table_data ON table_data.oid = index_metadata.indrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace JOIN pg_catalog.pg_class AS index_data ON index_data.oid = index_metadata.indexrelid JOIN pg_catalog.pg_am AS access_method ON access_method.oid = index_data.relam WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint AS constraint_data WHERE constraint_data.conindid = index_metadata.indexrelid) AND (NOT index_metadata.indisvalid OR index_metadata.indexprs IS NOT NULL OR index_metadata.indpred IS NOT NULL OR index_metadata.indnkeyatts <> index_metadata.indnatts OR access_method.amname <> 'btree' OR index_data.reloptions IS NOT NULL OR index_data.reltablespace <> 0 OR " + nullsNotDistinct + " OR index_metadata.indisreplident OR EXISTS (SELECT 1 FROM unnest(index_metadata.indoption::smallint[]) AS index_option(value) WHERE index_option.value <> 0) OR EXISTS (SELECT 1 FROM unnest(index_metadata.indkey::smallint[]) WITH ORDINALITY AS key_column(attribute_number, ordinal_position) JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = index_metadata.indrelid AND attribute.attnum = key_column.attribute_number JOIN pg_catalog.pg_type AS type_data ON type_data.oid = attribute.atttypid JOIN LATERAL unnest(index_metadata.indclass::oid[]) WITH ORDINALITY AS operator_class(operator_class_oid, ordinal_position) ON operator_class.ordinal_position = key_column.ordinal_position JOIN pg_catalog.pg_opclass AS operator_class_metadata ON operator_class_metadata.oid = operator_class.operator_class_oid JOIN LATERAL unnest(index_metadata.indcollation::oid[]) WITH ORDINALITY AS index_collation(collation_oid, ordinal_position) ON index_collation.ordinal_position = key_column.ordinal_position WHERE NOT operator_class_metadata.opcdefault OR index_collation.collation_oid <> attribute.attcollation OR attribute.attcollation <> type_data.typcollation)) ORDER BY index_data.relname",
		indexes:                         "SELECT index_data.relname, index_metadata.indisunique, attribute.attname FROM pg_catalog.pg_index AS index_metadata JOIN pg_catalog.pg_class AS table_data ON table_data.oid = index_metadata.indrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace JOIN pg_catalog.pg_class AS index_data ON index_data.oid = index_metadata.indexrelid JOIN pg_catalog.pg_am AS access_method ON access_method.oid = index_data.relam JOIN LATERAL unnest(index_metadata.indkey::smallint[]) WITH ORDINALITY AS key_column(attribute_number, ordinal_position) ON TRUE JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = index_metadata.indrelid AND attribute.attnum = key_column.attribute_number WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint AS constraint_data WHERE constraint_data.conindid = index_metadata.indexrelid) AND index_metadata.indisvalid AND index_metadata.indexprs IS NULL AND index_metadata.indpred IS NULL AND index_metadata.indnkeyatts = index_metadata.indnatts AND access_method.amname = 'btree' AND index_data.reloptions IS NULL AND index_data.reltablespace = 0 AND NOT index_metadata.indisreplident AND NOT EXISTS (SELECT 1 FROM unnest(index_metadata.indoption::smallint[]) AS index_option(value) WHERE index_option.value <> 0) ORDER BY index_data.relname, key_column.ordinal_position",
		foreignKeys:                     "SELECT constraint_data.conname, local_attribute.attname, referenced_table.relname, referenced_attribute.attname, constraint_data.confdeltype, constraint_data.confupdtype, constraint_data.confmatchtype, referenced_namespace.nspname = current_schema(), constraint_data.condeferrable, constraint_data.condeferred, " + deleteSetColumns + ", constraint_data.convalidated, " + enforced + ", " + temporal + " FROM pg_catalog.pg_constraint AS constraint_data JOIN pg_catalog.pg_class AS table_data ON table_data.oid = constraint_data.conrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace JOIN pg_catalog.pg_class AS referenced_table ON referenced_table.oid = constraint_data.confrelid JOIN pg_catalog.pg_namespace AS referenced_namespace ON referenced_namespace.oid = referenced_table.relnamespace JOIN LATERAL unnest(constraint_data.conkey) WITH ORDINALITY AS local_key(attribute_number, ordinal_position) ON TRUE JOIN LATERAL unnest(constraint_data.confkey) WITH ORDINALITY AS referenced_key(attribute_number, ordinal_position) ON referenced_key.ordinal_position = local_key.ordinal_position JOIN pg_catalog.pg_attribute AS local_attribute ON local_attribute.attrelid = constraint_data.conrelid AND local_attribute.attnum = local_key.attribute_number JOIN pg_catalog.pg_attribute AS referenced_attribute ON referenced_attribute.attrelid = constraint_data.confrelid AND referenced_attribute.attnum = referenced_key.attribute_number WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND constraint_data.contype = 'f' ORDER BY constraint_data.conname, local_key.ordinal_position",
	}
}

func postgreSQLCatalogBoolean(version int, introducedVersion int, column string) string {
	if version >= introducedVersion {
		return column
	}
	return "FALSE"
}

// normalizeType maps one native column type to a concrete schema column type.
// Only MySQL can report an unsigned column. PostgreSQL has no unsigned integer
// type, and SQLite stores a signed 64-bit value whatever a column is declared.
func normalizeType(dialectName string, databaseType string) (schema.ColumnType, error) {
	typeName := strings.ToUpper(strings.TrimSpace(databaseType))
	switch dialectName {
	case "postgresql":
		switch typeName {
		case "BOOLEAN":
			return schema.BooleanType{}, nil
		case "SMALLINT", "INTEGER", "BIGINT":
			return schema.IntegerType{}, nil
		case "REAL", "DOUBLE PRECISION":
			return schema.FloatType{}, nil
		case "NUMERIC", "DECIMAL":
			return schema.DecimalType{}, nil
		case "TEXT", "CHARACTER VARYING", "CHARACTER", "VARCHAR", "CHAR":
			return schema.TextType{}, nil
		case "BYTEA":
			return schema.BytesType{}, nil
		case "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITHOUT TIME ZONE", "DATE", "TIME WITH TIME ZONE", "TIME WITHOUT TIME ZONE":
			return schema.TimeType{}, nil
		case "JSON", "JSONB":
			return schema.JSONType{}, nil
		case "UUID":
			return schema.UUIDType{}, nil
		}
	case "mysql":
		return normalizeMySQLType(typeName, databaseType)
	case "sqlite":
		switch {
		case strings.Contains(typeName, "DECIMAL") || strings.Contains(typeName, "NUMERIC"):
			return nil, fmt.Errorf("exact decimal type %q is not exact in SQLite: a NUMERIC-affinity column stores REAL, so declare the column TEXT", databaseType)
		case strings.Contains(typeName, "BOOL"):
			return schema.BooleanType{}, nil
		case strings.Contains(typeName, "INT"):
			return schema.IntegerType{}, nil
		case strings.Contains(typeName, "CHAR") || strings.Contains(typeName, "CLOB") || strings.Contains(typeName, "TEXT"):
			return schema.TextType{}, nil
		case strings.Contains(typeName, "BLOB") || typeName == "":
			return schema.BytesType{}, nil
		case strings.Contains(typeName, "REAL") || strings.Contains(typeName, "FLOA") || strings.Contains(typeName, "DOUB"):
			return schema.FloatType{}, nil
		case strings.Contains(typeName, "JSON"):
			return schema.JSONType{}, nil
		case strings.Contains(typeName, "DATE") || strings.Contains(typeName, "TIME"):
			return schema.TimeType{}, nil
		case strings.Contains(typeName, "UUID"):
			return schema.UUIDType{}, nil
		}
	}
	return nil, fmt.Errorf("unsupported %s type %q", dialectName, databaseType)
}

// normalizeMySQLType maps one MySQL COLUMN_TYPE, already upper-cased as
// typeName, to a logical type and its signedness. databaseType is the original
// catalog text, quoted back in errors.
func normalizeMySQLType(typeName string, databaseType string) (schema.ColumnType, error) {
	decimal, err := mysqlDecimalDeclaration(typeName, databaseType)
	if err != nil {
		return nil, err
	}
	if decimal {
		return schema.DecimalType{}, nil
	}
	// BOOLEAN, BOOL and TINYINT(1) are the same MySQL column, and the catalog
	// spells it TINYINT(1). It is matched before the integer declaration so
	// that spelling stays a boolean; TINYINT(1) UNSIGNED is a different column
	// and remains an integer, as it was before signedness was recorded.
	if typeName == "BOOLEAN" || typeName == "BOOL" || typeName == "TINYINT(1)" {
		return schema.BooleanType{}, nil
	}
	integer, unsigned, err := mysqlIntegerDeclaration(typeName, databaseType)
	if err != nil {
		return nil, err
	}
	if integer {
		return schema.IntegerType{Unsigned: unsigned}, nil
	}
	switch {
	case strings.Contains(typeName, "FLOAT") || strings.Contains(typeName, "DOUBLE"):
		return schema.FloatType{}, nil
	case strings.Contains(typeName, "BLOB") || strings.Contains(typeName, "BINARY"):
		return schema.BytesType{}, nil
	case typeName == "JSON":
		return schema.JSONType{}, nil
	case strings.Contains(typeName, "DATE") || strings.Contains(typeName, "TIME"):
		return schema.TimeType{}, nil
	case strings.Contains(typeName, "CHAR") || strings.Contains(typeName, "TEXT") || strings.Contains(typeName, "ENUM") || strings.Contains(typeName, "SET"):
		return schema.TextType{}, nil
	}
	return nil, fmt.Errorf("unsupported mysql type %q", databaseType)
}

// mysqlDecimalDeclaration reports whether typeName, an upper-cased MySQL
// COLUMN_TYPE, declares an exact decimal column this package can represent.
// COLUMN_TYPE is a whole type declaration rather than a bare type name, so it
// is matched as a whole: a substring test would accept catalog text such as
// FOODECIMALBAR, and the catalog is read from a server the application may not
// control. Only DECIMAL or NUMERIC, optionally followed by (precision) or
// (precision, scale), is a decimal.
//
// A DECIMAL or NUMERIC declaration carrying UNSIGNED, ZEROFILL or any other
// trailing modifier returns an error instead of a logical type.
// schema.IntegerType.Unsigned states the signedness of an integer column only, so a
// decimal's UNSIGNED still has nowhere to be recorded, and it narrows the
// values the column permits, so a descriptor that dropped it would re-render as
// a column with a different meaning. Anything that is not a decimal declaration
// at all reports false with no error, leaving the caller's remaining type
// matches to run.
func mysqlDecimalDeclaration(typeName string, databaseType string) (bool, error) {
	base, rest := splitMySQLDeclaration(typeName)
	if base != "DECIMAL" && base != "NUMERIC" {
		return false, nil
	}

	if strings.HasPrefix(rest, "(") {
		end := strings.Index(rest, ")")
		if end < 0 || !validDecimalArguments(rest[1:end]) {
			return false, fmt.Errorf("unsupported mysql type %q: a decimal column must be declared %s(precision, scale)", databaseType, base)
		}
		rest = strings.TrimSpace(rest[end+1:])
	}
	if rest != "" {
		return false, fmt.Errorf("mysql type %q cannot be represented: a decimal column must carry no %s modifier, because rasql cannot record one and re-rendering the column without it would change the values it permits", databaseType, rest)
	}
	return true, nil
}

// mysqlIntegerDeclaration reports whether typeName, an upper-cased MySQL
// COLUMN_TYPE, declares an integer column this package can represent, and
// whether that column is UNSIGNED. The two results are, in order, whether the
// declaration is an integer at all and whether it is unsigned; a declaration
// that is not an integer reports false, false and no error, leaving the
// caller's remaining type matches to run.
//
// Like the decimal case, COLUMN_TYPE is matched as a whole declaration rather
// than by substring. A substring test on "INT" accepts MySQL's own POINT and
// MULTIPOINT, which are not integers at all, and it cannot see the UNSIGNED
// that follows the type: that is exactly how a BIGINT UNSIGNED column used to
// become a plain signed BIGINT, losing every value above 9223372036854775807.
// Only TINYINT, SMALLINT, MEDIUMINT, INT, INTEGER or BIGINT, optionally
// followed by a display width and then by UNSIGNED, is an integer here.
//
// ZEROFILL and any other trailing modifier returns an error instead, because
// schema.Column cannot record it and a descriptor that dropped it would
// re-render as a column with a different meaning.
func mysqlIntegerDeclaration(typeName string, databaseType string) (bool, bool, error) {
	base, rest := splitMySQLDeclaration(typeName)
	switch base {
	case "TINYINT", "SMALLINT", "MEDIUMINT", "INT", "INTEGER", "BIGINT":
	default:
		return false, false, nil
	}

	if strings.HasPrefix(rest, "(") {
		end := strings.Index(rest, ")")
		if end < 0 || !validDigitRun(rest[1:end]) {
			return false, false, fmt.Errorf("unsupported mysql type %q: an integer column must be declared %s or %s(width)", databaseType, base, base)
		}
		rest = strings.TrimSpace(rest[end+1:])
	}
	if rest == "UNSIGNED" {
		return true, true, nil
	}
	if rest != "" {
		return false, false, fmt.Errorf("mysql type %q cannot be represented: an integer column must carry no %s modifier, because rasql cannot record one and re-rendering the column without it would change the values it permits", databaseType, rest)
	}
	return true, false, nil
}

// splitMySQLDeclaration cuts an upper-cased MySQL COLUMN_TYPE into its bare
// type name and whatever follows it: the arguments in parentheses, the
// modifiers after them, or both. The remainder is returned with surrounding
// space trimmed, so a caller compares it against a modifier directly.
func splitMySQLDeclaration(typeName string) (string, string) {
	index := strings.IndexAny(typeName, " (")
	if index < 0 {
		return typeName, ""
	}
	return typeName[:index], strings.TrimSpace(typeName[index:])
}

// validDecimalArguments reports whether arguments is the inside of a MySQL
// decimal type's parentheses: a digit run for the precision, optionally
// followed by a comma and a digit run for the scale.
func validDecimalArguments(arguments string) bool {
	parts := strings.Split(arguments, ",")
	if len(parts) > 2 {
		return false
	}
	for _, part := range parts {
		if !validDigitRun(part) {
			return false
		}
	}
	return true
}

// validDigitRun reports whether value, once surrounding space is trimmed, is a
// non-empty run of decimal digits.
func validDigitRun(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" {
		return false
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

func text(value any) string {
	switch value := value.(type) {
	case nil:
		return ""
	case string:
		return value
	case []byte:
		return string(value)
	default:
		return fmt.Sprint(value)
	}
}

func isNil(value any) bool {
	if value == nil {
		return true
	}
	reflectValue := reflect.ValueOf(value)
	switch reflectValue.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflectValue.IsNil()
	default:
		return false
	}
}
