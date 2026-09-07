package compilerir

import (
	"maps"
	"slices"
)

func (c PhysicalCatalog) Clone() PhysicalCatalog {
	out := c
	out.Objects = slices.Clone(c.Objects)
	for i := range out.Objects {
		o := &out.Objects[i]
		o.Columns = slices.Clone(o.Columns)
		for j := range o.Columns {
			o.Columns[j].Native = cloneNative(o.Columns[j].Native)
			if o.Columns[j].Integer != nil {
				facts := *o.Columns[j].Integer
				o.Columns[j].Integer = &facts
			}
			if o.Columns[j].Text != nil {
				facts := *o.Columns[j].Text
				o.Columns[j].Text = &facts
			}
			if o.Columns[j].Decimal != nil {
				facts := *o.Columns[j].Decimal
				o.Columns[j].Decimal = &facts
			}
		}
		o.VirtualTableModuleArguments = slices.Clone(o.VirtualTableModuleArguments)
		o.ExclusionConstraints = slices.Clone(o.ExclusionConstraints)
		for j := range o.ExclusionConstraints {
			o.ExclusionConstraints[j].Elements = append([]ExclusionElement(nil), o.ExclusionConstraints[j].Elements...)
		}
		o.Constraints = slices.Clone(o.Constraints)
		for j := range o.Constraints {
			o.Constraints[j].Columns = slices.Clone(o.Constraints[j].Columns)
			o.Constraints[j].IncludeColumns = slices.Clone(o.Constraints[j].IncludeColumns)
			o.Constraints[j].Keys = slices.Clone(o.Constraints[j].Keys)
			o.Constraints[j].DeleteSetColumns = slices.Clone(o.Constraints[j].DeleteSetColumns)
			o.Constraints[j].StorageParameters = maps.Clone(o.Constraints[j].StorageParameters)
			o.Constraints[j].Collations = maps.Clone(o.Constraints[j].Collations)
			if o.Constraints[j].Reference != nil {
				r := *o.Constraints[j].Reference
				r.Columns = slices.Clone(r.Columns)
				o.Constraints[j].Reference = &r
			}
		}
		o.Indexes = slices.Clone(o.Indexes)
		for j := range o.Indexes {
			o.Indexes[j].Parts = slices.Clone(o.Indexes[j].Parts)
			o.Indexes[j].IncludeColumns = slices.Clone(o.Indexes[j].IncludeColumns)
			o.Indexes[j].StorageParameters = maps.Clone(o.Indexes[j].StorageParameters)
		}
	}
	return out
}
func cloneNative(n *NativeType) *NativeType {
	if n == nil {
		return nil
	}
	out := *n
	out.Arguments = slices.Clone(n.Arguments)
	out.Element = cloneNative(n.Element)
	return &out
}
func (in IdentityInput) Clone() IdentityInput {
	out := in
	out.Prior = slices.Clone(in.Prior)
	out.Renames = slices.Clone(in.Renames)
	return out
}
func (m MappingConfig) Clone() MappingConfig {
	out := m
	out.Scalars = slices.Clone(m.Scalars)
	for i := range out.Scalars {
		out.Scalars[i].Imports = append([]GoImport(nil), m.Scalars[i].Imports...)
	}
	return out
}
func (m SemanticModel) Clone() SemanticModel {
	out := m
	out.Objects = slices.Clone(m.Objects)
	out.Queries = slices.Clone(m.Queries)
	out.Diagnostics = slices.Clone(m.Diagnostics)
	for i := range out.Objects {
		out.Objects[i].Columns = slices.Clone(m.Objects[i].Columns)
		out.Objects[i].Relations = slices.Clone(m.Objects[i].Relations)
		for j := range out.Objects[i].Relations {
			out.Objects[i].Relations[j].From = append([]string(nil), m.Objects[i].Relations[j].From...)
			out.Objects[i].Relations[j].To = append([]string(nil), m.Objects[i].Relations[j].To...)
		}
	}
	for i := range out.Queries {
		out.Queries[i].Parameters = slices.Clone(m.Queries[i].Parameters)
		out.Queries[i].Results = slices.Clone(m.Queries[i].Results)
	}
	return out
}

func (m GoModel) Clone() GoModel {
	out := m
	out.Imports = slices.Clone(m.Imports)
	out.Files = slices.Clone(m.Files)
	for i := range out.Files {
		out.Files[i].Declarations = slices.Clone(m.Files[i].Declarations)
	}
	out.Objects = slices.Clone(m.Objects)
	for i := range out.Objects {
		out.Objects[i].Row.Fields = slices.Clone(m.Objects[i].Row.Fields)
		out.Objects[i].Columns = slices.Clone(m.Objects[i].Columns)
		out.Objects[i].Relations = slices.Clone(m.Objects[i].Relations)
		if m.Objects[i].Create != nil {
			shape := *m.Objects[i].Create
			shape.Fields = slices.Clone(shape.Fields)
			out.Objects[i].Create = &shape
		}
		if m.Objects[i].Patch != nil {
			shape := *m.Objects[i].Patch
			shape.Fields = slices.Clone(shape.Fields)
			out.Objects[i].Patch = &shape
		}
	}
	out.Queries = slices.Clone(m.Queries)
	for i := range out.Queries {
		out.Queries[i].Parameters = slices.Clone(m.Queries[i].Parameters)
		if m.Queries[i].Result != nil {
			shape := *m.Queries[i].Result
			shape.Fields = slices.Clone(shape.Fields)
			out.Queries[i].Result = &shape
		}
	}
	return out
}

func (c GoConfig) Clone() GoConfig {
	out := c
	out.Objects = slices.Clone(c.Objects)
	out.Queries = slices.Clone(c.Queries)
	out.Scalars = slices.Clone(c.Scalars)
	for i := range out.Scalars {
		out.Scalars[i].Imports = append([]GoImport(nil), c.Scalars[i].Imports...)
	}
	return out
}
func (q QueryAnalysis) Clone() QueryAnalysis {
	out := q
	out.Parameters = slices.Clone(q.Parameters)
	out.Results = slices.Clone(q.Results)
	out.Diagnostics = slices.Clone(q.Diagnostics)
	for i := range out.Parameters {
		out.Parameters[i].Native = cloneNative(q.Parameters[i].Native)
		if q.Parameters[i].Integer != nil {
			x := *q.Parameters[i].Integer
			out.Parameters[i].Integer = &x
		}
	}
	for i := range out.Results {
		out.Results[i].Native = cloneNative(q.Results[i].Native)
		if q.Results[i].Integer != nil {
			x := *q.Results[i].Integer
			out.Results[i].Integer = &x
		}
	}
	return out
}
