package generate

import (
	"github.com/lestrrat-go/rasql/internal/schemagen"
	"github.com/lestrrat-go/rasql/schema"
)

func toNameOverrides(names map[schema.ObjectName]legacyObjectNames) schemagen.NameOverrides {
	overrides := schemagen.NameOverrides{Objects: make(map[schema.ObjectName]schemagen.ObjectNameOverrides, len(names))}
	for object, configured := range names {
		columns := make(map[string]schemagen.ColumnNameOverrides, len(configured.Columns))
		for column, value := range configured.Columns {
			columns[column] = schemagen.ColumnNameOverrides{Field: value.Field, Accessor: value.Accessor}
		}
		overrides.Objects[object] = schemagen.ObjectNameOverrides{Accessor: configured.Accessor, TableType: configured.TableType, RowType: configured.RowType, FileBase: configured.FileBase, Columns: columns}
	}
	return overrides
}

type legacyObjectNames struct {
	Accessor  string                       `json:"accessor,omitempty"`
	TableType string                       `json:"table_type,omitempty"`
	RowType   string                       `json:"row_type,omitempty"`
	FileBase  string                       `json:"file_base,omitempty"`
	Columns   map[string]legacyColumnNames `json:"columns,omitempty"`
}

type legacyColumnNames struct {
	Field    string `json:"field,omitempty"`
	Accessor string `json:"accessor,omitempty"`
}
