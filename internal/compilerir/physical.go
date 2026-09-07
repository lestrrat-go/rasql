// Package compilerir contains the validated, immutable-by-convention inputs
// and outputs of the schema compiler.
package compilerir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"maps"
	"slices"
	"sort"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
)

type ObjectID string
type QueryID string
type DiagnosticLevel string
type Certainty string

const (
	DiagnosticError   DiagnosticLevel = "error"
	DiagnosticWarning DiagnosticLevel = "warning"
	CertaintyKnown    Certainty       = "known"
	CertaintyDeclared Certainty       = "declared"
	CertaintyUnknown  Certainty       = "unknown"
)

type PhysicalCatalog struct {
	Engine  EngineIdentity   `json:"engine"`
	Objects []PhysicalObject `json:"objects"`
}
type EngineIdentity struct {
	Dialect string `json:"dialect"`
	Version string `json:"version"`
	Profile string `json:"profile"`
}
type PhysicalObject struct {
	ID                                            ObjectID `json:"id"`
	Kind                                          string   `json:"kind"`
	Schema                                        string   `json:"schema"`
	Name                                          string   `json:"name"`
	Columns                                       []PhysicalColumn
	Constraints                                   []PhysicalConstraint
	Indexes                                       []PhysicalIndex
	ExclusionConstraints                          []PhysicalExclusionConstraint
	Strict, WithoutRowID, PrimaryKeyAutoincrement bool
	PrimaryKeyOnConflict, VirtualTableModule      string
	VirtualTableModuleArguments                   []string
}
type PhysicalColumn struct {
	Name             string      `json:"name"`
	Ordinal          int         `json:"ordinal"`
	LogicalKind      string      `json:"logical_kind"`
	Native           *NativeType `json:"native"`
	Nullable         bool        `json:"nullable"`
	DefaultSQL       string      `json:"default_sql"`
	GeneratedSQL     string      `json:"generated_sql"`
	GeneratedStorage string      `json:"generated_storage"`
	Identity         string      `json:"identity"`
	Collation        string      `json:"collation"`
	Hidden           bool        `json:"hidden"`
	Integer          *IntegerTypeFacts
	Text             *TextTypeFacts
	Decimal          *DecimalTypeFacts
}
type OptionalInt struct {
	Value int
	Set   bool
}
type IntegerTypeFacts struct {
	Unsigned     bool        `json:"unsigned"`
	DisplayWidth OptionalInt `json:"display_width"`
	ZeroFill     bool        `json:"zero_fill"`
}
type TextTypeFacts struct {
	Width OptionalInt `json:"width"`
	Fixed bool        `json:"fixed"`
}
type DecimalTypeFacts struct {
	Precision int         `json:"precision"`
	Scale     OptionalInt `json:"scale"`
	Unsigned  bool        `json:"unsigned"`
	ZeroFill  bool        `json:"zero_fill"`
}
type NativeType struct {
	Dialect   string      `json:"dialect"`
	Schema    string      `json:"schema"`
	Name      string      `json:"name"`
	Kind      string      `json:"kind"`
	Arguments []string    `json:"arguments"`
	Element   *NativeType `json:"element"`
}
type PhysicalConstraint struct {
	Name, Kind                       string
	Columns                          []string
	Reference                        *ForeignReference
	ExpressionSQL                    string
	Deferrable, InitiallyDeferred    bool
	OnUpdate, OnDelete               string
	Deferrability, Match             string
	NullsNotDistinct                 bool
	IncludeColumns                   []string
	OnConflict                       string
	Keys                             []IndexPart
	Temporal                         bool
	StorageParameters                map[string]string
	Tablespace                       string
	ReplicaIdentity                  bool
	Collations                       map[string]string
	NoInherit, NotValid, NotEnforced bool
	DeleteSetColumns                 []string
}
type ForeignReference struct {
	Schema  string   `json:"schema"`
	Object  string   `json:"object"`
	Columns []string `json:"columns"`
}
type PhysicalIndex struct {
	Name                                  string      `json:"name"`
	Unique                                bool        `json:"unique"`
	Method                                string      `json:"method"`
	KeyForm                               string      `json:"key_form"`
	Parts                                 []IndexPart `json:"parts"`
	PredicateSQL                          string      `json:"predicate_sql"`
	IncludeColumns                        []string
	Invisible, NotValid, NullsNotDistinct bool
	StorageParameters                     map[string]string
	Tablespace                            string
	ReplicaIdentity                       bool
}
type IndexPart struct {
	Column        string `json:"column"`
	ExpressionSQL string `json:"expression_sql"`
	Direction     string `json:"direction"`
	Nulls         string `json:"nulls"`
	Collation     string `json:"collation"`
	OperatorClass string `json:"operator_class"`
	PrefixLength  int    `json:"prefix_length"`
}
type PhysicalExclusionConstraint struct {
	Name, Method                string
	Elements                    []ExclusionElement
	PredicateSQL, Deferrability string
}
type ExclusionElement struct{ ExpressionSQL, Operator string }
type Diagnostic struct {
	Level               DiagnosticLevel
	Code, Path, Message string
}

type ObjectRename struct {
	ID ObjectID
	To QualifiedName
}
type PriorObject struct {
	ID   ObjectID
	Kind string
	Name QualifiedName
}
type IdentityInput struct {
	SourceIdentity string
	Prior          []PriorObject
	Renames        []ObjectRename
}

// PhysicalFromTableDefs adapts the public descriptor without retaining any
// pointers into the caller's descriptors.
func PhysicalFromTableDefs(engine EngineIdentity, tables []schema.TableDef) (PhysicalCatalog, []Diagnostic) {
	c := PhysicalCatalog{Engine: engine}
	var diagnostics []Diagnostic
	for _, source := range tables {
		t := source.Clone()
		if err := t.Validate(); err != nil {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "invalid_table", Path: t.QualifiedName(), Message: err.Error()})
			continue
		}
		o := PhysicalObject{Kind: string(t.EffectiveKind()), Schema: t.Schema, Name: t.Name, Strict: t.Strict, WithoutRowID: t.WithoutRowID, PrimaryKeyAutoincrement: t.PrimaryKeyAutoincrement, PrimaryKeyOnConflict: string(t.PrimaryKeyOnConflict), VirtualTableModule: t.VirtualTableModule, VirtualTableModuleArguments: slices.Clone(t.VirtualTableModuleArguments)}
		for i, col := range t.Columns {
			pc := PhysicalColumn{Name: col.Name, Ordinal: i, LogicalKind: string(col.Type.Kind()), Nullable: col.Nullable, DefaultSQL: string(col.Default), GeneratedSQL: string(col.GeneratedExpression), GeneratedStorage: string(col.GeneratedStorage), Identity: string(col.Identity), Collation: col.Collation, Hidden: col.Hidden}
			pc.Native = nativeType(col.NativeType)
			switch typ := col.Type.(type) {
			case schema.IntegerType:
				pc.Integer = &IntegerTypeFacts{Unsigned: typ.Unsigned, ZeroFill: typ.ZeroFill}
				if value, set := typ.DisplayWidth.Value(); set {
					pc.Integer.DisplayWidth = OptionalInt{Value: value, Set: true}
				}
			case schema.TextType:
				pc.Text = &TextTypeFacts{Fixed: typ.Fixed}
				if value, set := typ.Width.Value(); set {
					pc.Text.Width = OptionalInt{Value: value, Set: true}
				}
			case schema.DecimalType:
				pc.Decimal = &DecimalTypeFacts{Precision: typ.Precision, Unsigned: typ.Unsigned, ZeroFill: typ.ZeroFill}
				if value, set := typ.Scale.Value(); set {
					pc.Decimal.Scale = OptionalInt{Value: value, Set: true}
				}
			}
			o.Columns = append(o.Columns, pc)
		}
		if len(t.PrimaryKey) > 0 {
			o.Constraints = append(o.Constraints, PhysicalConstraint{Kind: "primary_key", Columns: append([]string(nil), t.PrimaryKey...)})
		}
		for _, u := range t.UniqueConstraints {
			constraint := PhysicalConstraint{Name: u.Name, Kind: "unique", Columns: slices.Clone(u.Columns), Deferrability: string(u.Deferrable), NullsNotDistinct: u.NullsNotDistinct, IncludeColumns: slices.Clone(u.IncludeColumns), OnConflict: string(u.OnConflict), StorageParameters: maps.Clone(u.StorageParameters), Tablespace: u.Tablespace, ReplicaIdentity: u.ReplicaIdentity, Collations: maps.Clone(u.Collations), Temporal: u.Temporal}
			constraint.Keys = make([]IndexPart, len(u.Keys))
			for i, key := range u.Keys {
				constraint.Keys[i] = IndexPart{Column: string(key.Expression), Direction: indexDirection(key.Descending), Collation: key.Collation, OperatorClass: key.OperatorClass, PrefixLength: key.PrefixLength, Nulls: string(key.NullsOrder)}
			}
			o.Constraints = append(o.Constraints, constraint)
		}
		for _, f := range t.ForeignKeys {
			o.Constraints = append(o.Constraints, PhysicalConstraint{Name: f.Name, Kind: "foreign_key", Columns: append([]string(nil), f.Columns...), Reference: &ForeignReference{Schema: f.ReferencedSchema, Object: f.ReferencedTable, Columns: append([]string(nil), f.ReferencedColumns...)}, OnDelete: string(f.OnDelete), OnUpdate: string(f.OnUpdate), Deferrability: string(f.Deferrable), Deferrable: f.Deferrable != "", InitiallyDeferred: f.Deferrable == schema.DeferrableInitiallyDeferred, Match: string(f.Match), NotValid: f.NotValid, NotEnforced: f.NotEnforced, Temporal: f.Temporal, DeleteSetColumns: append([]string(nil), f.DeleteSetColumns...)})
		}
		for _, check := range t.Checks {
			o.Constraints = append(o.Constraints, PhysicalConstraint{Name: check.Name, Kind: "check", ExpressionSQL: string(check.Expression), NoInherit: check.NoInherit, NotValid: check.NotValid, NotEnforced: check.NotEnforced})
		}
		for _, index := range t.Indexes {
			forms := 0
			if len(index.Columns) > 0 {
				forms++
			}
			if len(index.Expressions) > 0 {
				forms++
			}
			if len(index.Keys) > 0 {
				forms++
			}
			if forms > 1 {
				diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "index_key_form_conflict", Path: t.QualifiedName() + ".indexes." + index.Name, Message: "index columns, expressions, and keys cannot be combined"})
				continue
			}
			keyForm := "columns"
			if len(index.Expressions) > 0 {
				keyForm = "expressions"
			}
			if len(index.Keys) > 0 {
				keyForm = "keys"
			}
			pi := PhysicalIndex{Name: index.Name, Unique: index.Unique, Method: string(index.Method), KeyForm: keyForm, PredicateSQL: string(index.Predicate), IncludeColumns: append([]string(nil), index.IncludeColumns...), Invisible: index.Invisible, NotValid: index.NotValid, StorageParameters: maps.Clone(index.StorageParameters), Tablespace: index.Tablespace, ReplicaIdentity: index.ReplicaIdentity, NullsNotDistinct: index.NullsNotDistinct}
			for _, p := range index.Columns {
				pi.Parts = append(pi.Parts, IndexPart{Column: p})
			}
			for _, p := range index.Expressions {
				pi.Parts = append(pi.Parts, IndexPart{ExpressionSQL: string(p)})
			}
			for _, key := range index.Keys {
				pi.Parts = append(pi.Parts, IndexPart{ExpressionSQL: string(key.Expression), Direction: indexDirection(key.Descending), Collation: key.Collation, OperatorClass: key.OperatorClass, PrefixLength: key.PrefixLength, Nulls: string(key.NullsOrder)})
			}
			o.Indexes = append(o.Indexes, pi)
		}
		for _, exclusion := range t.ExclusionConstraints {
			pe := PhysicalExclusionConstraint{Name: exclusion.Name, Method: string(exclusion.Method), PredicateSQL: string(exclusion.Predicate), Deferrability: string(exclusion.Deferrable)}
			for _, element := range exclusion.Elements {
				pe.Elements = append(pe.Elements, ExclusionElement{ExpressionSQL: string(element.Expression), Operator: element.Operator})
			}
			o.ExclusionConstraints = append(o.ExclusionConstraints, pe)
		}
		c.Objects = append(c.Objects, o)
	}
	sort.Slice(c.Objects, func(i, j int) bool {
		if c.Objects[i].Kind != c.Objects[j].Kind {
			return c.Objects[i].Kind < c.Objects[j].Kind
		}
		if c.Objects[i].Schema != c.Objects[j].Schema {
			return c.Objects[i].Schema < c.Objects[j].Schema
		}
		return c.Objects[i].Name < c.Objects[j].Name
	})
	return c, sortDiagnostics(diagnostics)
}

func nativeType(n *schema.NativeTypeDef) *NativeType {
	if n == nil {
		return nil
	}
	out := NativeType{Dialect: n.Dialect, Schema: n.Schema, Name: n.Name, Kind: string(n.Kind), Arguments: slices.Clone(n.Arguments)}
	if n.Element != nil {
		out.Element = nativeType(n.Element)
	}
	return &out
}
func indexDirection(desc bool) string {
	if desc {
		return "DESC"
	}
	return ""
}

func AssignObjectIDs(c PhysicalCatalog, in IdentityInput) (PhysicalCatalog, []Diagnostic) {
	c = c.Clone()
	var diagnostics []Diagnostic
	if in.SourceIdentity == "" {
		diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "identity_source_empty", Path: "source_identity", Message: "source identity must not be empty"})
	}
	priorByID := make(map[ObjectID]PriorObject, len(in.Prior))
	priorNames := make(map[QualifiedName]struct{}, len(in.Prior))
	for i, p := range in.Prior {
		if p.ID == "" {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "prior_id_empty", Path: fmt.Sprintf("prior[%d].id", i), Message: "prior object ID must not be empty"})
		}
		if _, ok := priorByID[p.ID]; ok {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "prior_id_duplicate", Path: fmt.Sprintf("prior[%d].id", i), Message: "prior object ID is duplicated"})
		}
		priorByID[p.ID] = p
		if _, ok := priorNames[p.Name]; ok {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "prior_name_duplicate", Path: fmt.Sprintf("prior[%d].name", i), Message: "prior object name is duplicated"})
		}
		priorNames[p.Name] = struct{}{}
	}
	renameDest := make(map[QualifiedName]struct{}, len(in.Renames))
	renameIDs := make(map[ObjectID]struct{}, len(in.Renames))
	renamed := make(map[ObjectID]QualifiedName, len(in.Renames))
	for i, r := range in.Renames {
		if _, ok := renameIDs[r.ID]; ok {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "rename_id_duplicate", Path: fmt.Sprintf("renames[%d].id", i), Message: "rename ID is duplicated"})
		}
		renameIDs[r.ID] = struct{}{}
		if _, ok := priorByID[r.ID]; !ok {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "rename_id_missing", Path: fmt.Sprintf("renames[%d].id", i), Message: "rename ID is absent from prior objects"})
		}
		if _, ok := renameDest[r.To]; ok {
			diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "rename_destination_duplicate", Path: fmt.Sprintf("renames[%d].to", i), Message: "rename destination is duplicated"})
		}
		renameDest[r.To] = struct{}{}
		renamed[r.ID] = r.To
	}
	for i := range c.Objects {
		o := &c.Objects[i]
		o.ID = hashID(in.SourceIdentity, o.Kind, o.Schema, o.Name)
	}
	for _, p := range in.Prior {
		name := p.Name
		if renamedName, ok := renamed[p.ID]; ok {
			name = renamedName
		}
		for i := range c.Objects {
			if c.Objects[i].Kind == p.Kind && c.Objects[i].Schema == name.Schema && c.Objects[i].Name == name.Name {
				c.Objects[i].ID = p.ID
			}
		}
	}
	return c, sortDiagnostics(diagnostics)
}

func hashID(source, kind, schemaName, name string) ObjectID {
	sum := sha256.Sum256([]byte(source + "\n" + kind + "\n" + schemaName + "\n" + name))
	return ObjectID(hex.EncodeToString(sum[:]))
}

func (c PhysicalCatalog) Validate() error { return ValidatePhysical(c) }
func invalid(path, format string, args ...any) error {
	return fmt.Errorf("compilerir: %s: %s", path, fmt.Sprintf(format, args...))
}

// TableDefsFromPhysical provides the compatibility boundary for the legacy
// generator. Facts without a schema.TableDef representation are retained in
// diagnostics rather than silently discarded.
func TableDefsFromPhysical(c PhysicalCatalog) ([]schema.TableDef, []Diagnostic) {
	var out []schema.TableDef
	var diagnostics []Diagnostic
	for _, object := range c.Objects {
		t := schema.TableDef{Schema: object.Schema, Name: object.Name, Kind: schema.ObjectKind(object.Kind), Strict: object.Strict, WithoutRowID: object.WithoutRowID, PrimaryKeyAutoincrement: object.PrimaryKeyAutoincrement, PrimaryKeyOnConflict: schema.ConflictResolution(object.PrimaryKeyOnConflict), VirtualTableModule: object.VirtualTableModule, VirtualTableModuleArguments: append([]string(nil), object.VirtualTableModuleArguments...)}
		for _, col := range object.Columns {
			typeValue := schema.ColumnType(schema.OpaqueType{})
			switch col.LogicalKind {
			case string(schema.KindBoolean):
				typeValue = schema.BooleanType{}
			case string(schema.KindInteger):
				typ := schema.IntegerType{}
				if col.Integer != nil {
					typ.Unsigned, typ.ZeroFill = col.Integer.Unsigned, col.Integer.ZeroFill
					if col.Integer.DisplayWidth.Set {
						typ.DisplayWidth = schema.NewIntegerDisplayWidth(col.Integer.DisplayWidth.Value)
					}
				}
				typeValue = typ
			case string(schema.KindFloat):
				typeValue = schema.FloatType{}
			case string(schema.KindText):
				typ := schema.TextType{}
				if col.Text != nil {
					typ.Fixed = col.Text.Fixed
					if col.Text.Width.Set {
						typ.Width = schema.NewTextWidth(col.Text.Width.Value)
					}
				}
				typeValue = typ
			case string(schema.KindBytes):
				typeValue = schema.BytesType{}
			case string(schema.KindTime):
				typeValue = schema.TimeType{}
			case string(schema.KindJSON):
				typeValue = schema.JSONType{}
			case string(schema.KindUUID):
				typeValue = schema.UUIDType{}
			case string(schema.KindDecimal):
				if col.Decimal == nil || !col.Decimal.Scale.Set {
					diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticError, Code: "decimal_facts_missing", Path: object.Name + "." + col.Name, Message: "decimal precision and stated scale are required"})
					typeValue = schema.DecimalType{}
				} else {
					typeValue = schema.DecimalType{Precision: col.Decimal.Precision, Scale: schema.NewDecimalScale(col.Decimal.Scale.Value), Unsigned: col.Decimal.Unsigned, ZeroFill: col.Decimal.ZeroFill}
				}
			default:
				diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticWarning, Code: "opaque_type", Path: object.Name + "." + col.Name, Message: "native type has no legacy schema equivalent"})
			}
			var native *schema.NativeTypeDef
			if col.Native != nil {
				native = nativeDef(col.Native)
			}
			t.Columns = append(t.Columns, schema.ColumnDef{Name: col.Name, Type: typeValue, Nullable: col.Nullable, Default: sqltext.Text(col.DefaultSQL), Collation: col.Collation, GeneratedExpression: sqltext.Text(col.GeneratedSQL), GeneratedStorage: schema.GeneratedStorage(col.GeneratedStorage), Identity: schema.IdentityGeneration(col.Identity), Hidden: col.Hidden, NativeType: native})
		}
		for _, constraint := range object.Constraints {
			switch constraint.Kind {
			case "primary_key":
				t.PrimaryKey = append([]string(nil), constraint.Columns...)
			case "unique":
				u := schema.UniqueDef{Name: constraint.Name, Columns: slices.Clone(constraint.Columns), Deferrable: schema.Deferrability(constraint.Deferrability), NullsNotDistinct: constraint.NullsNotDistinct, IncludeColumns: slices.Clone(constraint.IncludeColumns), OnConflict: schema.ConflictResolution(constraint.OnConflict), StorageParameters: maps.Clone(constraint.StorageParameters), Tablespace: constraint.Tablespace, ReplicaIdentity: constraint.ReplicaIdentity, Collations: maps.Clone(constraint.Collations), Temporal: constraint.Temporal}
				for _, key := range constraint.Keys {
					u.Keys = append(u.Keys, schema.IndexKeyDef{Expression: sqltext.Text(key.Column), Descending: key.Direction == "DESC", Collation: key.Collation, OperatorClass: key.OperatorClass, PrefixLength: key.PrefixLength, NullsOrder: schema.NullsOrder(key.Nulls)})
				}
				t.UniqueConstraints = append(t.UniqueConstraints, u)
			case "check":
				t.Checks = append(t.Checks, schema.CheckDef{Name: constraint.Name, Expression: sqltext.Text(constraint.ExpressionSQL), NoInherit: constraint.NoInherit, NotValid: constraint.NotValid, NotEnforced: constraint.NotEnforced})
			case "foreign_key":
				if constraint.Reference != nil {
					t.ForeignKeys = append(t.ForeignKeys, schema.ForeignKeyDef{Name: constraint.Name, Columns: append([]string(nil), constraint.Columns...), ReferencedSchema: constraint.Reference.Schema, ReferencedTable: constraint.Reference.Object, ReferencedColumns: append([]string(nil), constraint.Reference.Columns...), Deferrable: schema.Deferrability(constraint.Deferrability), Match: schema.MatchType(constraint.Match), OnDelete: schema.ReferenceAction(constraint.OnDelete), OnUpdate: schema.ReferenceAction(constraint.OnUpdate), NotValid: constraint.NotValid, NotEnforced: constraint.NotEnforced, Temporal: constraint.Temporal, DeleteSetColumns: append([]string(nil), constraint.DeleteSetColumns...)})
				}
			}
		}
		for _, index := range object.Indexes {
			idx := schema.IndexDef{Name: index.Name, Unique: index.Unique, Method: schema.IndexMethod(index.Method), Predicate: sqltext.Text(index.PredicateSQL), IncludeColumns: append([]string(nil), index.IncludeColumns...), Invisible: index.Invisible, NotValid: index.NotValid, StorageParameters: maps.Clone(index.StorageParameters), Tablespace: index.Tablespace, ReplicaIdentity: index.ReplicaIdentity, NullsNotDistinct: index.NullsNotDistinct}
			switch index.KeyForm {
			case "keys":
				for _, part := range index.Parts {
					idx.Keys = append(idx.Keys, schema.IndexKeyDef{Expression: sqltext.Text(part.ExpressionSQL), Descending: part.Direction == "DESC", Collation: part.Collation, OperatorClass: part.OperatorClass, PrefixLength: part.PrefixLength, NullsOrder: schema.NullsOrder(part.Nulls)})
				}
			case "expressions":
				for _, part := range index.Parts {
					idx.Expressions = append(idx.Expressions, sqltext.Text(part.ExpressionSQL))
				}
			default:
				for _, part := range index.Parts {
					if part.ExpressionSQL != "" {
						idx.Expressions = append(idx.Expressions, sqltext.Text(part.ExpressionSQL))
					} else {
						idx.Columns = append(idx.Columns, part.Column)
					}
				}
			}
			t.Indexes = append(t.Indexes, idx)
		}
		for _, exclusion := range object.ExclusionConstraints {
			e := schema.ExclusionDef{Name: exclusion.Name, Method: schema.IndexMethod(exclusion.Method), Predicate: sqltext.Text(exclusion.PredicateSQL), Deferrable: schema.Deferrability(exclusion.Deferrability)}
			for _, element := range exclusion.Elements {
				e.Elements = append(e.Elements, schema.ExclusionElementDef{Expression: sqltext.Text(element.ExpressionSQL), Operator: element.Operator})
			}
			t.ExclusionConstraints = append(t.ExclusionConstraints, e)
		}
		out = append(out, t)
	}
	return out, sortDiagnostics(diagnostics)
}

func nativeDef(n *NativeType) *schema.NativeTypeDef {
	if n == nil {
		return nil
	}
	out := &schema.NativeTypeDef{Dialect: n.Dialect, Schema: n.Schema, Name: n.Name, Kind: schema.NativeTypeKind(n.Kind), Arguments: slices.Clone(n.Arguments)}
	out.Element = nativeDef(n.Element)
	return out
}
