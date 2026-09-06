package schema

// GoImport names a package used by a GoBinding type expression.
type GoImport struct {
	Path string `json:"Path"`
	Name string `json:",omitempty"`
}

// GoBinding overrides the Go type generated for a column. Type is the type
// used for non-null values; NullableType, when present, is used for nullable
// values. Imports list the packages referenced by either expression.
type GoBinding struct {
	Type         string     `json:"Type"`
	NullableType string     `json:",omitempty"`
	Imports      []GoImport `json:",omitempty"`
}

// Clone returns an independent copy of b.
func (b *GoBinding) Clone() *GoBinding {
	if b == nil {
		return nil
	}
	clone := *b
	clone.Imports = append([]GoImport(nil), b.Imports...)
	return &clone
}
