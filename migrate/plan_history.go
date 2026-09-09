package migrate

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
)

type resolvedPlanHistory struct {
	schema              string
	historyTable        schema.ObjectName
	legacyProgressTable schema.ObjectName
	planProgressTable   schema.ObjectName
	qualifiedPlanSQL    string
	dialectName         string
}

func resolvePlanHistory(ctx context.Context, connection *sql.Conn, prepared preparedChangePlan) (resolvedPlanHistory, error) {
	if connection == nil {
		return resolvedPlanHistory{}, fmt.Errorf("migrate: plan history requires a database connection")
	}
	planSchema := prepared.history.Schema()
	effectiveSchema := planSchema
	dialectValue, err := planHistoryDialect(prepared.profile.Engine)
	if err != nil {
		return resolvedPlanHistory{}, err
	}
	if prepared.profile.Engine == changeplan.SQLiteEngine {
		if effectiveSchema == "" {
			effectiveSchema = "main"
		}
		if effectiveSchema != "main" {
			return resolvedPlanHistory{}, fmt.Errorf("migrate: SQLite plan schema %q is not main", planSchema)
		}
	} else {
		query := "SELECT current_schema()"
		if prepared.profile.Engine == changeplan.MySQLEngine {
			query = "SELECT DATABASE()"
		}
		var observed sql.NullString
		if err := connection.QueryRowContext(ctx, query).Scan(&observed); err != nil {
			return resolvedPlanHistory{}, fmt.Errorf("migrate: resolve plan schema: %w", err)
		}
		if !observed.Valid || observed.String == "" {
			return resolvedPlanHistory{}, fmt.Errorf("migrate: database returned an empty plan schema")
		}
		if effectiveSchema == "" {
			effectiveSchema = observed.String
		} else if effectiveSchema != observed.String {
			return resolvedPlanHistory{}, fmt.Errorf("migrate: plan schema %q does not match database schema %q", effectiveSchema, observed.String)
		}
	}
	quotedSchema, err := dialectValue.QuoteIdentifier(effectiveSchema)
	if err != nil {
		return resolvedPlanHistory{}, fmt.Errorf("migrate: quote plan schema: %w", err)
	}
	quotedTable, err := dialectValue.QuoteIdentifier(prepared.progressName.bareName)
	if err != nil {
		return resolvedPlanHistory{}, fmt.Errorf("migrate: quote plan progress table: %w", err)
	}
	exclusionSchema := effectiveSchema
	if prepared.profile.Engine != changeplan.SQLiteEngine {
		exclusionSchema = ""
	}
	return resolvedPlanHistory{
		schema:              effectiveSchema,
		historyTable:        schema.ObjectName{Schema: exclusionSchema, Name: prepared.history.Table()},
		legacyProgressTable: schema.ObjectName{Schema: exclusionSchema, Name: prepared.history.Table() + "_progress"},
		planProgressTable:   schema.ObjectName{Schema: exclusionSchema, Name: prepared.progressName.bareName},
		qualifiedPlanSQL:    quotedSchema + "." + quotedTable,
		dialectName:         dialectValue.Name(),
	}, nil
}

func planHistoryDialect(engine changeplan.EngineID) (dialect.Dialect, error) {
	switch engine {
	case changeplan.PostgreSQLEngine:
		return dialect.PostgreSQL(), nil
	case changeplan.MySQLEngine:
		return dialect.MySQL(), nil
	case changeplan.SQLiteEngine:
		return dialect.SQLite(), nil
	default:
		return nil, fmt.Errorf("migrate: unsupported plan history engine %d", engine)
	}
}

func legacyProgressTableExists(ctx context.Context, queries queryer, history resolvedPlanHistory) (bool, error) {
	dialectValue, err := resolvedHistoryDialect(history)
	if err != nil {
		return false, err
	}
	placeholder, err := dialectValue.Placeholder(1)
	if err != nil {
		return false, err
	}
	query := ""
	args := []any{history.schema, history.legacyProgressTable.Name}
	switch dialectValue.Name() {
	case "sqlite":
		query = "SELECT 1 FROM " + quoteSQLiteCatalogSchema(history.schema) + ".sqlite_master WHERE type = 'table' AND name = " + placeholder
		args = args[1:]
	case "postgresql", "mysql":
		query = "SELECT 1 FROM information_schema.tables WHERE table_schema = " + placeholder + " AND table_name = " + mustPlaceholder(dialectValue, 2)
	default:
		return false, fmt.Errorf("migrate: unsupported plan history dialect %q", dialectValue.Name())
	}
	rows, err := queries.QueryContext(ctx, query, args...)
	if err != nil {
		return false, fmt.Errorf("migrate: inspect legacy progress table: %w", err)
	}
	if !rows.Next() {
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return false, fmt.Errorf("migrate: inspect legacy progress table: %w", err)
		}
		if err := rows.Close(); err != nil {
			return false, fmt.Errorf("migrate: close legacy progress rows: %w", err)
		}
		return false, nil
	}
	var marker int
	if err := rows.Scan(&marker); err != nil {
		_ = rows.Close()
		return false, fmt.Errorf("migrate: scan legacy progress table: %w", err)
	}
	if err := rows.Close(); err != nil {
		return false, fmt.Errorf("migrate: close legacy progress rows: %w", err)
	}
	return true, nil
}

func readLegacyProgressIfExists(ctx context.Context, runner Runner, queries queryer, history resolvedPlanHistory) (*progressEntry, error) {
	exists, err := legacyProgressTableExists(ctx, queries, history)
	if err != nil || !exists {
		return nil, err
	}
	return runner.progress(ctx, queries)
}

func planHistoryDialectByName(name string) (dialect.Dialect, error) {
	switch name {
	case "postgresql":
		return dialect.PostgreSQL(), nil
	case "mysql":
		return dialect.MySQL(), nil
	case "sqlite":
		return dialect.SQLite(), nil
	default:
		return nil, fmt.Errorf("migrate: unsupported plan history dialect %q", name)
	}
}

func resolvedHistoryDialect(history resolvedPlanHistory) (dialect.Dialect, error) {
	if history.dialectName != "" {
		return planHistoryDialectByName(history.dialectName)
	}
	return nil, fmt.Errorf("migrate: resolved plan history dialect is missing")
}

func quoteSQLiteCatalogSchema(name string) string {
	quoted, _ := dialect.SQLite().QuoteIdentifier(name)
	return quoted
}

func mustPlaceholder(d dialect.Dialect, position int) string {
	placeholder, _ := d.Placeholder(position)
	return placeholder
}
