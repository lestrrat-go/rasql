package conformance

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

func validateDatabaseSeedIdentity(ctx context.Context, db *sql.DB, engine string, document SignatureDocument) error {
	actual := SignatureSeed{Cardinalities: make([]SignatureCardinality, 0, len(document.Schema.Tables))}
	for _, table := range document.Schema.Tables {
		columns := make([]string, len(table.Columns))
		for index, column := range table.Columns {
			columns[index] = column.Name
		}
		statement := "SELECT " + strings.Join(columns, ", ") + " FROM " + table.Name + " ORDER BY " + strings.Join(table.PrimaryKey, ", ")
		rows, err := db.QueryContext(ctx, BindSQL(engine, statement))
		if err != nil {
			return fmt.Errorf("seed identity %s: %w", table.Name, err)
		}
		count := 0
		for rows.Next() {
			destinations := make([]any, len(table.Columns))
			values := make([]any, len(table.Columns))
			for index := range destinations {
				destinations[index] = &values[index]
			}
			if err := rows.Scan(destinations...); err != nil {
				_ = rows.Close()
				return fmt.Errorf("seed identity %s scan: %w", table.Name, err)
			}
			for index, column := range table.Columns {
				values[index], err = normalizeDatabaseSeedValue(column.Type, values[index])
				if err != nil {
					_ = rows.Close()
					return fmt.Errorf("seed identity %s.%s: %w", table.Name, column.Name, err)
				}
			}
			actual.Rows = append(actual.Rows, SignatureRow{Table: table.Name, Values: values})
			count++
		}
		if err := rows.Err(); err != nil {
			_ = rows.Close()
			return fmt.Errorf("seed identity %s rows: %w", table.Name, err)
		}
		if err := rows.Close(); err != nil {
			return fmt.Errorf("seed identity %s close: %w", table.Name, err)
		}
		actual.Cardinalities = append(actual.Cardinalities, SignatureCardinality{Name: table.Name, Count: count})
	}
	ordered, err := orderDatabaseSeedRows(actual.Rows, document)
	if err != nil {
		return err
	}
	actual.Rows = ordered
	if digestJSON(actual) != portableSeedSHA256 {
		return fmt.Errorf("seed identity digest differs")
	}
	return nil
}

func orderDatabaseSeedRows(rows []SignatureRow, document SignatureDocument) ([]SignatureRow, error) {
	tables := make(map[string]SignatureTable, len(document.Schema.Tables))
	available := make(map[string]map[string]SignatureRow, len(document.Schema.Tables))
	for _, table := range document.Schema.Tables {
		tables[table.Name] = table
		available[table.Name] = make(map[string]SignatureRow)
	}
	for _, row := range rows {
		key, err := signatureSeedKey(tables[row.Table], row)
		if err != nil {
			return nil, err
		}
		available[row.Table][key] = row
	}
	ordered := make([]SignatureRow, 0, len(rows))
	for _, expected := range document.Seed.Rows {
		key, err := signatureSeedKey(tables[expected.Table], expected)
		if err != nil {
			return nil, err
		}
		row, ok := available[expected.Table][key]
		if !ok {
			return nil, fmt.Errorf("seed identity %s is missing primary key %s", expected.Table, key)
		}
		ordered = append(ordered, row)
		delete(available[expected.Table], key)
	}
	for table, remaining := range available {
		if len(remaining) != 0 {
			return nil, fmt.Errorf("seed identity %s has %d unexpected rows", table, len(remaining))
		}
	}
	return ordered, nil
}

func signatureSeedKey(table SignatureTable, row SignatureRow) (string, error) {
	values := make([]any, len(table.PrimaryKey))
	for index, column := range table.PrimaryKey {
		values[index] = row.Values[signatureColumnIndex(table, column)]
	}
	data, err := json.Marshal(values)
	if err != nil {
		return "", fmt.Errorf("seed identity %s primary key: %w", row.Table, err)
	}
	return string(data), nil
}

func normalizeDatabaseSeedValue(columnType string, value any) (any, error) {
	if value == nil {
		return nil, nil
	}
	switch columnType {
	case "integer":
		switch value := value.(type) {
		case int64:
			return float64(value), nil
		case []byte:
			parsed, err := strconv.ParseInt(string(value), 10, 64)
			return float64(parsed), err
		case string:
			parsed, err := strconv.ParseInt(value, 10, 64)
			return float64(parsed), err
		}
	case "boolean":
		switch value := value.(type) {
		case bool:
			return value, nil
		case int64:
			if value == 0 || value == 1 {
				return value == 1, nil
			}
		case []byte:
			return string(value) == "1" || strings.EqualFold(string(value), "true"), nil
		}
	case "varchar(32)", "varchar(64)", "varchar(128)":
		switch value := value.(type) {
		case string:
			return value, nil
		case []byte:
			return string(value), nil
		}
	case "date":
		switch value := value.(type) {
		case time.Time:
			return value.UTC().Format("2006-01-02"), nil
		case string:
			return value, nil
		case []byte:
			return string(value), nil
		}
	case "timestamp":
		switch value := value.(type) {
		case time.Time:
			return value.UTC().Format(time.RFC3339), nil
		case string:
			return canonicalDatabaseTimestamp(value), nil
		case []byte:
			return canonicalDatabaseTimestamp(string(value)), nil
		}
	}
	return nil, fmt.Errorf("cannot normalize %T as %s", value, columnType)
}

func canonicalDatabaseTimestamp(value string) string {
	value = strings.Replace(value, " ", "T", 1)
	if !strings.HasSuffix(value, "Z") {
		value += "Z"
	}
	return value
}
