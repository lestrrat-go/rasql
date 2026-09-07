// Package compilerlock implements the versioned, deterministic compiler lockfile.
package compilerlock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sort"
	"strings"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

const FormatVersion = 1
const MaxSourceFileSize = 64 << 20

type File struct {
	Format     int              `json:"format"`
	Compiler   string           `json:"compiler"`
	Source     SourceRecord     `json:"source"`
	Engine     EngineRecord     `json:"engine"`
	Catalog    CatalogRecord    `json:"catalog"`
	Queries    []QueryRecord    `json:"queries"`
	Generation GenerationRecord `json:"generation"`
	Digests    Digests          `json:"digests"`
}
type GenerationRecord struct {
	Package string             `json:"package"`
	Output  string             `json:"output"`
	Emitter string             `json:"emitter"`
	Prune   bool               `json:"prune"`
	Objects []ObjectNameRecord `json:"objects"`
	Queries []QueryNameRecord  `json:"queries,omitempty"`
}
type ObjectNameRecord struct{ ID, Source, Row, Create, Patch, File string }

func (r ObjectNameRecord) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID     string `json:"id"`
		Source string `json:"source"`
		Row    string `json:"row"`
		Create string `json:"create"`
		Patch  string `json:"patch"`
		File   string `json:"file"`
	}{r.ID, r.Source, r.Row, r.Create, r.Patch, r.File})
}

type QueryNameRecord struct{ ID, Function, Result, Projection, Decoder, File string }

func (r QueryNameRecord) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		ID         string `json:"id"`
		Function   string `json:"function"`
		Result     string `json:"result"`
		Projection string `json:"projection"`
		Decoder    string `json:"decoder"`
		File       string `json:"file"`
	}{r.ID, r.Function, r.Result, r.Projection, r.Decoder, r.File})
}

type EngineRecord struct {
	Dialect string `json:"dialect"`
	Version string `json:"version"`
	Profile string `json:"profile"`
}
type CatalogRecord struct {
	Objects []ObjectRecord `json:"objects"`
}
type ObjectRecord struct {
	ID                          string                      `json:"id"`
	Kind                        string                      `json:"kind"`
	Schema                      string                      `json:"schema,omitempty"`
	Name                        string                      `json:"name"`
	Columns                     []ColumnRecord              `json:"columns"`
	Constraints                 []ConstraintRecord          `json:"constraints,omitempty"`
	Indexes                     []IndexRecord               `json:"indexes,omitempty"`
	ExclusionConstraints        []ExclusionConstraintRecord `json:"exclusion_constraints,omitempty"`
	Strict                      bool                        `json:"strict,omitempty"`
	WithoutRowID                bool                        `json:"without_row_id,omitempty"`
	PrimaryKeyAutoincrement     bool                        `json:"primary_key_autoincrement,omitempty"`
	PrimaryKeyOnConflict        string                      `json:"primary_key_on_conflict,omitempty"`
	VirtualTableModule          string                      `json:"virtual_table_module,omitempty"`
	VirtualTableModuleArguments []string                    `json:"virtual_table_module_arguments,omitempty"`
}
type ColumnRecord struct {
	Name             string                  `json:"name"`
	Ordinal          int                     `json:"ordinal"`
	LogicalKind      string                  `json:"logical_kind"`
	Native           *NativeTypeRecord       `json:"native,omitempty"`
	Integer          *IntegerTypeFactsRecord `json:"integer,omitempty"`
	Text             *TextTypeFactsRecord    `json:"text,omitempty"`
	Decimal          *DecimalTypeFactsRecord `json:"decimal,omitempty"`
	Nullable         bool                    `json:"nullable"`
	DefaultSQL       string                  `json:"default_sql,omitempty"`
	GeneratedSQL     string                  `json:"generated_sql,omitempty"`
	GeneratedStorage string                  `json:"generated_storage,omitempty"`
	Identity         string                  `json:"identity,omitempty"`
	Collation        string                  `json:"collation,omitempty"`
	Hidden           bool                    `json:"hidden,omitempty"`
}
type NativeTypeRecord struct {
	Dialect   string            `json:"dialect"`
	Schema    string            `json:"schema,omitempty"`
	Name      string            `json:"name"`
	Kind      string            `json:"kind"`
	Arguments *[]string         `json:"arguments,omitempty"`
	Element   *NativeTypeRecord `json:"element,omitempty"`
}
type OptionalIntRecord struct {
	Value int  `json:"value"`
	Set   bool `json:"set"`
}
type IntegerTypeFactsRecord struct {
	Unsigned     bool              `json:"unsigned,omitempty"`
	DisplayWidth OptionalIntRecord `json:"display_width"`
	ZeroFill     bool              `json:"zero_fill,omitempty"`
}
type TextTypeFactsRecord struct {
	Width OptionalIntRecord `json:"width"`
	Fixed bool              `json:"fixed,omitempty"`
}
type DecimalTypeFactsRecord struct {
	Precision int               `json:"precision"`
	Scale     OptionalIntRecord `json:"scale"`
	Unsigned  bool              `json:"unsigned,omitempty"`
	ZeroFill  bool              `json:"zero_fill,omitempty"`
}
type ConstraintRecord struct {
	Name              string            `json:"name"`
	Kind              string            `json:"kind"`
	Columns           []string          `json:"columns,omitempty"`
	Reference         *ReferenceRecord  `json:"reference,omitempty"`
	ExpressionSQL     string            `json:"expression_sql,omitempty"`
	Deferrability     string            `json:"deferrability,omitempty"`
	OnUpdate          string            `json:"on_update,omitempty"`
	OnDelete          string            `json:"on_delete,omitempty"`
	Match             string            `json:"match,omitempty"`
	NullsNotDistinct  bool              `json:"nulls_not_distinct,omitempty"`
	IncludeColumns    []string          `json:"include_columns,omitempty"`
	OnConflict        string            `json:"on_conflict,omitempty"`
	Keys              []IndexPartRecord `json:"keys,omitempty"`
	Temporal          bool              `json:"temporal,omitempty"`
	StorageParameters map[string]string `json:"storage_parameters,omitempty"`
	Tablespace        string            `json:"tablespace,omitempty"`
	ReplicaIdentity   bool              `json:"replica_identity,omitempty"`
	Collations        map[string]string `json:"collations,omitempty"`
	NoInherit         bool              `json:"no_inherit,omitempty"`
	NotValid          bool              `json:"not_valid,omitempty"`
	NotEnforced       bool              `json:"not_enforced,omitempty"`
	DeleteSetColumns  []string          `json:"delete_set_columns,omitempty"`
}
type ReferenceRecord struct {
	Schema  string   `json:"schema,omitempty"`
	Object  string   `json:"object"`
	Columns []string `json:"columns"`
}
type IndexRecord struct {
	Name              string            `json:"name"`
	Unique            bool              `json:"unique,omitempty"`
	Method            string            `json:"method,omitempty"`
	Parts             []IndexPartRecord `json:"parts"`
	PredicateSQL      string            `json:"predicate_sql,omitempty"`
	IncludeColumns    []string          `json:"include_columns,omitempty"`
	Invisible         bool              `json:"invisible,omitempty"`
	NotValid          bool              `json:"not_valid,omitempty"`
	StorageParameters map[string]string `json:"storage_parameters,omitempty"`
	Tablespace        string            `json:"tablespace,omitempty"`
	ReplicaIdentity   bool              `json:"replica_identity,omitempty"`
	NullsNotDistinct  bool              `json:"nulls_not_distinct,omitempty"`
}
type IndexPartRecord struct {
	Column        string `json:"column,omitempty"`
	ExpressionSQL string `json:"expression_sql,omitempty"`
	Direction     string `json:"direction,omitempty"`
	Nulls         string `json:"nulls,omitempty"`
	Collation     string `json:"collation,omitempty"`
	OperatorClass string `json:"operator_class,omitempty"`
	PrefixLength  int    `json:"prefix_length,omitempty"`
}
type ExclusionConstraintRecord struct {
	Name          string                   `json:"name"`
	Method        string                   `json:"method,omitempty"`
	Elements      []ExclusionElementRecord `json:"elements"`
	PredicateSQL  string                   `json:"predicate_sql,omitempty"`
	Deferrability string                   `json:"deferrability,omitempty"`
}
type ExclusionElementRecord struct {
	ExpressionSQL string `json:"expression_sql"`
	Operator      string `json:"operator"`
}
type SourceRecord struct {
	Kind     string       `json:"kind"`
	Identity string       `json:"identity"`
	Files    []SourceFile `json:"files,omitempty"`
}
type SourceFile struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}
type Digests struct {
	Source     string `json:"source"`
	Mappings   string `json:"mappings"`
	Queries    string `json:"queries"`
	Generation string `json:"generation"`
}
type QueryRecord struct {
	ID          compilerir.QueryID `json:"id"`
	Name        string             `json:"name"`
	SQL         SourceFile         `json:"sql"`
	Operation   string             `json:"operation"`
	Parameters  []ValueRecord      `json:"parameters"`
	Results     []ValueRecord      `json:"results,omitempty"`
	Cardinality string             `json:"cardinality"`
	Evidence    EngineEvidence     `json:"evidence"`
}
type ValueRecord struct {
	Name                 string               `json:"name"`
	Scalar               string               `json:"scalar"`
	Nullable             bool                 `json:"nullable"`
	TypeCertainty        compilerir.Certainty `json:"type_certainty"`
	NullabilityCertainty compilerir.Certainty `json:"nullability_certainty"`
}
type EngineEvidence struct {
	Dialect     string        `json:"dialect"`
	Profile     string        `json:"profile"`
	Parameters  []ValueRecord `json:"parameters"`
	Results     []ValueRecord `json:"results,omitempty"`
	Diagnostics []string      `json:"diagnostics,omitempty"`
}
type DigestInputs struct {
	Source     SourceDigestInput
	Mappings   compilerir.MappingConfig
	Queries    []QueryDigestInput
	Generation compilerir.GoConfig
}
type SourceDigestInput struct {
	Record       SourceRecord
	Engine       EngineRecord
	Materializer []KeyValue
}
type QueryDigestInput struct {
	ID                  string
	SQL                 SourceFile
	Operation           string
	Parameters, Results []ValueRecord
	Cardinality         string
}
type KeyValue struct{ Key, Value string }

func Read(name string) (File, error) {
	b, err := os.ReadFile(name)
	if err != nil {
		return File{}, err
	}
	return Decode(b)
}
func Decode(b []byte) (File, error) {
	var f File
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&f); err != nil {
		return f, err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return f, errors.New("compilerlock: trailing JSON")
	}
	if f.Format != FormatVersion {
		return f, fmt.Errorf("compilerlock: unsupported format %d", f.Format)
	}
	if err := validateFile(f); err != nil {
		return f, err
	}
	return normalize(f), nil
}
func Encode(f File) ([]byte, error) {
	if f.Format != FormatVersion {
		return nil, fmt.Errorf("compilerlock: unsupported format %d", f.Format)
	}
	f = normalize(f)
	if err := validateFile(f); err != nil {
		return nil, err
	}
	var b bytes.Buffer
	e := json.NewEncoder(&b)
	e.SetEscapeHTML(false)
	e.SetIndent("", "  ")
	if err := e.Encode(f); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}
func Upgrade(b []byte) ([]byte, error) {
	var h struct {
		Format int `json:"format"`
	}
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if err := d.Decode(&h); err != nil {
		return nil, err
	}
	if h.Format > FormatVersion {
		return nil, fmt.Errorf("compilerlock: newer format %d", h.Format)
	}
	if h.Format < FormatVersion {
		return nil, fmt.Errorf("compilerlock: no upgrade from format %d", h.Format)
	}
	f, err := Decode(b)
	if err != nil {
		return nil, err
	}
	return Encode(f)
}

func validateFile(f File) error {
	if f.Engine.Dialect != "postgresql" && f.Engine.Dialect != "mysql" && f.Engine.Dialect != "sqlite" {
		return fmt.Errorf("compilerlock: unsupported engine dialect %q", f.Engine.Dialect)
	}
	if f.Source.Identity == "" || f.Source.Kind == "" {
		return errors.New("compilerlock: source kind and identity are required")
	}
	if f.Engine.Profile == "" {
		return errors.New("compilerlock: engine profile is required")
	}
	if err := validateSource(f.Source); err != nil {
		return err
	}
	if len(f.Catalog.Objects) > 0 {
		c := ToPhysical(f)
		if err := compilerir.ValidatePhysical(c); err != nil {
			return err
		}
	}
	ids := map[string]struct{}{}
	for _, q := range f.Queries {
		if q.ID == "" {
			return errors.New("compilerlock: empty query ID")
		}
		if _, ok := ids[string(q.ID)]; ok {
			return fmt.Errorf("compilerlock: duplicate query ID %q", q.ID)
		}
		if err := validateSource(SourceRecord{Files: []SourceFile{q.SQL}}); err != nil {
			return err
		}
		ids[string(q.ID)] = struct{}{}
		if err := validateValues(q.Parameters); err != nil {
			return err
		}
		if err := validateValues(q.Results); err != nil {
			return err
		}
	}
	return nil
}
func validateValues(v []ValueRecord) error {
	for _, x := range v {
		if x.TypeCertainty != "known" && x.TypeCertainty != "declared" && x.TypeCertainty != "unknown" {
			return fmt.Errorf("compilerlock: unknown certainty %q", x.TypeCertainty)
		}
		if x.NullabilityCertainty != "known" && x.NullabilityCertainty != "declared" && x.NullabilityCertainty != "unknown" {
			return fmt.Errorf("compilerlock: unknown certainty %q", x.NullabilityCertainty)
		}
	}
	return nil
}
func validateSource(s SourceRecord) error {
	seen := map[string]struct{}{}
	for _, f := range s.Files {
		p := strings.ReplaceAll(f.Path, "\\", "/")
		if p != f.Path || path.IsAbs(p) || path.Clean(p) != p || p == ".." || strings.HasPrefix(p, "../") {
			return fmt.Errorf("compilerlock: invalid source path %q", f.Path)
		}
		if _, ok := seen[p]; ok {
			return fmt.Errorf("compilerlock: duplicate source path %q", p)
		}
		seen[p] = struct{}{}
		if len(f.SHA256) != 64 {
			return fmt.Errorf("compilerlock: invalid sha256 for %q", p)
		}
		for _, c := range f.SHA256 {
			if !((c >= '0' && c <= '9') || (c >= 'a' && c <= 'f')) {
				return fmt.Errorf("compilerlock: invalid sha256 for %q", p)
			}
		}
	}
	return nil
}
func normalize(f File) File {
	sort.Slice(f.Catalog.Objects, func(i, j int) bool {
		a, b := f.Catalog.Objects[i], f.Catalog.Objects[j]
		return less4(a.Kind, a.Schema, a.Name, a.ID, b.Kind, b.Schema, b.Name, b.ID)
	})
	sort.Slice(f.Source.Files, func(i, j int) bool { return f.Source.Files[i].Path < f.Source.Files[j].Path })
	sort.Slice(f.Queries, func(i, j int) bool { return f.Queries[i].ID < f.Queries[j].ID })
	for i := range f.Catalog.Objects {
		sort.Slice(f.Catalog.Objects[i].Columns, func(a, b int) bool {
			return f.Catalog.Objects[i].Columns[a].Ordinal < f.Catalog.Objects[i].Columns[b].Ordinal
		})
		sort.Slice(f.Catalog.Objects[i].Constraints, func(a, b int) bool {
			return f.Catalog.Objects[i].Constraints[a].Name < f.Catalog.Objects[i].Constraints[b].Name
		})
		sort.Slice(f.Catalog.Objects[i].Indexes, func(a, b int) bool {
			return f.Catalog.Objects[i].Indexes[a].Name < f.Catalog.Objects[i].Indexes[b].Name
		})
	}
	return f
}
func less4(a, b, c, d, e, f, g, h string) bool {
	for _, x := range [][2]string{{a, e}, {b, f}, {c, g}, {d, h}} {
		if x[0] != x[1] {
			return x[0] < x[1]
		}
	}
	return false
}

// SourceBytes reads a source file with the lockfile input limit.
func SourceBytes(name string) ([]byte, error) {
	st, err := os.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !st.Mode().IsRegular() {
		return nil, fmt.Errorf("compilerlock: source is not regular: %s", name)
	}
	f, err := os.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	r := io.LimitReader(f, MaxSourceFileSize+1)
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if len(b) > MaxSourceFileSize {
		return nil, fmt.Errorf("compilerlock: source exceeds %d bytes", MaxSourceFileSize)
	}
	return b, nil
}
