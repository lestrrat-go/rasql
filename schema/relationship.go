package schema

import (
	"strings"
	"unicode"
)

// RelationshipsFromForeignKeys derives the default belongs-to relationships
// for table's physical foreign keys without changing the table descriptor.
func RelationshipsFromForeignKeys(table TableDef) []RelationshipDef {
	relationships := make([]RelationshipDef, 0, len(table.ForeignKeys))
	for _, key := range table.ForeignKeys {
		if len(key.Columns) == 0 {
			continue
		}
		name := strings.TrimSuffix(key.Columns[0], "_id")
		name = relationshipGoName(name)
		if name == "" {
			name = relationshipGoName(key.ReferencedTable)
		}
		relationships = append(relationships, RelationshipDef{
			Name: name, Kind: RelationshipBelongsTo,
			Columns: append([]string(nil), key.Columns...), ReferencedSchema: key.ReferencedSchema,
			ReferencedTable: key.ReferencedTable, ReferencedColumns: append([]string(nil), key.ReferencedColumns...),
		})
	}
	return relationships
}

func relationshipGoName(name string) string {
	parts := strings.FieldsFunc(name, func(r rune) bool { return r == '_' })
	var result strings.Builder
	for _, part := range parts {
		switch strings.ToLower(part) {
		case "api":
			result.WriteString("API")
		case "id":
			result.WriteString("ID")
		case "json":
			result.WriteString("JSON")
		case "url":
			result.WriteString("URL")
		case "uuid":
			result.WriteString("UUID")
		default:
			for index, r := range part {
				if index == 0 {
					result.WriteRune(unicode.ToUpper(r))
					continue
				}
				result.WriteRune(r)
			}
		}
	}
	return result.String()
}
