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

// Table reads the columns and primary key for tableName.
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
	table, err := schema.NewTable(schema.Table{Name: tableName, Columns: columns, PrimaryKey: primaryKey})
	if err != nil {
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
		column := schema.Column{Name: name, Type: logicalType, Nullable: notNull == 0, Default: text(defaultValue)}
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
	table, err := schema.NewTable(schema.Table{Name: tableName, Columns: columns, PrimaryKey: primaryKey})
	if err != nil {
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

type informationQueries struct {
	columns    string
	primaryKey string
	named      bool
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
			columns:    "SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = current_schema() AND table_name = $1 ORDER BY ordinal_position",
			primaryKey: "SELECT key_column_usage.column_name FROM information_schema.table_constraints JOIN information_schema.key_column_usage ON table_constraints.constraint_name = key_column_usage.constraint_name AND table_constraints.table_schema = key_column_usage.table_schema WHERE table_constraints.table_schema = current_schema() AND table_constraints.table_name = $1 AND table_constraints.constraint_type = 'PRIMARY KEY' ORDER BY key_column_usage.ordinal_position",
		}, nil
	case "mysql":
		return informationQueries{
			columns:    "SELECT column_name, data_type, is_nullable, column_default FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = ? ORDER BY ordinal_position",
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
