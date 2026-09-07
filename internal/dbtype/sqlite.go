package dbtype

import (
	"fmt"
	"github.com/lestrrat-go/rasql/schema"
	"strings"
)

// SQLite maps SQLite declared type names using the same affinity rules as inspection.
func SQLite(databaseType string) (schema.ColumnType, error) {
	t := strings.ToUpper(strings.TrimSpace(databaseType))
	switch {
	case strings.Contains(t, "DECIMAL") || strings.Contains(t, "NUMERIC"):
		return nil, fmt.Errorf("exact decimal type %q is not exact in SQLite: a NUMERIC-affinity column stores REAL, so declare the column TEXT", databaseType)
	case strings.Contains(t, "BOOL"):
		return schema.BooleanType{}, nil
	case strings.Contains(t, "INT"):
		return schema.IntegerType{}, nil
	case strings.Contains(t, "CHAR") || strings.Contains(t, "CLOB") || strings.Contains(t, "TEXT"):
		return schema.TextType{}, nil
	case strings.Contains(t, "BLOB") || t == "":
		return schema.BytesType{}, nil
	case strings.Contains(t, "REAL") || strings.Contains(t, "FLOA") || strings.Contains(t, "DOUB"):
		return schema.FloatType{}, nil
	case strings.Contains(t, "JSON"):
		return schema.JSONType{}, nil
	case strings.Contains(t, "DATE") || strings.Contains(t, "TIME"):
		return schema.TimeType{}, nil
	case strings.Contains(t, "UUID"):
		return schema.UUIDType{}, nil
	default:
		return schema.OpaqueType{}, nil
	}
}
