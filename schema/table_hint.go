package schema

// TableHint carries a Go-side generation override that no live database can
// supply. An owned generator can keep its hints in a hand-owned map keyed by
// table name and apply each one to a schema.TableDef before passing the
// descriptors to generate.Store.
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
