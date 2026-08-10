// Package inspect reads live database metadata into schema descriptors.
//
// For PostgreSQL, MySQL, and SQLite, a table descriptor is either complete or
// an error: this package never returns a schema.Table silently missing columns.
// PostgreSQL and SQLite also reject a silently missing primary key. PostgreSQL's
// information_schema is filtered per column and per table by the inspecting
// role's privileges while pg_catalog is not;
// this package cross-checks the two and reports [IncompleteMetadataError] or
// [TableNotFoundError] instead of guessing. MySQL's information_schema is
// filtered too, so this package cross-checks its column count against the full
// definition returned by SHOW CREATE TABLE. SQLite has no privilege filtering.
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

	"github.com/go-sql-driver/mysql"
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
// [IncompleteMetadataError], so callers that only need to detect a privilege
// problem can use errors.Is instead of errors.As.
var ErrIncompleteMetadata = errors.New("inspect: incomplete table metadata")

// IncompleteMetadataError reports that the inspecting role sees fewer
// columns of Table than actually exist. PostgreSQL's information_schema.columns
// filters each row by has_column_privilege, so a role granted SELECT on only
// some columns, or none, gets a short or empty result with no error of its
// own; pg_catalog carries no such filter and reports the true count. MySQL
// applies the same column filter, and SHOW CREATE TABLE supplies the complete
// count for the comparison. Visible and Actual let a caller distinguish this
// from a schema rasql genuinely cannot represent: a privilege gap is fixed
// with GRANT, a representability gap needs a schema change, and only one of
// those is this error.
type IncompleteMetadataError struct {
	// Table is the requested table name.
	Table string
	// Visible is the column count information_schema exposed to the
	// inspecting role.
	Visible int
	// Actual is the true column count reported by the database catalog.
	Actual int
	// Reason explains why the complete column count could not be verified when
	// the catalog returned no count that can be compared.
	Reason string
}

func (e *IncompleteMetadataError) Error() string {
	if e.Reason != "" {
		return fmt.Sprintf("inspect: table %q column metadata could not be verified: %s", e.Table, e.Reason)
	}
	return fmt.Sprintf("inspect: table %q column metadata could not be read: the complete metadata source reports %d columns but information_schema.columns exposed %d, so the inspecting role holds insufficient column privileges on the table", e.Table, e.Actual, e.Visible)
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

// Table reads the supported schema metadata for tableName. It returns
// [TableNotFoundError] when tableName does not exist and
// [IncompleteMetadataError] when the inspecting role's privileges hide some
// or all of the table's columns.
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
	} else if i.dialect.Name() == "mysql" {
		exists, err := i.mySQLCheckColumnVisibility(ctx, tableName, len(columns))
		if err != nil {
			return schema.Table{}, err
		}
		if !exists {
			return schema.Table{}, &TableNotFoundError{Table: tableName, Scope: "the current database"}
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
		if i.dialect.Name() == "mysql" {
			hasExpression, err := i.mysqlStatisticsHasExpression(ctx)
			if err != nil {
				return schema.Table{}, err
			}
			queries.mysqlIndexHasExpression = hasExpression
			if !hasExpression {
				queries.indexes = "SELECT index_name, non_unique = 0, column_name, sub_part, collation FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name <> 'PRIMARY' ORDER BY index_name, seq_in_index"
			}
		}
		table.Indexes, err = i.readIndexes(ctx, queries.indexes, queries.argument(tableName), queries.mysqlIndexHasExpression)
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

func (i Inspector) mysqlStatisticsHasExpression(ctx context.Context) (bool, error) {
	rows, err := i.queryer.QueryContext(ctx, "SHOW COLUMNS FROM information_schema.statistics LIKE 'EXPRESSION'")
	if err != nil {
		return false, fmt.Errorf("inspect: read MySQL statistics columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	hasExpression := rows.Next()
	if err := rows.Err(); err != nil {
		return false, fmt.Errorf("inspect: iterate MySQL statistics columns: %w", err)
	}
	return hasExpression, nil
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

// mySQLCheckColumnVisibility compares the information_schema column count
// with the complete CREATE TABLE definition. MySQL filters information_schema
// columns by the inspecting role's privileges, but SHOW CREATE TABLE returns
// the full definition when the role has any privilege on the table. The bool
// reports whether SHOW CREATE TABLE proved that the table exists.
func (i Inspector) mySQLCheckColumnVisibility(ctx context.Context, tableName string, visible int) (bool, error) {
	query := "SHOW CREATE TABLE `" + strings.ReplaceAll(tableName, "`", "``") + "`"
	rows, err := i.queryer.QueryContext(ctx, query)
	if err != nil {
		var mysqlErr *mysql.MySQLError
		if errors.As(err, &mysqlErr) && mysqlErr.Number == 1146 {
			return false, nil
		}
		return true, fmt.Errorf("inspect: read MySQL table %q definition: %w", tableName, err)
	}
	defer func() { _ = rows.Close() }()

	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return true, fmt.Errorf("inspect: iterate MySQL table %q definition: %w", tableName, err)
		}
		return false, nil
	}
	var returnedTable string
	var definition string
	if err := rows.Scan(&returnedTable, &definition); err != nil {
		return true, &IncompleteMetadataError{
			Table:   tableName,
			Visible: visible,
			Reason:  fmt.Sprintf("SHOW CREATE TABLE returned unreadable metadata: %v", err),
		}
	}
	if err := rows.Err(); err != nil {
		return true, fmt.Errorf("inspect: iterate MySQL table %q definition: %w", tableName, err)
	}
	if !strings.HasPrefix(strings.ToUpper(strings.TrimSpace(definition)), "CREATE TABLE ") {
		return true, nil
	}
	actual, err := mysqlCreateTableColumnCount(definition)
	if err != nil {
		return true, &IncompleteMetadataError{
			Table:   tableName,
			Visible: visible,
			Reason:  fmt.Sprintf("SHOW CREATE TABLE could not prove completeness: %v", err),
		}
	}
	if actual != visible {
		return true, &IncompleteMetadataError{Table: tableName, Visible: visible, Actual: actual}
	}
	return true, nil
}

// mysqlCreateTableColumnCount counts column definitions in the parenthesized
// portion of SHOW CREATE TABLE output. The server appends engine and other
// table options after that portion, so only balanced parentheses outside SQL
// quotes are considered. Views are left to the existing information_schema
// path because SHOW CREATE TABLE returns a CREATE VIEW statement for them.
func mysqlCreateTableColumnCount(definition string) (int, error) {
	trimmed := strings.TrimSpace(definition)
	if !strings.HasPrefix(strings.ToUpper(trimmed), "CREATE TABLE ") {
		return 0, nil
	}
	open := strings.IndexByte(trimmed, '(')
	if open < 0 {
		return 0, fmt.Errorf("CREATE TABLE definition has no column list")
	}
	close, err := mysqlMatchingParenthesis(trimmed, open)
	if err != nil {
		return 0, err
	}
	body := trimmed[open+1 : close]
	parts, err := mysqlTopLevelParts(body)
	if err != nil {
		return 0, err
	}
	columns := 0
	for _, part := range parts {
		if mysqlCreateTablePartIsColumn(part) {
			columns++
		}
	}
	return columns, nil
}

func mysqlMatchingParenthesis(input string, open int) (int, error) {
	depth := 1
	var quote byte
	for index := open + 1; index < len(input); index++ {
		character := input[index]
		if quote != 0 {
			if character == '\\' && quote != '`' {
				index++
				continue
			}
			if character == quote {
				if index+1 < len(input) && input[index+1] == quote {
					index++
					continue
				}
				quote = 0
			}
			continue
		}
		switch character {
		case '\'', '"', '`':
			quote = character
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return index, nil
			}
		}
	}
	return 0, fmt.Errorf("CREATE TABLE definition has unbalanced parentheses")
}

func mysqlTopLevelParts(input string) ([]string, error) {
	parts := make([]string, 0)
	start := 0
	depth := 0
	var quote byte
	for index := 0; index < len(input); index++ {
		character := input[index]
		if quote != 0 {
			if character == '\\' && quote != '`' {
				index++
				continue
			}
			if character == quote {
				if index+1 < len(input) && input[index+1] == quote {
					index++
					continue
				}
				quote = 0
			}
			continue
		}
		switch character {
		case '\'', '"', '`':
			quote = character
		case '(':
			depth++
		case ')':
			if depth == 0 {
				return nil, fmt.Errorf("CREATE TABLE definition has unbalanced parentheses")
			}
			depth--
		case ',':
			if depth == 0 {
				parts = append(parts, input[start:index])
				start = index + 1
			}
		}
	}
	if quote != 0 || depth != 0 {
		return nil, fmt.Errorf("CREATE TABLE definition has unterminated column metadata")
	}
	return append(parts, input[start:]), nil
}

func mysqlCreateTablePartIsColumn(part string) bool {
	part = strings.TrimSpace(part)
	if part == "" {
		return false
	}
	if part[0] == '`' || part[0] == '"' {
		return true
	}
	end := 0
	for end < len(part) && ((part[end] >= 'a' && part[end] <= 'z') || (part[end] >= 'A' && part[end] <= 'Z') || (part[end] >= '0' && part[end] <= '9') || part[end] == '_') {
		end++
	}
	word := strings.ToUpper(part[:end])
	switch word {
	case "CHECK", "CONSTRAINT", "FOREIGN", "FULLTEXT", "INDEX", "KEY", "PRIMARY", "SPATIAL", "UNIQUE":
		return false
	default:
		return end > 0
	}
}

func (i Inspector) sqliteTable(ctx context.Context, tableName string) (schema.Table, error) {
	queryer := i.queryer
	if database, ok := queryer.(*sql.DB); ok {
		connection, err := database.Conn(ctx)
		if err != nil {
			return schema.Table{}, fmt.Errorf("inspect: acquire SQLite connection: %w", err)
		}
		defer func() { _ = connection.Close() }()
		queryer = connection
	}
	return i.sqliteTableOnConnection(ctx, queryer, tableName)
}

func (i Inspector) sqliteTableOnConnection(ctx context.Context, queryer Queryer, tableName string) (schema.Table, error) {
	query := "PRAGMA table_info(\"" + tableName + "\")"
	rows, err := queryer.QueryContext(ctx, query)
	if err != nil {
		return schema.Table{}, fmt.Errorf("inspect: read SQLite columns: %w", err)
	}
	defer func() { _ = rows.Close() }()

	type sqliteColumn struct {
		name            string
		databaseType    string
		notNull         int64
		defaultValue    any
		primaryPosition int64
	}
	type primaryColumn struct {
		position int64
		name     string
	}
	metadata := make([]sqliteColumn, 0)
	primaryColumns := make([]primaryColumn, 0)
	for rows.Next() {
		var ordinal int64
		var name string
		var databaseType string
		var notNull int64
		var defaultValue any
		var primaryPosition int64
		if err := rows.Scan(&ordinal, &name, &databaseType, &notNull, &defaultValue, &primaryPosition); err != nil {
			return schema.Table{}, fmt.Errorf("inspect: scan SQLite column: %w", err)
		}
		metadata = append(metadata, sqliteColumn{
			name:            name,
			databaseType:    databaseType,
			notNull:         notNull,
			defaultValue:    defaultValue,
			primaryPosition: primaryPosition,
		})
		if primaryPosition > 0 {
			primaryColumns = append(primaryColumns, primaryColumn{position: primaryPosition, name: name})
		}
	}
	if err := rows.Err(); err != nil {
		return schema.Table{}, fmt.Errorf("inspect: iterate SQLite columns: %w", err)
	}
	_ = rows.Close()
	if len(metadata) == 0 {
		return schema.Table{}, &TableNotFoundError{Table: tableName, Scope: "the connection's attached databases"}
	}
	rowIDAlias := false
	for _, column := range metadata {
		if column.primaryPosition == 0 || column.notNull != 0 || !strings.EqualFold(strings.TrimSpace(column.databaseType), "INTEGER") {
			continue
		}
		rowIDAlias, err = i.sqliteRowIDAlias(ctx, queryer, tableName, column.name, len(primaryColumns))
		if err != nil {
			return schema.Table{}, err
		}
		break
	}
	columns := make([]schema.Column, 0, len(metadata))
	for _, column := range metadata {
		// The signedness result is discarded: SQLite's declared type carries
		// none this package can trust, because an INTEGER column stores a
		// signed 64-bit value however it was declared, so even a column
		// declared UNSIGNED BIG INT is signed storage. The descriptor records
		// that truth rather than the declaration.
		columnType, err := normalizeType(i.dialect.Name(), column.databaseType)
		if err != nil {
			return schema.Table{}, fmt.Errorf("inspect: column %q: %w", column.name, err)
		}
		columns = append(columns, schema.Column{
			Name:     column.name,
			Type:     columnType,
			Nullable: column.notNull == 0 && (!rowIDAlias || len(primaryColumns) != 1 || column.primaryPosition <= 0),
			Default:  text(column.defaultValue),
		})
	}
	sort.Slice(primaryColumns, func(left, right int) bool {
		return primaryColumns[left].position < primaryColumns[right].position
	})
	primaryKey := make([]string, len(primaryColumns))
	for index, column := range primaryColumns {
		primaryKey[index] = column.name
	}
	table := schema.Table{Name: tableName, Columns: columns, PrimaryKey: primaryKey}
	if err := table.Validate(); err != nil {
		return schema.Table{}, fmt.Errorf("inspect: normalize table %q: %w", tableName, err)
	}
	return table, nil
}

func (i Inspector) sqliteRowIDAlias(ctx context.Context, queryer Queryer, tableName, columnName string, primaryKeyColumns int) (bool, error) {
	if primaryKeyColumns != 1 {
		return false, nil
	}
	declaration, err := i.sqliteTableDeclaration(ctx, queryer, tableName)
	if err != nil {
		return false, err
	}
	return sqliteDeclarationHasRowIDAlias(declaration, columnName), nil
}

func (i Inspector) sqliteTableDeclaration(ctx context.Context, queryer Queryer, tableName string) (string, error) {
	for _, schemaName := range []string{"temp", "main"} {
		declaration, found, err := i.sqliteCatalogDeclaration(ctx, queryer, schemaName, tableName)
		if err != nil {
			return "", err
		}
		if found {
			return declaration, nil
		}
	}

	rows, err := queryer.QueryContext(ctx, "PRAGMA database_list")
	if err != nil {
		return "", fmt.Errorf("inspect: read SQLite database list: %w", err)
	}
	defer func() { _ = rows.Close() }()
	attachedSchemas := make([]string, 0)
	for rows.Next() {
		var sequence int64
		var schemaName string
		var filename string
		if err := rows.Scan(&sequence, &schemaName, &filename); err != nil {
			return "", fmt.Errorf("inspect: scan SQLite database list: %w", err)
		}
		if schemaName == "temp" || schemaName == "main" {
			continue
		}
		attachedSchemas = append(attachedSchemas, schemaName)
	}
	if err := rows.Err(); err != nil {
		return "", fmt.Errorf("inspect: iterate SQLite database list: %w", err)
	}
	_ = rows.Close()
	for _, schemaName := range attachedSchemas {
		declaration, found, err := i.sqliteCatalogDeclaration(ctx, queryer, schemaName, tableName)
		if err != nil {
			return "", err
		}
		if found {
			return declaration, nil
		}
	}
	return "", nil
}

func (i Inspector) sqliteCatalogDeclaration(ctx context.Context, queryer Queryer, schemaName, tableName string) (string, bool, error) {
	query := "SELECT sql FROM " + sqliteQuoteIdentifier(schemaName) + ".sqlite_master WHERE type = 'table' AND name = ? COLLATE NOCASE"
	rows, err := queryer.QueryContext(ctx, query, tableName)
	if err != nil {
		return "", false, fmt.Errorf("inspect: read SQLite %s table declaration: %w", schemaName, err)
	}
	defer func() { _ = rows.Close() }()
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			return "", false, fmt.Errorf("inspect: iterate SQLite %s table declaration: %w", schemaName, err)
		}
		return "", false, nil
	}
	var declaration sql.NullString
	if err := rows.Scan(&declaration); err != nil {
		return "", false, fmt.Errorf("inspect: scan SQLite %s table declaration: %w", schemaName, err)
	}
	if err := rows.Err(); err != nil {
		return "", false, fmt.Errorf("inspect: iterate SQLite %s table declaration: %w", schemaName, err)
	}
	return declaration.String, true, nil
}

func sqliteQuoteIdentifier(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

type sqliteDeclarationToken struct {
	text   string
	quoted bool
}

func sqliteDeclarationHasRowIDAlias(declaration, columnName string) bool {
	tokens, bodyStart, bodyEnd, ok := sqliteDeclarationTokens(declaration)
	if !ok || sqliteDeclarationHasWithoutRowID(tokens[bodyEnd+1:]) {
		return false
	}
	for _, definition := range sqliteDeclarationDefinitions(tokens[bodyStart+1 : bodyEnd]) {
		if sqliteDeclarationHasTablePrimaryKey(definition, columnName) {
			return true
		}
		if len(definition) < 4 || !sqliteDeclarationIdentifierEqual(definition[0], columnName) {
			continue
		}
		if !strings.EqualFold(definition[1].text, "INTEGER") || (len(definition) > 2 && !sqliteDeclarationConstraintStart(definition[2])) {
			continue
		}
		depth := 0
		for index := 2; index+1 < len(definition); index++ {
			switch definition[index].text {
			case "(":
				depth++
				continue
			case ")":
				depth--
				continue
			}
			if depth != 0 {
				continue
			}
			if definition[index].quoted || !strings.EqualFold(definition[index].text, "PRIMARY") || definition[index+1].quoted || !strings.EqualFold(definition[index+1].text, "KEY") {
				continue
			}
			return index+2 >= len(definition) || definition[index+2].quoted || !strings.EqualFold(definition[index+2].text, "DESC")
		}
	}
	return false
}

func sqliteDeclarationHasTablePrimaryKey(definition []sqliteDeclarationToken, columnName string) bool {
	index := 0
	if len(definition) > index && !definition[index].quoted && strings.EqualFold(definition[index].text, "CONSTRAINT") {
		index += 2
	}
	if index+3 >= len(definition) || definition[index].quoted || !strings.EqualFold(definition[index].text, "PRIMARY") || definition[index+1].quoted || !strings.EqualFold(definition[index+1].text, "KEY") || definition[index+2].text != "(" {
		return false
	}
	index += 3
	if index >= len(definition) || !sqliteDeclarationIdentifierEqual(definition[index], columnName) {
		return false
	}
	index++
	if index < len(definition) && !definition[index].quoted && strings.EqualFold(definition[index].text, "COLLATE") {
		index++
		if index >= len(definition) || definition[index].text == ")" {
			return false
		}
		index++
	}
	if index < len(definition) && !definition[index].quoted && (strings.EqualFold(definition[index].text, "ASC") || strings.EqualFold(definition[index].text, "DESC")) {
		index++
	}
	return index < len(definition) && definition[index].text == ")"
}

func sqliteDeclarationConstraintStart(token sqliteDeclarationToken) bool {
	if token.quoted {
		return false
	}
	switch strings.ToUpper(token.text) {
	case "CONSTRAINT", "NOT", "NULL", "DEFAULT", "PRIMARY", "UNIQUE", "CHECK", "REFERENCES", "COLLATE", "GENERATED", "ALWAYS", "AS":
		return true
	default:
		return false
	}
}

func sqliteDeclarationIdentifierEqual(token sqliteDeclarationToken, columnName string) bool {
	return strings.EqualFold(token.text, columnName)
}

func sqliteDeclarationHasWithoutRowID(tokens []sqliteDeclarationToken) bool {
	for index := 0; index+1 < len(tokens); index++ {
		if !tokens[index].quoted && strings.EqualFold(tokens[index].text, "WITHOUT") && !tokens[index+1].quoted && strings.EqualFold(tokens[index+1].text, "ROWID") {
			return true
		}
	}
	return false
}

func sqliteDeclarationDefinitions(tokens []sqliteDeclarationToken) [][]sqliteDeclarationToken {
	definitions := make([][]sqliteDeclarationToken, 0)
	start := 0
	depth := 0
	for index, token := range tokens {
		switch token.text {
		case "(":
			depth++
		case ")":
			depth--
		case ",":
			if depth == 0 {
				definitions = append(definitions, tokens[start:index])
				start = index + 1
			}
		}
	}
	return append(definitions, tokens[start:])
}

func sqliteDeclarationTokens(declaration string) ([]sqliteDeclarationToken, int, int, bool) {
	tokens := make([]sqliteDeclarationToken, 0)
	for index := 0; index < len(declaration); {
		switch declaration[index] {
		case ' ', '\t', '\n', '\r', '\f':
			index++
		case '-',
			'/':
			if index+1 < len(declaration) && declaration[index] == '-' {
				index += 2
				for index < len(declaration) && declaration[index] != '\n' {
					index++
				}
				continue
			}
			if index+1 < len(declaration) && declaration[index] == '/' && declaration[index+1] == '*' {
				index += 2
				for index+1 < len(declaration) && (declaration[index] != '*' || declaration[index+1] != '/') {
					index++
				}
				if index+1 >= len(declaration) {
					return nil, 0, 0, false
				}
				index += 2
				continue
			}
			fallthrough
		case '(', ')', ',':
			tokens = append(tokens, sqliteDeclarationToken{text: string(declaration[index])})
			index++
		case '\'', '"', '`', '[':
			quote := declaration[index]
			closeQuote := quote
			if quote == '[' {
				closeQuote = ']'
			}
			index++
			var value strings.Builder
			closed := false
			for index < len(declaration) {
				if declaration[index] == closeQuote {
					if index+1 < len(declaration) && declaration[index+1] == closeQuote {
						value.WriteByte(closeQuote)
						index += 2
						continue
					}
					index++
					tokens = append(tokens, sqliteDeclarationToken{text: value.String(), quoted: quote != '\''})
					closed = true
					break
				}
				value.WriteByte(declaration[index])
				index++
			}
			if !closed {
				return nil, 0, 0, false
			}
		default:
			start := index
			for index < len(declaration) && !strings.ContainsRune(" \t\n\r\f(),", rune(declaration[index])) && declaration[index] != '\'' && declaration[index] != '"' && declaration[index] != '`' && declaration[index] != '[' {
				if index+1 < len(declaration) && ((declaration[index] == '-' && declaration[index+1] == '-') || (declaration[index] == '/' && declaration[index+1] == '*')) {
					break
				}
				index++
			}
			tokens = append(tokens, sqliteDeclarationToken{text: declaration[start:index]})
		}
	}
	open := -1
	depth := 0
	close := -1
	for index, token := range tokens {
		if token.text == "(" {
			if open < 0 {
				open = index
			}
			depth++
		}
		if token.text == ")" {
			depth--
			if depth == 0 {
				close = index
				break
			}
		}
	}
	return tokens, open, close, open >= 0 && close > open
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

func (i Inspector) readIndexes(ctx context.Context, query string, argument any, mysqlIndexHasExpression bool) ([]schema.Index, error) {
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
		if i.dialect.Name() == "mysql" {
			var nullableColumn sql.NullString
			var prefixLength sql.NullInt64
			var expression sql.NullString
			var collation sql.NullString
			scanArgs := []any{&name, &unique, &nullableColumn, &prefixLength}
			if mysqlIndexHasExpression {
				scanArgs = append(scanArgs, &expression)
			}
			scanArgs = append(scanArgs, &collation)
			if err := rows.Scan(scanArgs...); err != nil {
				return nil, fmt.Errorf("inspect: scan index: %w", err)
			}
			if unique && strings.EqualFold(collation.String, "D") {
				return nil, fmt.Errorf("inspect: index %q cannot be represented: rasql does not support MySQL descending unique index parts", name)
			}
			if prefixLength.Valid {
				return nil, fmt.Errorf("inspect: index %q cannot be represented: rasql does not support MySQL prefix index parts", name)
			}
			if expression.Valid {
				return nil, fmt.Errorf("inspect: index %q cannot be represented: rasql does not support MySQL functional index parts", name)
			}
			if !nullableColumn.Valid {
				return nil, fmt.Errorf("inspect: index %q cannot be represented: rasql does not support MySQL non-column index parts", name)
			}
			column = nullableColumn.String
		} else if err := rows.Scan(&name, &unique, &column); err != nil {
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
	mysqlIndexHasExpression         bool
}

func (informationQueries) argument(tableName string) any {
	return tableName
}

// informationSchemaQueries returns the information_schema metadata queries
// used by MySQL. MySQL's information_schema.columns and
// information_schema.key_column_usage carry the same per-column privilege
// filter PostgreSQL's do; mySQLCheckColumnVisibility supplies the separate
// SHOW CREATE TABLE completeness check.
func informationSchemaQueries(name string) (informationQueries, error) {
	switch name {
	case "mysql":
		return informationQueries{
			columns:    "SELECT column_name, column_type, is_nullable, column_default, numeric_precision, numeric_scale FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position",
			primaryKey: "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position",
			indexes:    "SELECT index_name, non_unique = 0, column_name, sub_part, expression, collation FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = ? AND index_name <> 'PRIMARY' ORDER BY index_name, seq_in_index",
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
