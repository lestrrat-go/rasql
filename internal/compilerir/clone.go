package compilerir

func (c PhysicalCatalog) Clone() PhysicalCatalog {
	out := c
	out.Objects = append([]PhysicalObject(nil), c.Objects...)
	for i := range out.Objects {
		o := &out.Objects[i]
		o.Columns = append([]PhysicalColumn(nil), o.Columns...)
		for j := range o.Columns {
			o.Columns[j].Native = cloneNative(o.Columns[j].Native)
		}
		o.Constraints = append([]PhysicalConstraint(nil), o.Constraints...)
		for j := range o.Constraints {
			o.Constraints[j].Columns = append([]string(nil), o.Constraints[j].Columns...)
			if o.Constraints[j].Reference != nil {
				r := *o.Constraints[j].Reference
				r.Columns = append([]string(nil), r.Columns...)
				o.Constraints[j].Reference = &r
			}
		}
		o.Indexes = append([]PhysicalIndex(nil), o.Indexes...)
		for j := range o.Indexes {
			o.Indexes[j].Parts = append([]IndexPart(nil), o.Indexes[j].Parts...)
		}
	}
	return out
}
func cloneNative(n NativeType) NativeType {
	n.Arguments = append([]string(nil), n.Arguments...)
	if n.Element != nil {
		e := cloneNative(*n.Element)
		n.Element = &e
	}
	return n
}
func (in IdentityInput) Clone() IdentityInput {
	out := in
	out.Prior = append([]PriorObject(nil), in.Prior...)
	out.Renames = append([]ObjectRename(nil), in.Renames...)
	return out
}
func (m MappingConfig) Clone() MappingConfig {
	out := m
	out.Scalars = append([]ScalarMapping(nil), m.Scalars...)
	for i := range out.Scalars {
		out.Scalars[i].Imports = append([]GoImport(nil), m.Scalars[i].Imports...)
	}
	return out
}
func (m SemanticModel) Clone() SemanticModel {
	out := m
	out.Objects = append([]SemanticObject(nil), m.Objects...)
	out.Queries = append([]SemanticQuery(nil), m.Queries...)
	out.Diagnostics = append([]Diagnostic(nil), m.Diagnostics...)
	for i := range out.Objects {
		out.Objects[i].Columns = append([]SemanticColumn(nil), m.Objects[i].Columns...)
		out.Objects[i].Relations = append([]SemanticRelation(nil), m.Objects[i].Relations...)
		for j := range out.Objects[i].Relations {
			out.Objects[i].Relations[j].From = append([]string(nil), m.Objects[i].Relations[j].From...)
			out.Objects[i].Relations[j].To = append([]string(nil), m.Objects[i].Relations[j].To...)
		}
	}
	return out
}
