package schema

// TableHint carries a Go-side generation override that no live database can
// supply. A bootstrap-generated description package keeps its hints in a
// hand-owned file, conventionally holding a map named Hints keyed by table
// name, that the package's own generated Tables function applies to each
// schema.TableDef through Apply before returning it. `rasqlgen bootstrap`
// writes that file once, the first time it creates a package, and a later
// refresh never rewrites it -- see the "hints" section of
// docs/06-rasqlgen.md.
//
// The hint surface starts deliberately small. RowName is the one override
// no database can ever recover: a table's generated row type name is a
// Go-side fact the server never records, unlike, say, SQLite's Strict or
// WithoutRowID, which inspection already reads from the live schema itself
// and therefore never needs a hand-maintained override.
type TableHint struct {
	// RowName overrides the generated row type exactly like the RowNamed
	// TableOption does; see TableDef.RowName's own doc for what setting it
	// means and what rejects an invalid value. The zero value, an empty
	// string, applies no override, leaving TableDef.RowName exactly as
	// inspection or an earlier-applied hint left it.
	RowName string
}

// Apply returns table with hint's non-zero fields overlaid onto it. An
// empty TableHint returns table unchanged.
func (hint TableHint) Apply(table TableDef) TableDef {
	if hint.RowName != "" {
		table.RowName = hint.RowName
	}
	return table
}
