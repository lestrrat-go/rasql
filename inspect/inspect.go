// Package inspect reads live database metadata into schema descriptors.
package inspect

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
)

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
	if queryer == nil {
		return Inspector{}, fmt.Errorf("inspect: queryer must not be nil")
	}
	if d == nil {
		return Inspector{}, fmt.Errorf("inspect: dialect must not be nil")
	}
	return Inspector{queryer: queryer, dialect: d}, nil
}

// Table reads the supported schema metadata for tableName.
func (i Inspector) Table(ctx context.Context, tableName string) (schema.Table, error) {
	if err := schema.ValidateIdentifier(tableName); err != nil {
		return schema.Table{}, fmt.Errorf("inspect: invalid table name: %w", err)
	}
	if i.queryer == nil || i.dialect == nil {
		return schema.Table{}, fmt.Errorf("inspect: invalid inspector")
	}
	if i.dialect.Name() == "sqlite" {
		return i.sqliteTable(ctx, tableName)
	}
	return i.informationSchemaTable(ctx, tableName)
}

func (i Inspector) informationSchemaTable(ctx context.Context, tableName string) (schema.Table, error) {
	queries, err := informationSchemaQueries(i.dialect.Name())
	if err != nil {
		return schema.Table{}, err
	}
	columns, err := i.readColumns(ctx, queries.columns, queries.argument(tableName))
	if err != nil {
		return schema.Table{}, err
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

func (i Inspector) sqliteTable(ctx context.Context, tableName string) (schema.Table, error) {
	query := "PRAGMA table_info(\"" + tableName + "\")"
	rows, err := i.queryer.QueryContext(ctx, query)
	if err != nil {
		return schema.Table{}, fmt.Errorf("inspect: read SQLite columns: %w", err)
	}
	defer rows.Close()

	type primaryColumn struct {
		position int64
		name     string
	}
	columns := make([]schema.Column, 0)
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
		logicalType, err := normalizeType(i.dialect.Name(), databaseType)
		if err != nil {
			return schema.Table{}, fmt.Errorf("inspect: column %q: %w", name, err)
		}
		column := schema.Column{Name: name, Type: logicalType, Nullable: notNull == 0 && primaryPosition == 0, Default: text(defaultValue)}
		columns = append(columns, column)
		if primaryPosition > 0 {
			primaryColumns = append(primaryColumns, primaryColumn{position: primaryPosition, name: name})
		}
	}
	if err := rows.Err(); err != nil {
		return schema.Table{}, fmt.Errorf("inspect: iterate SQLite columns: %w", err)
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

func (i Inspector) readColumns(ctx context.Context, query string, argument any) ([]schema.Column, error) {
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("inspect: read columns: %w", err)
	}
	defer rows.Close()

	columns := make([]schema.Column, 0)
	for rows.Next() {
		var name string
		var databaseType string
		var nullable string
		var defaultValue any
		if err := rows.Scan(&name, &databaseType, &nullable, &defaultValue); err != nil {
			return nil, fmt.Errorf("inspect: scan column: %w", err)
		}
		logicalType, err := normalizeType(i.dialect.Name(), databaseType)
		if err != nil {
			return nil, fmt.Errorf("inspect: column %q: %w", name, err)
		}
		columns = append(columns, schema.Column{
			Name:     name,
			Type:     logicalType,
			Nullable: strings.EqualFold(nullable, "YES"),
			Default:  text(defaultValue),
		})
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
	defer rows.Close()

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
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("inspect: read unique constraints: %w", err)
	}
	defer rows.Close()

	var constraints []schema.UniqueConstraint
	for rows.Next() {
		var name string
		var column string
		var deferrable bool
		var initiallyDeferred bool
		var nullsNotDistinct bool
		if err := rows.Scan(&name, &column, &deferrable, &initiallyDeferred, &nullsNotDistinct); err != nil {
			return nil, fmt.Errorf("inspect: scan unique constraint: %w", err)
		}
		if deferrable || initiallyDeferred || nullsNotDistinct {
			return nil, fmt.Errorf("inspect: unique constraint %q cannot be represented: rasql supports only non-deferrable unique constraints with distinct nulls", name)
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
	defer rows.Close()

	var checks []schema.CheckConstraint
	for rows.Next() {
		var name string
		var expression string
		var noInherit bool
		if err := rows.Scan(&name, &expression, &noInherit); err != nil {
			return nil, fmt.Errorf("inspect: scan check constraint: %w", err)
		}
		if noInherit {
			return nil, fmt.Errorf("inspect: check constraint %q cannot be represented: rasql does not support NO INHERIT check constraints", name)
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
	defer rows.Close()

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
	return fmt.Errorf("inspect: index %q cannot be represented: rasql supports only non-partial B-tree indexes with simple ascending columns, no included columns, default operator classes and collations, and distinct nulls", name)
}

func (i Inspector) readIndexes(ctx context.Context, query string, argument any) ([]schema.Index, error) {
	rows, err := i.queryer.QueryContext(ctx, query, argument)
	if err != nil {
		return nil, fmt.Errorf("inspect: read indexes: %w", err)
	}
	defer rows.Close()

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
	defer rows.Close()

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
		if err := rows.Scan(&name, &column, &referencedTable, &referencedColumn, &deleteAction, &updateAction, &matchType, &referencedInCurrentSchema, &deferrable, &initiallyDeferred, &deleteSetColumns); err != nil {
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
	columns            string
	primaryKey         string
	uniqueConstraints  string
	checks             string
	unsupportedIndexes string
	indexes            string
	foreignKeys        string
	named              bool
}

func (q informationQueries) argument(tableName string) any {
	if q.named {
		return sql.Named("table_name", tableName)
	}
	return tableName
}

func informationSchemaQueries(name string) (informationQueries, error) {
	switch name {
	case "postgresql":
		return informationQueries{
			columns:            "SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position",
			primaryKey:         "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema AND table_constraints.table_name = key_column_usage.table_name WHERE table_constraints.table_schema = current_schema() AND table_constraints.table_name = $1 AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position",
			uniqueConstraints:  "SELECT constraint_data.conname, attribute.attname, constraint_data.condeferrable, constraint_data.condeferred, index_metadata.indnullsnotdistinct FROM pg_catalog.pg_constraint AS constraint_data JOIN pg_catalog.pg_class AS table_data ON table_data.oid = constraint_data.conrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace JOIN pg_catalog.pg_index AS index_metadata ON index_metadata.indexrelid = constraint_data.conindid JOIN LATERAL unnest(constraint_data.conkey) WITH ORDINALITY AS key_column(attribute_number, ordinal_position) ON TRUE JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = constraint_data.conrelid AND attribute.attnum = key_column.attribute_number WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND constraint_data.contype = 'u' ORDER BY constraint_data.conname, key_column.ordinal_position",
			checks:             "SELECT constraint_data.conname, pg_catalog.pg_get_expr(constraint_data.conbin, constraint_data.conrelid, true), constraint_data.connoinherit FROM pg_catalog.pg_constraint AS constraint_data JOIN pg_catalog.pg_class AS table_data ON table_data.oid = constraint_data.conrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND constraint_data.contype = 'c' ORDER BY constraint_data.conname",
			unsupportedIndexes: "SELECT index_data.relname FROM pg_catalog.pg_index AS index_metadata JOIN pg_catalog.pg_class AS table_data ON table_data.oid = index_metadata.indrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace JOIN pg_catalog.pg_class AS index_data ON index_data.oid = index_metadata.indexrelid JOIN pg_catalog.pg_am AS access_method ON access_method.oid = index_data.relam WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint AS constraint_data WHERE constraint_data.conindid = index_metadata.indexrelid) AND (index_metadata.indexprs IS NOT NULL OR index_metadata.indpred IS NOT NULL OR index_metadata.indnkeyatts <> index_metadata.indnatts OR access_method.amname <> 'btree' OR index_metadata.indnullsnotdistinct OR EXISTS (SELECT 1 FROM unnest(index_metadata.indoption::smallint[]) AS index_option(value) WHERE index_option.value <> 0) OR EXISTS (SELECT 1 FROM unnest(index_metadata.indkey::smallint[]) WITH ORDINALITY AS key_column(attribute_number, ordinal_position) JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = index_metadata.indrelid AND attribute.attnum = key_column.attribute_number JOIN LATERAL unnest(index_metadata.indclass::oid[]) WITH ORDINALITY AS operator_class(operator_class_oid, ordinal_position) ON operator_class.ordinal_position = key_column.ordinal_position JOIN pg_catalog.pg_opclass AS operator_class_metadata ON operator_class_metadata.oid = operator_class.operator_class_oid JOIN LATERAL unnest(index_metadata.indcollation::oid[]) WITH ORDINALITY AS index_collation(collation_oid, ordinal_position) ON index_collation.ordinal_position = key_column.ordinal_position WHERE NOT operator_class_metadata.opcdefault OR index_collation.collation_oid <> attribute.attcollation)) ORDER BY index_data.relname",
			indexes:            "SELECT index_data.relname, index_metadata.indisunique, attribute.attname FROM pg_catalog.pg_index AS index_metadata JOIN pg_catalog.pg_class AS table_data ON table_data.oid = index_metadata.indrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace JOIN pg_catalog.pg_class AS index_data ON index_data.oid = index_metadata.indexrelid JOIN pg_catalog.pg_am AS access_method ON access_method.oid = index_data.relam JOIN LATERAL unnest(index_metadata.indkey::smallint[]) WITH ORDINALITY AS key_column(attribute_number, ordinal_position) ON TRUE JOIN pg_catalog.pg_attribute AS attribute ON attribute.attrelid = index_metadata.indrelid AND attribute.attnum = key_column.attribute_number WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND NOT EXISTS (SELECT 1 FROM pg_catalog.pg_constraint AS constraint_data WHERE constraint_data.conindid = index_metadata.indexrelid) AND index_metadata.indexprs IS NULL AND index_metadata.indpred IS NULL AND index_metadata.indnkeyatts = index_metadata.indnatts AND access_method.amname = 'btree' AND NOT EXISTS (SELECT 1 FROM unnest(index_metadata.indoption::smallint[]) AS index_option(value) WHERE index_option.value <> 0) ORDER BY index_data.relname, key_column.ordinal_position",
			foreignKeys:        "SELECT constraint_data.conname, local_attribute.attname, referenced_table.relname, referenced_attribute.attname, constraint_data.confdeltype, constraint_data.confupdtype, constraint_data.confmatchtype, referenced_namespace.nspname = current_schema(), constraint_data.condeferrable, constraint_data.condeferred, constraint_data.confdelsetcols IS NOT NULL FROM pg_catalog.pg_constraint AS constraint_data JOIN pg_catalog.pg_class AS table_data ON table_data.oid = constraint_data.conrelid JOIN pg_catalog.pg_namespace AS table_namespace ON table_namespace.oid = table_data.relnamespace JOIN pg_catalog.pg_class AS referenced_table ON referenced_table.oid = constraint_data.confrelid JOIN pg_catalog.pg_namespace AS referenced_namespace ON referenced_namespace.oid = referenced_table.relnamespace JOIN LATERAL unnest(constraint_data.conkey) WITH ORDINALITY AS local_key(attribute_number, ordinal_position) ON TRUE JOIN LATERAL unnest(constraint_data.confkey) WITH ORDINALITY AS referenced_key(attribute_number, ordinal_position) ON referenced_key.ordinal_position = local_key.ordinal_position JOIN pg_catalog.pg_attribute AS local_attribute ON local_attribute.attrelid = constraint_data.conrelid AND local_attribute.attnum = local_key.attribute_number JOIN pg_catalog.pg_attribute AS referenced_attribute ON referenced_attribute.attrelid = constraint_data.confrelid AND referenced_attribute.attnum = referenced_key.attribute_number WHERE table_namespace.nspname = current_schema() AND table_data.relname = $1 AND constraint_data.contype = 'f' ORDER BY constraint_data.conname, local_key.ordinal_position",
		}, nil
	case "mysql":
		return informationQueries{
			columns:    "SELECT column_name, column_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position",
			primaryKey: "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = DATABASE() AND table_constraints.table_name = ? AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position",
		}, nil
	case "spanner":
		return informationQueries{
			columns:    "SELECT COLUMN_NAME, SPANNER_TYPE, IS_NULLABLE, COLUMN_DEFAULT FROM INFORMATION_SCHEMA.COLUMNS WHERE TABLE_NAME = @table_name ORDER BY ORDINAL_POSITION",
			primaryKey: "SELECT INDEX_COLUMNS.COLUMN_NAME FROM INFORMATION_SCHEMA.INDEXES JOIN INFORMATION_SCHEMA.INDEX_COLUMNS ON INDEXES.INDEX_NAME = INDEX_COLUMNS.INDEX_NAME AND INDEXES.TABLE_NAME = INDEX_COLUMNS.TABLE_NAME WHERE INDEXES.TABLE_NAME = @table_name AND INDEXES.INDEX_TYPE = 'PRIMARY_KEY' ORDER BY INDEX_COLUMNS.ORDINAL_POSITION",
			named:      true,
		}, nil
	default:
		return informationQueries{}, fmt.Errorf("inspect: unsupported dialect %q", name)
	}
}

func normalizeType(dialectName string, databaseType string) (schema.LogicalType, error) {
	typeName := strings.ToUpper(strings.TrimSpace(databaseType))
	switch dialectName {
	case "postgresql":
		switch typeName {
		case "BOOLEAN":
			return schema.TypeBoolean, nil
		case "SMALLINT", "INTEGER", "BIGINT":
			return schema.TypeInteger, nil
		case "REAL", "DOUBLE PRECISION", "NUMERIC", "DECIMAL":
			return schema.TypeFloat, nil
		case "TEXT", "CHARACTER VARYING", "CHARACTER", "VARCHAR", "CHAR":
			return schema.TypeText, nil
		case "BYTEA":
			return schema.TypeBytes, nil
		case "TIMESTAMP WITH TIME ZONE", "TIMESTAMP WITHOUT TIME ZONE", "DATE", "TIME WITH TIME ZONE", "TIME WITHOUT TIME ZONE":
			return schema.TypeTime, nil
		case "JSON", "JSONB":
			return schema.TypeJSON, nil
		case "UUID":
			return schema.TypeUUID, nil
		}
	case "mysql":
		switch {
		case typeName == "BOOLEAN" || typeName == "BOOL" || typeName == "TINYINT(1)":
			return schema.TypeBoolean, nil
		case strings.Contains(typeName, "INT"):
			return schema.TypeInteger, nil
		case strings.Contains(typeName, "FLOAT") || strings.Contains(typeName, "DOUBLE") || strings.Contains(typeName, "DECIMAL") || strings.Contains(typeName, "NUMERIC"):
			return schema.TypeFloat, nil
		case strings.Contains(typeName, "BLOB") || strings.Contains(typeName, "BINARY"):
			return schema.TypeBytes, nil
		case typeName == "JSON":
			return schema.TypeJSON, nil
		case strings.Contains(typeName, "DATE") || strings.Contains(typeName, "TIME"):
			return schema.TypeTime, nil
		case strings.Contains(typeName, "CHAR") || strings.Contains(typeName, "TEXT") || strings.Contains(typeName, "ENUM") || strings.Contains(typeName, "SET"):
			return schema.TypeText, nil
		}
	case "sqlite":
		switch {
		case strings.Contains(typeName, "BOOL"):
			return schema.TypeBoolean, nil
		case strings.Contains(typeName, "INT"):
			return schema.TypeInteger, nil
		case strings.Contains(typeName, "CHAR") || strings.Contains(typeName, "CLOB") || strings.Contains(typeName, "TEXT"):
			return schema.TypeText, nil
		case strings.Contains(typeName, "BLOB") || typeName == "":
			return schema.TypeBytes, nil
		case strings.Contains(typeName, "REAL") || strings.Contains(typeName, "FLOA") || strings.Contains(typeName, "DOUB"):
			return schema.TypeFloat, nil
		case strings.Contains(typeName, "JSON"):
			return schema.TypeJSON, nil
		case strings.Contains(typeName, "DATE") || strings.Contains(typeName, "TIME"):
			return schema.TypeTime, nil
		case strings.Contains(typeName, "UUID"):
			return schema.TypeUUID, nil
		}
	case "spanner":
		switch typeName {
		case "BOOL":
			return schema.TypeBoolean, nil
		case "INT64":
			return schema.TypeInteger, nil
		case "FLOAT64":
			return schema.TypeFloat, nil
		case "BYTES", "BYTES(MAX)":
			return schema.TypeBytes, nil
		case "TIMESTAMP", "DATE":
			return schema.TypeTime, nil
		case "JSON":
			return schema.TypeJSON, nil
		case "STRING", "STRING(MAX)", "STRING(36)":
			return schema.TypeText, nil
		}
	}
	return "", fmt.Errorf("unsupported %s type %q", dialectName, databaseType)
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
