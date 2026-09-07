// Package compilerir contains the validated, immutable-by-convention inputs
// and outputs of the schema compiler.
package compilerir

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
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
	Engine  EngineIdentity
	Objects []PhysicalObject
}
type EngineIdentity struct{ Dialect, Version, Profile string }
type PhysicalObject struct {
	ID                 ObjectID
	Kind, Schema, Name string
	Columns            []PhysicalColumn
	Constraints        []PhysicalConstraint
	Indexes            []PhysicalIndex
}
type PhysicalColumn struct {
	Name                                                            string
	Ordinal                                                         int
	LogicalKind                                                     string
	Native                                                          NativeType
	Nullable                                                        bool
	DefaultSQL, GeneratedSQL, GeneratedStorage, Identity, Collation string
	Hidden                                                          bool
}
type NativeType struct {
	Dialect, Schema, Name, Kind string
	Arguments                   []string
	Element                     *NativeType
}
type PhysicalConstraint struct {
	Name, Kind                    string
	Columns                       []string
	Reference                     *ForeignReference
	ExpressionSQL                 string
	Deferrable, InitiallyDeferred bool
	OnUpdate, OnDelete            string
}
type ForeignReference struct {
	Schema, Object string
	Columns        []string
}
type PhysicalIndex struct {
	Name         string
	Unique       bool
	Method       string
	Parts        []IndexPart
	PredicateSQL string
}
type IndexPart struct{ Column, ExpressionSQL, Direction, Nulls, Collation string }
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
	for _, source := range tables {
		t := source.Clone()
		o := PhysicalObject{Kind: string(t.EffectiveKind()), Schema: t.Schema, Name: t.Name}
		for i, col := range t.Columns {
			pc := PhysicalColumn{Name: col.Name, Ordinal: i, LogicalKind: string(col.Type.Kind()), Nullable: col.Nullable, DefaultSQL: string(col.Default), GeneratedSQL: string(col.GeneratedExpression), GeneratedStorage: string(col.GeneratedStorage), Identity: string(col.Identity), Collation: col.Collation, Hidden: col.Hidden}
			pc.Native = nativeType(col.NativeType)
			o.Columns = append(o.Columns, pc)
		}
		for _, key := range t.PrimaryKey {
			o.Constraints = append(o.Constraints, PhysicalConstraint{Kind: "primary_key", Columns: []string{key}})
		}
		for _, u := range t.UniqueConstraints {
			o.Constraints = append(o.Constraints, PhysicalConstraint{Name: u.Name, Kind: "unique", Columns: append([]string(nil), u.Columns...)})
		}
		for _, f := range t.ForeignKeys {
			o.Constraints = append(o.Constraints, PhysicalConstraint{Name: f.Name, Kind: "foreign_key", Columns: append([]string(nil), f.Columns...), Reference: &ForeignReference{Schema: f.ReferencedSchema, Object: f.ReferencedTable, Columns: append([]string(nil), f.ReferencedColumns...)}, OnDelete: string(f.OnDelete), OnUpdate: string(f.OnUpdate), Deferrable: f.Deferrable != "", InitiallyDeferred: f.Deferrable == schema.DeferrableInitiallyDeferred})
		}
		for _, check := range t.Checks {
			o.Constraints = append(o.Constraints, PhysicalConstraint{Name: check.Name, Kind: "check", ExpressionSQL: string(check.Expression)})
		}
		for _, index := range t.Indexes {
			pi := PhysicalIndex{Name: index.Name, Unique: index.Unique, Method: string(index.Method), PredicateSQL: string(index.Predicate)}
			for _, p := range index.Columns {
				pi.Parts = append(pi.Parts, IndexPart{Column: p})
			}
			for _, p := range index.Expressions {
				pi.Parts = append(pi.Parts, IndexPart{ExpressionSQL: string(p)})
			}
			o.Indexes = append(o.Indexes, pi)
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
	return c, nil
}

func nativeType(n *schema.NativeTypeDef) NativeType {
	if n == nil {
		return NativeType{}
	}
	out := NativeType{Dialect: n.Dialect, Schema: n.Schema, Name: n.Name, Kind: string(n.Kind), Arguments: append([]string(nil), n.Arguments...)}
	if n.Element != nil {
		e := nativeType(n.Element)
		out.Element = &e
	}
	return out
}

func AssignObjectIDs(c PhysicalCatalog, in IdentityInput) (PhysicalCatalog, []Diagnostic) {
	priorByID := make(map[ObjectID]PriorObject, len(in.Prior))
	for _, p := range in.Prior {
		priorByID[p.ID] = p
	}
	for i := range c.Objects {
		o := &c.Objects[i]
		o.ID = hashID(in.SourceIdentity, o.Kind, o.Schema, o.Name)
	}
	for _, p := range in.Prior {
		for i := range c.Objects {
			if c.Objects[i].Kind == p.Kind && c.Objects[i].Schema == p.Name.Schema && c.Objects[i].Name == p.Name.Name {
				c.Objects[i].ID = p.ID
			}
		}
	}
	for _, r := range in.Renames {
		prior, ok := priorByID[r.ID]
		if !ok {
			continue
		}
		for i := range c.Objects {
			if c.Objects[i].Kind == prior.Kind && c.Objects[i].Schema == r.To.Schema && c.Objects[i].Name == r.To.Name {
				c.Objects[i].ID = r.ID
			}
		}
	}
	return c, nil
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
		t := schema.TableDef{Schema: object.Schema, Name: object.Name, Kind: schema.ObjectKind(object.Kind)}
		for _, col := range object.Columns {
			typeValue := schema.ColumnType(schema.OpaqueType{})
			switch col.LogicalKind {
			case string(schema.KindBoolean):
				typeValue = schema.BooleanType{}
			case string(schema.KindInteger):
				typeValue = schema.IntegerType{}
			case string(schema.KindFloat):
				typeValue = schema.FloatType{}
			case string(schema.KindText):
				typeValue = schema.TextType{}
			case string(schema.KindBytes):
				typeValue = schema.BytesType{}
			case string(schema.KindTime):
				typeValue = schema.TimeType{}
			case string(schema.KindJSON):
				typeValue = schema.JSONType{}
			case string(schema.KindUUID):
				typeValue = schema.UUIDType{}
			case string(schema.KindDecimal):
				typeValue = schema.DecimalType{}
			default:
				diagnostics = append(diagnostics, Diagnostic{Level: DiagnosticWarning, Code: "opaque_type", Path: object.Name + "." + col.Name, Message: "native type has no legacy schema equivalent"})
			}
			var native *schema.NativeTypeDef
			if col.Native.Name != "" {
				native = &schema.NativeTypeDef{Dialect: col.Native.Dialect, Schema: col.Native.Schema, Name: col.Native.Name, Kind: schema.NativeTypeKind(col.Native.Kind), Arguments: append([]string(nil), col.Native.Arguments...)}
			}
			t.Columns = append(t.Columns, schema.ColumnDef{Name: col.Name, Type: typeValue, Nullable: col.Nullable, Collation: col.Collation, GeneratedExpression: sqltext.Text(col.GeneratedSQL), GeneratedStorage: schema.GeneratedStorage(col.GeneratedStorage), Identity: schema.IdentityGeneration(col.Identity), Hidden: col.Hidden, NativeType: native})
		}
		out = append(out, t)
	}
	return out, sortDiagnostics(diagnostics)
}
