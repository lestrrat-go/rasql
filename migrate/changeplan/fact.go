package changeplan

import (
	"bytes"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/lestrrat-go/rasql/internal/compilerir"
)

type catalogWire struct {
	Engine  catalogEngineWire   `json:"engine"`
	Objects []catalogObjectWire `json:"objects"`
}

type catalogObjectWire struct {
	ID                          string                  `json:"id"`
	Kind                        string                  `json:"kind"`
	Schema                      string                  `json:"schema"`
	Name                        string                  `json:"name"`
	Columns                     []catalogColumnWire     `json:"columns"`
	Constraints                 []catalogConstraintWire `json:"constraints"`
	Indexes                     []catalogIndexWire      `json:"indexes"`
	ExclusionConstraints        []catalogExclusionWire  `json:"exclusion_constraints"`
	Strict                      bool                    `json:"strict"`
	WithoutRowID                bool                    `json:"without_rowid"`
	PrimaryKeyAutoincrement     bool                    `json:"primary_key_autoincrement"`
	PrimaryKeyOnConflict        string                  `json:"primary_key_on_conflict"`
	VirtualTableModule          string                  `json:"virtual_table_module"`
	VirtualTableModuleArguments []string                `json:"virtual_table_module_arguments"`
}
type catalogColumnWire struct {
	Name             string              `json:"name"`
	Ordinal          int                 `json:"ordinal"`
	LogicalKind      string              `json:"logical_kind"`
	Native           *catalogNativeWire  `json:"native"`
	Nullable         bool                `json:"nullable"`
	DefaultSQL       string              `json:"default_sql"`
	GeneratedSQL     string              `json:"generated_sql"`
	GeneratedStorage string              `json:"generated_storage"`
	Identity         string              `json:"identity"`
	Collation        string              `json:"collation"`
	Hidden           bool                `json:"hidden"`
	Integer          *catalogIntegerWire `json:"integer"`
	Text             *catalogTextWire    `json:"text"`
	Decimal          *catalogDecimalWire `json:"decimal"`
}
type catalogNativeWire struct {
	Dialect   string             `json:"dialect"`
	Schema    string             `json:"schema"`
	Name      string             `json:"name"`
	Kind      string             `json:"kind"`
	Arguments []string           `json:"arguments"`
	Element   *catalogNativeWire `json:"element"`
}
type catalogOptionalIntWire struct {
	Value int  `json:"value"`
	Set   bool `json:"set"`
}
type catalogIntegerWire struct {
	Unsigned     bool                   `json:"unsigned"`
	DisplayWidth catalogOptionalIntWire `json:"display_width"`
	ZeroFill     bool                   `json:"zero_fill"`
}
type catalogTextWire struct {
	Width catalogOptionalIntWire `json:"width"`
	Fixed bool                   `json:"fixed"`
}
type catalogDecimalWire struct {
	Precision int                    `json:"precision"`
	Scale     catalogOptionalIntWire `json:"scale"`
	Unsigned  bool                   `json:"unsigned"`
	ZeroFill  bool                   `json:"zero_fill"`
}
type catalogReferenceWire struct {
	Schema  string   `json:"schema"`
	Object  string   `json:"object"`
	Columns []string `json:"columns"`
}
type catalogIndexPartWire struct {
	Column        string `json:"column"`
	ExpressionSQL string `json:"expression_sql"`
	Direction     string `json:"direction"`
	Nulls         string `json:"nulls"`
	Collation     string `json:"collation"`
	OperatorClass string `json:"operator_class"`
	PrefixLength  int    `json:"prefix_length"`
}
type catalogPairWire struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}
type catalogConstraintWire struct {
	Name              string                 `json:"name"`
	Kind              string                 `json:"kind"`
	Columns           []string               `json:"columns"`
	Reference         *catalogReferenceWire  `json:"reference"`
	ExpressionSQL     string                 `json:"expression_sql"`
	Deferrable        bool                   `json:"deferrable"`
	InitiallyDeferred bool                   `json:"initially_deferred"`
	OnUpdate          string                 `json:"on_update"`
	OnDelete          string                 `json:"on_delete"`
	Deferrability     string                 `json:"deferrability"`
	Match             string                 `json:"match"`
	NullsNotDistinct  bool                   `json:"nulls_not_distinct"`
	IncludeColumns    []string               `json:"include_columns"`
	OnConflict        string                 `json:"on_conflict"`
	Keys              []catalogIndexPartWire `json:"keys"`
	Temporal          bool                   `json:"temporal"`
	StorageParameters []catalogPairWire      `json:"storage_parameters"`
	Tablespace        string                 `json:"tablespace"`
	ReplicaIdentity   bool                   `json:"replica_identity"`
	Collations        []catalogPairWire      `json:"collations"`
	NoInherit         bool                   `json:"no_inherit"`
	NotValid          bool                   `json:"not_valid"`
	NotEnforced       bool                   `json:"not_enforced"`
	DeleteSetColumns  []string               `json:"delete_set_columns"`
}
type catalogIndexWire struct {
	Name              string                 `json:"name"`
	Unique            bool                   `json:"unique"`
	Method            string                 `json:"method"`
	KeyForm           string                 `json:"key_form"`
	Parts             []catalogIndexPartWire `json:"parts"`
	PredicateSQL      string                 `json:"predicate_sql"`
	IncludeColumns    []string               `json:"include_columns"`
	Invisible         bool                   `json:"invisible"`
	NotValid          bool                   `json:"not_valid"`
	NullsNotDistinct  bool                   `json:"nulls_not_distinct"`
	StorageParameters []catalogPairWire      `json:"storage_parameters"`
	Tablespace        string                 `json:"tablespace"`
	ReplicaIdentity   bool                   `json:"replica_identity"`
}
type catalogExclusionWire struct {
	Name          string                        `json:"name"`
	Method        string                        `json:"method"`
	Elements      []catalogExclusionElementWire `json:"elements"`
	PredicateSQL  string                        `json:"predicate_sql"`
	Deferrability string                        `json:"deferrability"`
}
type catalogExclusionElementWire struct {
	ExpressionSQL string `json:"expression_sql"`
	Operator      string `json:"operator"`
}
type catalogEngineWire struct {
	Dialect string `json:"dialect"`
	Version string `json:"version"`
	Profile string `json:"profile"`
}

func catalogValue(c compilerir.PhysicalCatalog) catalogWire {
	objects := append([]compilerir.PhysicalObject(nil), c.Objects...)
	sort.SliceStable(objects, func(i, j int) bool { return string(objects[i].ID) < string(objects[j].ID) })
	out := catalogWire{Engine: catalogEngineWire{Dialect: c.Engine.Dialect, Version: c.Engine.Version, Profile: c.Engine.Profile}, Objects: make([]catalogObjectWire, len(objects))}
	for i, object := range objects {
		out.Objects[i] = catalogObjectValue(object)
	}
	return out
}
func catalogObjectValue(object compilerir.PhysicalObject) catalogObjectWire {
	out := catalogObjectWire{ID: string(object.ID), Kind: object.Kind, Schema: object.Schema, Name: object.Name, Strict: object.Strict, WithoutRowID: object.WithoutRowID, PrimaryKeyAutoincrement: object.PrimaryKeyAutoincrement, PrimaryKeyOnConflict: object.PrimaryKeyOnConflict, VirtualTableModule: object.VirtualTableModule, VirtualTableModuleArguments: append([]string(nil), object.VirtualTableModuleArguments...)}
	out.Columns = make([]catalogColumnWire, len(object.Columns))
	for i, value := range object.Columns {
		out.Columns[i] = catalogColumnValue(value)
	}
	out.Constraints = make([]catalogConstraintWire, len(object.Constraints))
	for i, value := range object.Constraints {
		out.Constraints[i] = catalogConstraintValue(value)
	}
	out.Indexes = make([]catalogIndexWire, len(object.Indexes))
	for i, value := range object.Indexes {
		out.Indexes[i] = catalogIndexValue(value)
	}
	out.ExclusionConstraints = make([]catalogExclusionWire, len(object.ExclusionConstraints))
	for i, value := range object.ExclusionConstraints {
		exclusion := catalogExclusionWire{Name: value.Name, Method: value.Method, PredicateSQL: value.PredicateSQL, Deferrability: value.Deferrability}
		exclusion.Elements = make([]catalogExclusionElementWire, len(value.Elements))
		for j, element := range value.Elements {
			exclusion.Elements[j] = catalogExclusionElementWire{ExpressionSQL: element.ExpressionSQL, Operator: element.Operator}
		}
		out.ExclusionConstraints[i] = exclusion
	}
	return out
}
func catalogColumnValue(value compilerir.PhysicalColumn) catalogColumnWire {
	out := catalogColumnWire{Name: value.Name, Ordinal: value.Ordinal, LogicalKind: value.LogicalKind, Native: catalogNativeValue(value.Native), Nullable: value.Nullable, DefaultSQL: value.DefaultSQL, GeneratedSQL: value.GeneratedSQL, GeneratedStorage: value.GeneratedStorage, Identity: value.Identity, Collation: value.Collation, Hidden: value.Hidden}
	if value.Integer != nil {
		out.Integer = &catalogIntegerWire{Unsigned: value.Integer.Unsigned, DisplayWidth: catalogOptionalIntWire{Value: value.Integer.DisplayWidth.Value, Set: value.Integer.DisplayWidth.Set}, ZeroFill: value.Integer.ZeroFill}
	}
	if value.Text != nil {
		out.Text = &catalogTextWire{Width: catalogOptionalIntWire{Value: value.Text.Width.Value, Set: value.Text.Width.Set}, Fixed: value.Text.Fixed}
	}
	if value.Decimal != nil {
		out.Decimal = &catalogDecimalWire{Precision: value.Decimal.Precision, Scale: catalogOptionalIntWire{Value: value.Decimal.Scale.Value, Set: value.Decimal.Scale.Set}, Unsigned: value.Decimal.Unsigned, ZeroFill: value.Decimal.ZeroFill}
	}
	return out
}
func catalogNativeValue(value *compilerir.NativeType) *catalogNativeWire {
	if value == nil {
		return nil
	}
	return &catalogNativeWire{Dialect: value.Dialect, Schema: value.Schema, Name: value.Name, Kind: value.Kind, Arguments: append([]string(nil), value.Arguments...), Element: catalogNativeValue(value.Element)}
}
func catalogConstraintValue(value compilerir.PhysicalConstraint) catalogConstraintWire {
	out := catalogConstraintWire{Name: value.Name, Kind: value.Kind, Columns: append([]string(nil), value.Columns...), ExpressionSQL: value.ExpressionSQL, Deferrable: value.Deferrable, InitiallyDeferred: value.InitiallyDeferred, OnUpdate: value.OnUpdate, OnDelete: value.OnDelete, Deferrability: value.Deferrability, Match: value.Match, NullsNotDistinct: value.NullsNotDistinct, IncludeColumns: append([]string(nil), value.IncludeColumns...), OnConflict: value.OnConflict, Temporal: value.Temporal, StorageParameters: sortedWirePairs(value.StorageParameters), Tablespace: value.Tablespace, ReplicaIdentity: value.ReplicaIdentity, Collations: sortedWirePairs(value.Collations), NoInherit: value.NoInherit, NotValid: value.NotValid, NotEnforced: value.NotEnforced, DeleteSetColumns: append([]string(nil), value.DeleteSetColumns...)}
	if value.Reference != nil {
		out.Reference = &catalogReferenceWire{Schema: value.Reference.Schema, Object: value.Reference.Object, Columns: append([]string(nil), value.Reference.Columns...)}
	}
	out.Keys = make([]catalogIndexPartWire, len(value.Keys))
	for i, key := range value.Keys {
		out.Keys[i] = catalogIndexPartWire{Column: key.Column, ExpressionSQL: key.ExpressionSQL, Direction: key.Direction, Nulls: key.Nulls, Collation: key.Collation, OperatorClass: key.OperatorClass, PrefixLength: key.PrefixLength}
	}
	return out
}
func catalogIndexValue(value compilerir.PhysicalIndex) catalogIndexWire {
	out := catalogIndexWire{Name: value.Name, Unique: value.Unique, Method: value.Method, KeyForm: value.KeyForm, PredicateSQL: value.PredicateSQL, IncludeColumns: append([]string(nil), value.IncludeColumns...), Invisible: value.Invisible, NotValid: value.NotValid, NullsNotDistinct: value.NullsNotDistinct, StorageParameters: sortedWirePairs(value.StorageParameters), Tablespace: value.Tablespace, ReplicaIdentity: value.ReplicaIdentity}
	out.Parts = make([]catalogIndexPartWire, len(value.Parts))
	for i, part := range value.Parts {
		out.Parts[i] = catalogIndexPartWire{Column: part.Column, ExpressionSQL: part.ExpressionSQL, Direction: part.Direction, Nulls: part.Nulls, Collation: part.Collation, OperatorClass: part.OperatorClass, PrefixLength: part.PrefixLength}
	}
	return out
}
func sortedWirePairs(values map[string]string) []catalogPairWire {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]catalogPairWire, len(keys))
	for i, key := range keys {
		out[i] = catalogPairWire{Key: key, Value: values[key]}
	}
	return out
}
func CatalogDigest(c Catalog) (Digest, error) {
	if err := c.validate(); err != nil {
		return Digest{}, fmt.Errorf("%w: catalog: %v", ErrInvalidIdentity, err)
	}
	b, err := marshalNoHTML(catalogValue(c.physical))
	if err != nil {
		return Digest{}, err
	}
	return sha256Digest(b), nil
}
func EvaluateFacts(c Catalog, facts []Fact) error {
	for _, fact := range facts {
		if err := EvaluateFact(c, fact); err != nil {
			return err
		}
	}
	return nil
}
func EvaluateFact(c Catalog, fact Fact) error {
	if err := c.validate(); err != nil {
		return fmt.Errorf("%w: catalog: %v", ErrInvalidFact, err)
	}
	if err := validateFact(fact); err != nil {
		return err
	}
	var object catalogObjectWire
	found := false
	for _, candidate := range c.physical.Objects {
		if ObjectID(candidate.ID) == fact.object {
			object = catalogObjectValue(candidate)
			found = true
			break
		}
	}
	if !found {
		if fact.operator == string(FactOperatorAbsent) && fact.path == "$" {
			return nil
		}
		return fmt.Errorf("%w: object %q is missing", ErrFactMismatch, fact.object)
	}
	present, value, err := walkFactPath(object, fact.path)
	if err != nil {
		return err
	}
	switch FactOperator(fact.operator) {
	case FactOperatorPresent:
		if !present {
			return fmt.Errorf("%w: %s is absent", ErrFactMismatch, fact.path)
		}
	case FactOperatorAbsent:
		if present {
			return fmt.Errorf("%w: %s is present", ErrFactMismatch, fact.path)
		}
	case FactOperatorEqual:
		if !present {
			return fmt.Errorf("%w: %s is absent", ErrFactMismatch, fact.path)
		}
		if fact.path != "$" && factValueIsStruct(value) {
			return fmt.Errorf("%w: equality path %s selects an object", ErrInvalidFact, fact.path)
		}
		actual, err := canonicalJSON(mustJSON(value))
		if err != nil {
			return err
		}
		if !bytes.Equal(actual, []byte(fact.canonicalValue)) {
			return fmt.Errorf("%w: %s differs", ErrFactMismatch, fact.path)
		}
	}
	return nil
}

func factValueIsStruct(value any) bool {
	switch value := value.(type) {
	case catalogObjectWire, catalogColumnWire, catalogNativeWire, catalogOptionalIntWire,
		catalogIntegerWire, catalogTextWire, catalogDecimalWire, catalogReferenceWire,
		catalogIndexPartWire, catalogPairWire, catalogConstraintWire, catalogIndexWire,
		catalogExclusionWire, catalogExclusionElementWire:
		return true
	case *catalogNativeWire:
		return value != nil
	case *catalogIntegerWire:
		return value != nil
	case *catalogTextWire:
		return value != nil
	case *catalogDecimalWire:
		return value != nil
	case *catalogReferenceWire:
		return value != nil
	case *catalogConstraintWire:
		return value != nil
	case *catalogIndexWire:
		return value != nil
	case *catalogExclusionWire:
		return value != nil
	case *catalogExclusionElementWire:
		return value != nil
	default:
		return false
	}
}
func mustJSON(value any) []byte { b, _ := marshalNoHTML(value); return b }
func walkFactPath(root catalogObjectWire, path string) (bool, any, error) {
	if path == "$" {
		return true, root, nil
	}
	if !strings.HasPrefix(path, "/") {
		return false, nil, fmt.Errorf("%w: path must begin with /", ErrInvalidFact)
	}
	segments := strings.Split(path[1:], "/")
	return walkFactValue(root, segments)
}

func walkFactValue(current any, segments []string) (bool, any, error) {
	if len(segments) == 0 {
		return true, current, nil
	}
	raw := segments[0]
	if !validPointerToken(raw) {
		return false, nil, fmt.Errorf("%w: malformed escape", ErrInvalidFact)
	}
	segment := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
	next, err := factField(current, segment)
	if err != nil {
		return false, nil, err
	}
	return walkFactValue(next, segments[1:])
}

func factField(current any, segment string) (any, error) {
	switch value := current.(type) {
	case catalogObjectWire:
		switch segment {
		case "id":
			return value.ID, nil
		case "kind":
			return value.Kind, nil
		case "schema":
			return value.Schema, nil
		case "name":
			return value.Name, nil
		case "columns":
			return value.Columns, nil
		case "constraints":
			return value.Constraints, nil
		case "indexes":
			return value.Indexes, nil
		case "exclusion_constraints":
			return value.ExclusionConstraints, nil
		case "strict":
			return value.Strict, nil
		case "without_rowid":
			return value.WithoutRowID, nil
		case "primary_key_autoincrement":
			return value.PrimaryKeyAutoincrement, nil
		case "primary_key_on_conflict":
			return value.PrimaryKeyOnConflict, nil
		case "virtual_table_module":
			return value.VirtualTableModule, nil
		case "virtual_table_module_arguments":
			return value.VirtualTableModuleArguments, nil
		default:
			return nil, unknownFactField(segment)
		}
	case catalogColumnWire:
		switch segment {
		case "name":
			return value.Name, nil
		case "ordinal":
			return value.Ordinal, nil
		case "logical_kind":
			return value.LogicalKind, nil
		case "native":
			return value.Native, nil
		case "nullable":
			return value.Nullable, nil
		case "default_sql":
			return value.DefaultSQL, nil
		case "generated_sql":
			return value.GeneratedSQL, nil
		case "generated_storage":
			return value.GeneratedStorage, nil
		case "identity":
			return value.Identity, nil
		case "collation":
			return value.Collation, nil
		case "hidden":
			return value.Hidden, nil
		case "integer":
			return value.Integer, nil
		case "text":
			return value.Text, nil
		case "decimal":
			return value.Decimal, nil
		default:
			return nil, unknownFactField(segment)
		}
	case catalogNativeWire:
		switch segment {
		case "dialect":
			return value.Dialect, nil
		case "schema":
			return value.Schema, nil
		case "name":
			return value.Name, nil
		case "kind":
			return value.Kind, nil
		case "arguments":
			return value.Arguments, nil
		case "element":
			return value.Element, nil
		default:
			return nil, unknownFactField(segment)
		}
	case *catalogNativeWire:
		if value == nil {
			return nil, fmt.Errorf("%w: path crosses null", ErrInvalidFact)
		}
		return factField(*value, segment)
	case catalogOptionalIntWire:
		switch segment {
		case "value":
			return value.Value, nil
		case "set":
			return value.Set, nil
		default:
			return nil, unknownFactField(segment)
		}
	case catalogIntegerWire:
		switch segment {
		case "unsigned":
			return value.Unsigned, nil
		case "display_width":
			return value.DisplayWidth, nil
		case "zero_fill":
			return value.ZeroFill, nil
		default:
			return nil, unknownFactField(segment)
		}
	case *catalogIntegerWire:
		if value == nil {
			return nil, fmt.Errorf("%w: path crosses null", ErrInvalidFact)
		}
		return factField(*value, segment)
	case catalogTextWire:
		switch segment {
		case "width":
			return value.Width, nil
		case "fixed":
			return value.Fixed, nil
		default:
			return nil, unknownFactField(segment)
		}
	case *catalogTextWire:
		if value == nil {
			return nil, fmt.Errorf("%w: path crosses null", ErrInvalidFact)
		}
		return factField(*value, segment)
	case catalogDecimalWire:
		switch segment {
		case "precision":
			return value.Precision, nil
		case "scale":
			return value.Scale, nil
		case "unsigned":
			return value.Unsigned, nil
		case "zero_fill":
			return value.ZeroFill, nil
		default:
			return nil, unknownFactField(segment)
		}
	case *catalogDecimalWire:
		if value == nil {
			return nil, fmt.Errorf("%w: path crosses null", ErrInvalidFact)
		}
		return factField(*value, segment)
	case catalogReferenceWire:
		switch segment {
		case "schema":
			return value.Schema, nil
		case "object":
			return value.Object, nil
		case "columns":
			return value.Columns, nil
		default:
			return nil, unknownFactField(segment)
		}
	case *catalogReferenceWire:
		if value == nil {
			return nil, fmt.Errorf("%w: path crosses null", ErrInvalidFact)
		}
		return factField(*value, segment)
	case catalogIndexPartWire:
		switch segment {
		case "column":
			return value.Column, nil
		case "expression_sql":
			return value.ExpressionSQL, nil
		case "direction":
			return value.Direction, nil
		case "nulls":
			return value.Nulls, nil
		case "collation":
			return value.Collation, nil
		case "operator_class":
			return value.OperatorClass, nil
		case "prefix_length":
			return value.PrefixLength, nil
		default:
			return nil, unknownFactField(segment)
		}
	case catalogPairWire:
		switch segment {
		case "key":
			return value.Key, nil
		case "value":
			return value.Value, nil
		default:
			return nil, unknownFactField(segment)
		}
	case catalogConstraintWire:
		switch segment {
		case "name":
			return value.Name, nil
		case "kind":
			return value.Kind, nil
		case "columns":
			return value.Columns, nil
		case "reference":
			return value.Reference, nil
		case "expression_sql":
			return value.ExpressionSQL, nil
		case "deferrable":
			return value.Deferrable, nil
		case "initially_deferred":
			return value.InitiallyDeferred, nil
		case "on_update":
			return value.OnUpdate, nil
		case "on_delete":
			return value.OnDelete, nil
		case "deferrability":
			return value.Deferrability, nil
		case "match":
			return value.Match, nil
		case "nulls_not_distinct":
			return value.NullsNotDistinct, nil
		case "include_columns":
			return value.IncludeColumns, nil
		case "on_conflict":
			return value.OnConflict, nil
		case "keys":
			return value.Keys, nil
		case "temporal":
			return value.Temporal, nil
		case "storage_parameters":
			return value.StorageParameters, nil
		case "tablespace":
			return value.Tablespace, nil
		case "replica_identity":
			return value.ReplicaIdentity, nil
		case "collations":
			return value.Collations, nil
		case "no_inherit":
			return value.NoInherit, nil
		case "not_valid":
			return value.NotValid, nil
		case "not_enforced":
			return value.NotEnforced, nil
		case "delete_set_columns":
			return value.DeleteSetColumns, nil
		default:
			return nil, unknownFactField(segment)
		}
	case *catalogConstraintWire:
		if value == nil {
			return nil, fmt.Errorf("%w: path crosses null", ErrInvalidFact)
		}
		return factField(*value, segment)
	case catalogIndexWire:
		switch segment {
		case "name":
			return value.Name, nil
		case "unique":
			return value.Unique, nil
		case "method":
			return value.Method, nil
		case "key_form":
			return value.KeyForm, nil
		case "parts":
			return value.Parts, nil
		case "predicate_sql":
			return value.PredicateSQL, nil
		case "include_columns":
			return value.IncludeColumns, nil
		case "invisible":
			return value.Invisible, nil
		case "not_valid":
			return value.NotValid, nil
		case "nulls_not_distinct":
			return value.NullsNotDistinct, nil
		case "storage_parameters":
			return value.StorageParameters, nil
		case "tablespace":
			return value.Tablespace, nil
		case "replica_identity":
			return value.ReplicaIdentity, nil
		default:
			return nil, unknownFactField(segment)
		}
	case *catalogIndexWire:
		if value == nil {
			return nil, fmt.Errorf("%w: path crosses null", ErrInvalidFact)
		}
		return factField(*value, segment)
	case catalogExclusionWire:
		switch segment {
		case "name":
			return value.Name, nil
		case "method":
			return value.Method, nil
		case "elements":
			return value.Elements, nil
		case "predicate_sql":
			return value.PredicateSQL, nil
		case "deferrability":
			return value.Deferrability, nil
		default:
			return nil, unknownFactField(segment)
		}
	case catalogExclusionElementWire:
		switch segment {
		case "expression_sql":
			return value.ExpressionSQL, nil
		case "operator":
			return value.Operator, nil
		default:
			return nil, unknownFactField(segment)
		}
	case *catalogExclusionWire:
		if value == nil {
			return nil, fmt.Errorf("%w: path crosses null", ErrInvalidFact)
		}
		return factField(*value, segment)
	case *catalogExclusionElementWire:
		if value == nil {
			return nil, fmt.Errorf("%w: path crosses null", ErrInvalidFact)
		}
		return factField(*value, segment)
	case []catalogColumnWire:
		return factArrayIndex(value, segment)
	case []catalogConstraintWire:
		return factArrayIndex(value, segment)
	case []catalogIndexWire:
		return factArrayIndex(value, segment)
	case []catalogExclusionWire:
		return factArrayIndex(value, segment)
	case []catalogExclusionElementWire:
		return factArrayIndex(value, segment)
	case []catalogIndexPartWire:
		return factArrayIndex(value, segment)
	case []catalogPairWire:
		return factArrayIndex(value, segment)
	case []string:
		return factArrayIndex(value, segment)
	default:
		return nil, fmt.Errorf("%w: path crosses scalar", ErrInvalidFact)
	}
}

func factArrayIndex(value any, segment string) (any, error) {
	if segment == "" || (len(segment) > 1 && segment[0] == '0') {
		return nil, fmt.Errorf("%w: array index %q is non-canonical", ErrInvalidFact, segment)
	}
	index, err := strconv.Atoi(segment)
	if err != nil || index < 0 {
		return nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
	}
	switch values := value.(type) {
	case []catalogColumnWire:
		if index >= len(values) {
			return nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
		}
		return values[index], nil
	case []catalogConstraintWire:
		if index >= len(values) {
			return nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
		}
		return values[index], nil
	case []catalogIndexWire:
		if index >= len(values) {
			return nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
		}
		return values[index], nil
	case []catalogExclusionWire:
		if index >= len(values) {
			return nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
		}
		return values[index], nil
	case []catalogExclusionElementWire:
		if index >= len(values) {
			return nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
		}
		return values[index], nil
	case []catalogIndexPartWire:
		if index >= len(values) {
			return nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
		}
		return values[index], nil
	case []catalogPairWire:
		if index >= len(values) {
			return nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
		}
		return values[index], nil
	case []string:
		if index >= len(values) {
			return nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
		}
		return values[index], nil
	default:
		return nil, fmt.Errorf("%w: path crosses scalar", ErrInvalidFact)
	}
}

func unknownFactField(segment string) error {
	return fmt.Errorf("%w: path segment %q is unknown", ErrFactMismatch, segment)
}
func validPointerToken(token string) bool {
	for i := 0; i < len(token); i++ {
		if token[i] != '~' {
			continue
		}
		if i+1 == len(token) || (token[i+1] != '0' && token[i+1] != '1') {
			return false
		}
		i++
	}
	return true
}
func sha256Digest(data []byte) Digest { return Digest(sha256Sum(data)) }
