package compilerlock

import "github.com/lestrrat-go/rasql/internal/compilerir"

func CatalogFromPhysical(c compilerir.PhysicalCatalog) CatalogRecord { return FromPhysical(c) }
func PhysicalFromCatalog(f File) compilerir.PhysicalCatalog          { return ToPhysical(f) }

func QueryFromAnalysis(q compilerir.QueryAnalysis) QueryRecord {
	r := QueryRecord{ID: q.ID, Name: q.Name, SQL: SourceFile{Path: q.SQLPath, SHA256: q.SQLSHA256}, Operation: q.Operation, Cardinality: q.Cardinality, Evidence: EngineEvidence{Dialect: q.Engine.Dialect, Profile: q.Engine.Profile}}
	for _, v := range q.Parameters {
		r.Parameters = append(r.Parameters, value(v))
		r.Evidence.Parameters = append(r.Evidence.Parameters, value(v))
	}
	for _, v := range q.Results {
		r.Results = append(r.Results, value(v))
		r.Evidence.Results = append(r.Evidence.Results, value(v))
	}
	for _, d := range q.Diagnostics {
		r.Evidence.Diagnostics = append(r.Evidence.Diagnostics, d.Code)
	}
	return r
}

func AnalysisFromQuery(q QueryRecord) compilerir.QueryAnalysis {
	r := compilerir.QueryAnalysis{ID: q.ID, Name: q.Name, SQLPath: q.SQL.Path, SQLSHA256: q.SQL.SHA256, Operation: q.Operation, Cardinality: q.Cardinality, Engine: compilerir.EngineIdentity{Dialect: q.Evidence.Dialect, Profile: q.Evidence.Profile}}
	for _, v := range q.Parameters {
		r.Parameters = append(r.Parameters, toValue(v))
	}
	for _, v := range q.Results {
		r.Results = append(r.Results, toValue(v))
	}
	for _, code := range q.Evidence.Diagnostics {
		r.Diagnostics = append(r.Diagnostics, compilerir.Diagnostic{Level: compilerir.DiagnosticWarning, Code: code})
	}
	return r
}

func value(v compilerir.SemanticValue) ValueRecord {
	return ValueRecord{Name: v.Name, Scalar: v.Scalar, Nullable: v.Nullable, TypeCertainty: v.TypeCertainty, NullabilityCertainty: v.NullabilityCertainty}
}
func toValue(v ValueRecord) compilerir.SemanticValue {
	return compilerir.SemanticValue{Name: v.Name, Scalar: v.Scalar, Nullable: v.Nullable, TypeCertainty: v.TypeCertainty, NullabilityCertainty: v.NullabilityCertainty}
}

func FromPhysical(c compilerir.PhysicalCatalog) CatalogRecord {
	r := CatalogRecord{}
	r.Objects = make([]ObjectRecord, len(c.Objects))
	for i, o := range c.Objects {
		x := ObjectRecord{ID: string(o.ID), Kind: o.Kind, Schema: o.Schema, Name: o.Name, Strict: o.Strict, WithoutRowID: o.WithoutRowID, PrimaryKeyAutoincrement: o.PrimaryKeyAutoincrement, PrimaryKeyOnConflict: o.PrimaryKeyOnConflict, VirtualTableModule: o.VirtualTableModule, VirtualTableModuleArguments: append([]string(nil), o.VirtualTableModuleArguments...)}
		for _, c := range o.Columns {
			x.Columns = append(x.Columns, column(c))
		}
		for _, c := range o.Constraints {
			x.Constraints = append(x.Constraints, constraint(c))
		}
		for _, i := range o.Indexes {
			x.Indexes = append(x.Indexes, index(i))
		}
		for _, e := range o.ExclusionConstraints {
			y := ExclusionConstraintRecord{Name: e.Name, Method: e.Method, PredicateSQL: e.PredicateSQL, Deferrability: e.Deferrability}
			for _, z := range e.Elements {
				y.Elements = append(y.Elements, ExclusionElementRecord{ExpressionSQL: z.ExpressionSQL, Operator: z.Operator})
			}
			x.ExclusionConstraints = append(x.ExclusionConstraints, y)
		}
		r.Objects[i] = x
	}
	return r
}
func column(c compilerir.PhysicalColumn) ColumnRecord {
	r := ColumnRecord{Name: c.Name, Ordinal: c.Ordinal, LogicalKind: c.LogicalKind, Nullable: c.Nullable, DefaultSQL: c.DefaultSQL, GeneratedSQL: c.GeneratedSQL, GeneratedStorage: c.GeneratedStorage, Identity: c.Identity, Collation: c.Collation, Hidden: c.Hidden}
	if c.Native != nil {
		r.Native = native(c.Native)
	}
	if c.Integer != nil {
		r.Integer = &IntegerTypeFactsRecord{Unsigned: c.Integer.Unsigned, DisplayWidth: oi(c.Integer.DisplayWidth), ZeroFill: c.Integer.ZeroFill}
	}
	if c.Text != nil {
		r.Text = &TextTypeFactsRecord{Width: oi(c.Text.Width), Fixed: c.Text.Fixed}
	}
	if c.Decimal != nil {
		r.Decimal = &DecimalTypeFactsRecord{Precision: c.Decimal.Precision, Scale: oi(c.Decimal.Scale), Unsigned: c.Decimal.Unsigned, ZeroFill: c.Decimal.ZeroFill}
	}
	return r
}
func oi(v compilerir.OptionalInt) OptionalIntRecord {
	return OptionalIntRecord{Value: v.Value, Set: v.Set}
}
func native(n *compilerir.NativeType) *NativeTypeRecord {
	r := &NativeTypeRecord{Dialect: n.Dialect, Schema: n.Schema, Name: n.Name, Kind: n.Kind}
	if n.Arguments != nil {
		x := append(make([]string, 0, len(n.Arguments)), n.Arguments...)
		r.Arguments = &x
	}
	if n.Element != nil {
		r.Element = native(n.Element)
	}
	return r
}
func constraint(c compilerir.PhysicalConstraint) ConstraintRecord {
	r := ConstraintRecord{Name: c.Name, Kind: c.Kind, Columns: append([]string(nil), c.Columns...), ExpressionSQL: c.ExpressionSQL, Deferrability: c.Deferrability, OnUpdate: c.OnUpdate, OnDelete: c.OnDelete, Match: c.Match, NullsNotDistinct: c.NullsNotDistinct, IncludeColumns: append([]string(nil), c.IncludeColumns...), OnConflict: c.OnConflict, Temporal: c.Temporal, StorageParameters: cloneMap(c.StorageParameters), Tablespace: c.Tablespace, ReplicaIdentity: c.ReplicaIdentity, Collations: cloneMap(c.Collations), NoInherit: c.NoInherit, NotValid: c.NotValid, NotEnforced: c.NotEnforced, DeleteSetColumns: append([]string(nil), c.DeleteSetColumns...)}
	if c.Reference != nil {
		r.Reference = &ReferenceRecord{Schema: c.Reference.Schema, Object: c.Reference.Object, Columns: append([]string(nil), c.Reference.Columns...)}
	}
	for _, p := range c.Keys {
		r.Keys = append(r.Keys, part(p))
	}
	return r
}
func index(i compilerir.PhysicalIndex) IndexRecord {
	r := IndexRecord{Name: i.Name, Unique: i.Unique, Method: i.Method, KeyForm: i.KeyForm, PredicateSQL: i.PredicateSQL, IncludeColumns: append([]string(nil), i.IncludeColumns...), Invisible: i.Invisible, NotValid: i.NotValid, StorageParameters: cloneMap(i.StorageParameters), Tablespace: i.Tablespace, ReplicaIdentity: i.ReplicaIdentity, NullsNotDistinct: i.NullsNotDistinct}
	for _, p := range i.Parts {
		r.Parts = append(r.Parts, part(p))
	}
	return r
}
func part(p compilerir.IndexPart) IndexPartRecord {
	return IndexPartRecord{Column: p.Column, ExpressionSQL: p.ExpressionSQL, Direction: p.Direction, Nulls: p.Nulls, Collation: p.Collation, OperatorClass: p.OperatorClass, PrefixLength: p.PrefixLength}
}
func cloneMap(m map[string]string) map[string]string {
	if m == nil {
		return nil
	}
	r := map[string]string{}
	for k, v := range m {
		r[k] = v
	}
	return r
}

func ToPhysical(f File) compilerir.PhysicalCatalog {
	c := compilerir.PhysicalCatalog{Engine: compilerir.EngineIdentity{Dialect: f.Engine.Dialect, Version: f.Engine.Version, Profile: f.Engine.Profile}}
	for _, o := range f.Catalog.Objects {
		x := compilerir.PhysicalObject{ID: compilerir.ObjectID(o.ID), Kind: o.Kind, Schema: o.Schema, Name: o.Name, Strict: o.Strict, WithoutRowID: o.WithoutRowID, PrimaryKeyAutoincrement: o.PrimaryKeyAutoincrement, PrimaryKeyOnConflict: o.PrimaryKeyOnConflict, VirtualTableModule: o.VirtualTableModule, VirtualTableModuleArguments: append([]string(nil), o.VirtualTableModuleArguments...)}
		for _, v := range o.Columns {
			x.Columns = append(x.Columns, toColumn(v))
		}
		for _, v := range o.Constraints {
			x.Constraints = append(x.Constraints, toConstraint(v))
		}
		for _, v := range o.Indexes {
			x.Indexes = append(x.Indexes, toIndex(v))
		}
		for _, v := range o.ExclusionConstraints {
			e := compilerir.PhysicalExclusionConstraint{Name: v.Name, Method: v.Method, PredicateSQL: v.PredicateSQL, Deferrability: v.Deferrability}
			for _, z := range v.Elements {
				e.Elements = append(e.Elements, compilerir.ExclusionElement{ExpressionSQL: z.ExpressionSQL, Operator: z.Operator})
			}
			x.ExclusionConstraints = append(x.ExclusionConstraints, e)
		}
		c.Objects = append(c.Objects, x)
	}
	return c
}
func toColumn(c ColumnRecord) compilerir.PhysicalColumn {
	r := compilerir.PhysicalColumn{Name: c.Name, Ordinal: c.Ordinal, LogicalKind: c.LogicalKind, Nullable: c.Nullable, DefaultSQL: c.DefaultSQL, GeneratedSQL: c.GeneratedSQL, GeneratedStorage: c.GeneratedStorage, Identity: c.Identity, Collation: c.Collation, Hidden: c.Hidden}
	if c.Native != nil {
		r.Native = toNative(c.Native)
	}
	if c.Integer != nil {
		r.Integer = &compilerir.IntegerTypeFacts{Unsigned: c.Integer.Unsigned, DisplayWidth: toOI(c.Integer.DisplayWidth), ZeroFill: c.Integer.ZeroFill}
	}
	if c.Text != nil {
		r.Text = &compilerir.TextTypeFacts{Width: toOI(c.Text.Width), Fixed: c.Text.Fixed}
	}
	if c.Decimal != nil {
		r.Decimal = &compilerir.DecimalTypeFacts{Precision: c.Decimal.Precision, Scale: toOI(c.Decimal.Scale), Unsigned: c.Decimal.Unsigned, ZeroFill: c.Decimal.ZeroFill}
	}
	return r
}
func toOI(v OptionalIntRecord) compilerir.OptionalInt {
	return compilerir.OptionalInt{Value: v.Value, Set: v.Set}
}
func toNative(n *NativeTypeRecord) *compilerir.NativeType {
	r := &compilerir.NativeType{Dialect: n.Dialect, Schema: n.Schema, Name: n.Name, Kind: n.Kind}
	if n.Arguments != nil {
		r.Arguments = append(make([]string, 0, len(*n.Arguments)), (*n.Arguments)...)
	}
	if n.Element != nil {
		r.Element = toNative(n.Element)
	}
	return r
}
func toConstraint(c ConstraintRecord) compilerir.PhysicalConstraint {
	r := compilerir.PhysicalConstraint{Name: c.Name, Kind: c.Kind, Columns: append([]string(nil), c.Columns...), ExpressionSQL: c.ExpressionSQL, Deferrability: c.Deferrability, OnUpdate: c.OnUpdate, OnDelete: c.OnDelete, Match: c.Match, NullsNotDistinct: c.NullsNotDistinct, IncludeColumns: append([]string(nil), c.IncludeColumns...), OnConflict: c.OnConflict, Keys: make([]compilerir.IndexPart, 0, len(c.Keys)), Temporal: c.Temporal, StorageParameters: cloneMap(c.StorageParameters), Tablespace: c.Tablespace, ReplicaIdentity: c.ReplicaIdentity, Collations: cloneMap(c.Collations), NoInherit: c.NoInherit, NotValid: c.NotValid, NotEnforced: c.NotEnforced, DeleteSetColumns: append([]string(nil), c.DeleteSetColumns...)}
	if c.Reference != nil {
		r.Reference = &compilerir.ForeignReference{Schema: c.Reference.Schema, Object: c.Reference.Object, Columns: append([]string(nil), c.Reference.Columns...)}
	}
	for _, p := range c.Keys {
		r.Keys = append(r.Keys, compilerir.IndexPart{Column: p.Column, ExpressionSQL: p.ExpressionSQL, Direction: p.Direction, Nulls: p.Nulls, Collation: p.Collation, OperatorClass: p.OperatorClass, PrefixLength: p.PrefixLength})
	}
	return r
}
func toIndex(i IndexRecord) compilerir.PhysicalIndex {
	r := compilerir.PhysicalIndex{Name: i.Name, Unique: i.Unique, Method: i.Method, KeyForm: i.KeyForm, PredicateSQL: i.PredicateSQL, IncludeColumns: append([]string(nil), i.IncludeColumns...), Invisible: i.Invisible, NotValid: i.NotValid, StorageParameters: cloneMap(i.StorageParameters), Tablespace: i.Tablespace, ReplicaIdentity: i.ReplicaIdentity, NullsNotDistinct: i.NullsNotDistinct}
	for _, p := range i.Parts {
		r.Parts = append(r.Parts, compilerir.IndexPart{Column: p.Column, ExpressionSQL: p.ExpressionSQL, Direction: p.Direction, Nulls: p.Nulls, Collation: p.Collation, OperatorClass: p.OperatorClass, PrefixLength: p.PrefixLength})
	}
	return r
}
