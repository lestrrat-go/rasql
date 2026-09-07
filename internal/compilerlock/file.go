// Package compilerlock implements the versioned, deterministic compiler lockfile.
package compilerlock

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/token"
	"io"
	"os"
	"sort"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

const FormatVersion = 1
const MaxSourceFileBytes int64 = 64 << 20

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
type ObjectNameRecord struct {
	ID     string `json:"id"`
	Source string `json:"source"`
	Row    string `json:"row"`
	Create string `json:"create"`
	Patch  string `json:"patch"`
	File   string `json:"file"`
}
type QueryNameRecord struct {
	ID         string `json:"id"`
	Function   string `json:"function"`
	Result     string `json:"result"`
	Projection string `json:"projection"`
	Decoder    string `json:"decoder"`
	File       string `json:"file"`
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
	KeyForm           string            `json:"key_form"`
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
	for name, value := range map[string]string{"source": f.Digests.Source, "mappings": f.Digests.Mappings, "queries": f.Digests.Queries, "generation": f.Digests.Generation} {
		if err := ValidateDigest(value); err != nil {
			return fmt.Errorf("compilerlock: invalid %s digest: %w", name, err)
		}
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
		if q.Name == "" {
			return fmt.Errorf("compilerlock: query %s has empty name", q.ID)
		}
		if !validOperation(q.Operation) {
			return fmt.Errorf("compilerlock: query %s has invalid operation", q.ID)
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
		if q.Cardinality != "one" && q.Cardinality != "maybe" && q.Cardinality != "many" && q.Cardinality != "exec" {
			return fmt.Errorf("compilerlock: invalid cardinality %q", q.Cardinality)
		}
		if q.Cardinality == "exec" && len(q.Results) != 0 {
			return fmt.Errorf("compilerlock: exec query has results")
		}
		if q.Evidence.Dialect != f.Engine.Dialect || q.Evidence.Profile != f.Engine.Profile {
			return fmt.Errorf("compilerlock: query %s evidence engine mismatch", q.ID)
		}
		if err := validateEvidence(q); err != nil {
			return err
		}
	}
	return validateGeneration(f.Generation, f.Catalog, f.Queries)
}
func validOperation(op string) bool {
	return op == "select" || op == "insert" || op == "update" || op == "delete" || op == "exec"
}
func validateValues(v []ValueRecord) error {
	seen := map[string]struct{}{}
	for _, x := range v {
		if x.Name == "" || x.Scalar == "" {
			return errors.New("compilerlock: query value requires name and scalar")
		}
		if _, ok := seen[x.Name]; ok {
			return fmt.Errorf("compilerlock: duplicate query value %q", x.Name)
		}
		seen[x.Name] = struct{}{}
		if x.TypeCertainty != "known" && x.TypeCertainty != "declared" && x.TypeCertainty != "unknown" {
			return fmt.Errorf("compilerlock: unknown certainty %q", x.TypeCertainty)
		}
		if x.NullabilityCertainty != "known" && x.NullabilityCertainty != "declared" && x.NullabilityCertainty != "unknown" {
			return fmt.Errorf("compilerlock: unknown certainty %q", x.NullabilityCertainty)
		}
	}
	return nil
}
func validateEvidence(q QueryRecord) error {
	if len(q.Parameters) != len(q.Evidence.Parameters) || len(q.Results) != len(q.Evidence.Results) {
		return fmt.Errorf("compilerlock: query %s evidence differs", q.ID)
	}
	for i, v := range q.Parameters {
		if v != q.Evidence.Parameters[i] {
			return fmt.Errorf("compilerlock: query %s parameter evidence differs", q.ID)
		}
	}
	for i, v := range q.Results {
		if v != q.Evidence.Results[i] {
			return fmt.Errorf("compilerlock: query %s result evidence differs", q.ID)
		}
	}
	for _, v := range append(append([]ValueRecord{}, q.Parameters...), q.Results...) {
		if (v.TypeCertainty == "unknown" || v.NullabilityCertainty == "unknown") && !hasDiagnostic(q.Evidence.Diagnostics) {
			return fmt.Errorf("compilerlock: unknown query fact lacks diagnostic")
		}
	}
	return nil
}
func hasDiagnostic(d []string) bool {
	for _, code := range d {
		if code != "" {
			return true
		}
	}
	return false
}
func validateGeneration(g GenerationRecord, c CatalogRecord, queries []QueryRecord) error {
	if g.Package == "" || !token.IsIdentifier(g.Package) || g.Package == "_" || g.Emitter != "compact" && g.Emitter != "legacy" {
		return errors.New("compilerlock: invalid generation policy")
	}
	if _, err := NormalizePath(g.Output); err != nil {
		return fmt.Errorf("compilerlock: generation output: %w", err)
	}
	objects := map[string]struct{}{}
	for _, o := range c.Objects {
		objects[o.ID] = struct{}{}
	}
	seen := map[string]struct{}{}
	files := map[string]struct{}{}
	for _, o := range g.Objects {
		if o.ID == "" {
			return errors.New("compilerlock: empty generation object ID")
		}
		if _, ok := seen[o.ID]; ok {
			return fmt.Errorf("compilerlock: duplicate generation object %q", o.ID)
		}
		seen[o.ID] = struct{}{}
		if _, ok := objects[o.ID]; !ok {
			return fmt.Errorf("compilerlock: unknown generation object %q", o.ID)
		}
		if o.Source == "" || o.Row == "" || o.File == "" || !validGoName(o.Source) || !validGoName(o.Row) || o.Create != "" && !validGoName(o.Create) || o.Patch != "" && !validGoName(o.Patch) {
			return fmt.Errorf("compilerlock: incomplete generation object %q", o.ID)
		}
		if _, err := NormalizePath(o.File); err != nil {
			return err
		}
		if _, ok := files[o.File]; ok {
			return fmt.Errorf("compilerlock: conflicting generation file %q", o.File)
		}
		files[o.File] = struct{}{}
	}
	qids := map[string]struct{}{}
	queryIDs := map[string]struct{}{}
	for _, q := range queries {
		queryIDs[string(q.ID)] = struct{}{}
	}
	for _, q := range g.Queries {
		if q.ID == "" {
			return errors.New("compilerlock: empty generation query ID")
		}
		if _, ok := qids[q.ID]; ok {
			return fmt.Errorf("compilerlock: duplicate generation query %q", q.ID)
		}
		qids[q.ID] = struct{}{}
		if _, ok := queryIDs[q.ID]; !ok {
			return fmt.Errorf("compilerlock: unknown generation query %q", q.ID)
		}
		if q.Function == "" || q.Result == "" || q.Projection == "" || q.Decoder == "" || !validGoName(q.Function) || !validGoName(q.Result) || !validGoName(q.Projection) || !validGoName(q.Decoder) {
			return fmt.Errorf("compilerlock: incomplete generation query %q", q.ID)
		}
		if _, err := NormalizePath(q.File); err != nil {
			return err
		}
		if _, ok := files[q.File]; ok {
			return fmt.Errorf("compilerlock: conflicting generation file %q", q.File)
		}
		files[q.File] = struct{}{}
	}
	return nil
}
func validGoName(s string) bool {
	if s == "" || !token.IsIdentifier(s) || s == "_" {
		return false
	}
	switch s {
	case "break", "default", "func", "interface", "select", "case", "defer", "go", "map", "struct", "chan", "else", "goto", "package", "switch", "const", "fallthrough", "if", "range", "type", "continue", "for", "import", "return", "var":
		return false
	}
	return true
}
func validateSource(s SourceRecord) error {
	if s.Kind != "" && s.Kind != "migrations" && s.Kind != "external" && s.Kind != "live" {
		return fmt.Errorf("compilerlock: unsupported source kind %q", s.Kind)
	}
	seen := map[string]struct{}{}
	for _, f := range s.Files {
		p, err := NormalizePath(f.Path)
		if err != nil {
			return err
		}
		if _, ok := seen[p]; ok {
			return fmt.Errorf("compilerlock: duplicate source path %q", p)
		}
		seen[p] = struct{}{}
		if len(f.SHA256) != 64 {
			return fmt.Errorf("compilerlock: invalid sha256 for %q", p)
		}
		for _, c := range f.SHA256 {
			if !isLowerHex(c) {
				return fmt.Errorf("compilerlock: invalid sha256 for %q", p)
			}
		}
	}
	return nil
}
func isLowerHex(c rune) bool { return c >= '0' && c <= '9' || c >= 'a' && c <= 'f' }
func normalize(f File) File {
	f = cloneFile(f)
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
		sort.Slice(f.Catalog.Objects[i].ExclusionConstraints, func(a, b int) bool {
			return f.Catalog.Objects[i].ExclusionConstraints[a].Name < f.Catalog.Objects[i].ExclusionConstraints[b].Name
		})
	}
	sort.Slice(f.Generation.Objects, func(i, j int) bool { return f.Generation.Objects[i].ID < f.Generation.Objects[j].ID })
	sort.Slice(f.Generation.Queries, func(i, j int) bool { return f.Generation.Queries[i].ID < f.Generation.Queries[j].ID })
	for i := range f.Queries {
		sort.Strings(f.Queries[i].Evidence.Diagnostics)
	}
	return f
}

func cloneFile(f File) File {
	o := f
	o.Source.Files = cloneSlice(f.Source.Files)
	o.Catalog.Objects = cloneSlice(f.Catalog.Objects)
	for i := range o.Catalog.Objects {
		x := &o.Catalog.Objects[i]
		x.Columns = cloneSlice(x.Columns)
		x.Constraints = cloneSlice(x.Constraints)
		x.Indexes = cloneSlice(x.Indexes)
		x.ExclusionConstraints = cloneSlice(x.ExclusionConstraints)
		x.VirtualTableModuleArguments = cloneSlice(x.VirtualTableModuleArguments)
		for j := range x.Columns {
			x.Columns[j] = cloneColumn(x.Columns[j])
		}
		for j := range x.Constraints {
			x.Constraints[j] = cloneConstraint(x.Constraints[j])
		}
		for j := range x.Indexes {
			x.Indexes[j] = cloneIndex(x.Indexes[j])
		}
		x.ExclusionConstraints = append([]ExclusionConstraintRecord(nil), x.ExclusionConstraints...)
		for j := range x.ExclusionConstraints {
			x.ExclusionConstraints[j].Elements = append([]ExclusionElementRecord(nil), x.ExclusionConstraints[j].Elements...)
		}
	}
	o.Queries = cloneSlice(f.Queries)
	for i := range o.Queries {
		o.Queries[i].Parameters = cloneSlice(f.Queries[i].Parameters)
		o.Queries[i].Results = cloneSlice(f.Queries[i].Results)
		o.Queries[i].Evidence.Parameters = cloneSlice(f.Queries[i].Evidence.Parameters)
		o.Queries[i].Evidence.Results = cloneSlice(f.Queries[i].Evidence.Results)
		o.Queries[i].Evidence.Diagnostics = cloneSlice(f.Queries[i].Evidence.Diagnostics)
	}
	o.Generation.Objects = cloneSlice(f.Generation.Objects)
	o.Generation.Queries = cloneSlice(f.Generation.Queries)
	return o
}
func cloneSlice[T any](v []T) []T {
	if v == nil {
		return nil
	}
	return append(make([]T, 0, len(v)), v...)
}
func cloneColumn(c ColumnRecord) ColumnRecord {
	o := c
	if c.Native != nil {
		o.Native = cloneNativeRecord(c.Native)
	}
	if c.Integer != nil {
		x := *c.Integer
		o.Integer = &x
	}
	if c.Text != nil {
		x := *c.Text
		o.Text = &x
	}
	if c.Decimal != nil {
		x := *c.Decimal
		o.Decimal = &x
	}
	return o
}
func cloneNativeRecord(n *NativeTypeRecord) *NativeTypeRecord {
	if n == nil {
		return nil
	}
	o := *n
	if n.Arguments != nil {
		x := append(make([]string, 0, len(*n.Arguments)), (*n.Arguments)...)
		o.Arguments = &x
	}
	o.Element = cloneNativeRecord(n.Element)
	return &o
}
func cloneConstraint(c ConstraintRecord) ConstraintRecord {
	o := c
	o.Columns = append([]string(nil), c.Columns...)
	o.IncludeColumns = append([]string(nil), c.IncludeColumns...)
	o.Keys = append([]IndexPartRecord(nil), c.Keys...)
	o.DeleteSetColumns = append([]string(nil), c.DeleteSetColumns...)
	o.StorageParameters = cloneMap(c.StorageParameters)
	o.Collations = cloneMap(c.Collations)
	if c.Reference != nil {
		x := *c.Reference
		x.Columns = append([]string(nil), c.Reference.Columns...)
		o.Reference = &x
	}
	return o
}
func cloneIndex(i IndexRecord) IndexRecord {
	o := i
	o.Parts = append([]IndexPartRecord(nil), i.Parts...)
	o.IncludeColumns = append([]string(nil), i.IncludeColumns...)
	o.StorageParameters = cloneMap(i.StorageParameters)
	return o
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
	st, err := os.Stat(name)
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
	defer func() { _ = f.Close() }()
	r := io.LimitReader(f, MaxSourceFileBytes+1)
	b, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > MaxSourceFileBytes {
		return nil, fmt.Errorf("compilerlock: source exceeds %d bytes", MaxSourceFileBytes)
	}
	return b, nil
}
