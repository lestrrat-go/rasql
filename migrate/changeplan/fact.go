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
	Elements      []compilerir.ExclusionElement `json:"elements"`
	PredicateSQL  string                        `json:"predicate_sql"`
	Deferrability string                        `json:"deferrability"`
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
		out.ExclusionConstraints[i] = catalogExclusionWire{Name: value.Name, Method: value.Method, Elements: append([]compilerir.ExclusionElement(nil), value.Elements...), PredicateSQL: value.PredicateSQL, Deferrability: value.Deferrability}
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
func physicalObjectValue(object compilerir.PhysicalObject) map[string]any {
	value := map[string]any{"id": string(object.ID), "kind": object.Kind, "schema": object.Schema, "name": object.Name,
		"columns": make([]any, len(object.Columns)), "constraints": make([]any, len(object.Constraints)), "indexes": make([]any, len(object.Indexes)),
		"exclusion_constraints": make([]any, len(object.ExclusionConstraints)), "strict": object.Strict, "without_rowid": object.WithoutRowID,
		"primary_key_autoincrement": object.PrimaryKeyAutoincrement, "primary_key_on_conflict": object.PrimaryKeyOnConflict,
		"virtual_table_module": object.VirtualTableModule, "virtual_table_module_arguments": append([]string(nil), object.VirtualTableModuleArguments...)}
	columns := value["columns"].([]any)
	for i, column := range object.Columns {
		entry := map[string]any{"name": column.Name, "ordinal": column.Ordinal, "logical_kind": column.LogicalKind, "native": nativeValue(column.Native), "nullable": column.Nullable,
			"default_sql": column.DefaultSQL, "generated_sql": column.GeneratedSQL, "generated_storage": column.GeneratedStorage, "identity": column.Identity, "collation": column.Collation, "hidden": column.Hidden,
			"integer": integerValue(column.Integer), "text": textValue(column.Text), "decimal": decimalValue(column.Decimal)}
		columns[i] = entry
	}
	constraints := value["constraints"].([]any)
	for i, constraint := range object.Constraints {
		constraints[i] = physicalConstraintValue(constraint)
	}
	indexes := value["indexes"].([]any)
	for i, index := range object.Indexes {
		indexes[i] = physicalIndexValue(index)
	}
	exclusions := value["exclusion_constraints"].([]any)
	for i, exclusion := range object.ExclusionConstraints {
		exclusions[i] = map[string]any{"name": exclusion.Name, "method": exclusion.Method, "elements": exclusion.Elements, "predicate_sql": exclusion.PredicateSQL, "deferrability": exclusion.Deferrability}
	}
	return value
}
func nativeValue(value *compilerir.NativeType) any {
	if value == nil {
		return nil
	}
	out := map[string]any{"dialect": value.Dialect, "schema": value.Schema, "name": value.Name, "kind": value.Kind, "arguments": append([]string(nil), value.Arguments...)}
	out["element"] = nativeValue(value.Element)
	return out
}
func integerValue(value *compilerir.IntegerTypeFacts) any {
	if value == nil {
		return nil
	}
	return map[string]any{"unsigned": value.Unsigned, "display_width": map[string]any{"value": value.DisplayWidth.Value, "set": value.DisplayWidth.Set}, "zero_fill": value.ZeroFill}
}
func textValue(value *compilerir.TextTypeFacts) any {
	if value == nil {
		return nil
	}
	return map[string]any{"width": map[string]any{"value": value.Width.Value, "set": value.Width.Set}, "fixed": value.Fixed}
}
func decimalValue(value *compilerir.DecimalTypeFacts) any {
	if value == nil {
		return nil
	}
	return map[string]any{"precision": value.Precision, "scale": map[string]any{"value": value.Scale.Value, "set": value.Scale.Set}, "unsigned": value.Unsigned, "zero_fill": value.ZeroFill}
}
func physicalConstraintValue(value compilerir.PhysicalConstraint) map[string]any {
	return map[string]any{"name": value.Name, "kind": value.Kind, "columns": append([]string(nil), value.Columns...), "reference": referenceValue(value.Reference), "expression_sql": value.ExpressionSQL,
		"deferrable": value.Deferrable, "initially_deferred": value.InitiallyDeferred, "on_update": value.OnUpdate, "on_delete": value.OnDelete, "deferrability": value.Deferrability, "match": value.Match,
		"nulls_not_distinct": value.NullsNotDistinct, "include_columns": append([]string(nil), value.IncludeColumns...), "on_conflict": value.OnConflict, "keys": value.Keys, "temporal": value.Temporal,
		"storage_parameters": sortedStringPairs(value.StorageParameters), "tablespace": value.Tablespace, "replica_identity": value.ReplicaIdentity, "collations": sortedStringPairs(value.Collations),
		"no_inherit": value.NoInherit, "not_valid": value.NotValid, "not_enforced": value.NotEnforced, "delete_set_columns": append([]string(nil), value.DeleteSetColumns...)}
}
func referenceValue(value *compilerir.ForeignReference) any {
	if value == nil {
		return nil
	}
	return map[string]any{"schema": value.Schema, "object": value.Object, "columns": append([]string(nil), value.Columns...)}
}
func physicalIndexValue(value compilerir.PhysicalIndex) map[string]any {
	return map[string]any{"name": value.Name, "unique": value.Unique, "method": value.Method, "key_form": value.KeyForm, "parts": value.Parts, "predicate_sql": value.PredicateSQL,
		"include_columns": append([]string(nil), value.IncludeColumns...), "invisible": value.Invisible, "not_valid": value.NotValid, "storage_parameters": sortedStringPairs(value.StorageParameters), "tablespace": value.Tablespace,
		"replica_identity": value.ReplicaIdentity, "nulls_not_distinct": value.NullsNotDistinct}
}
func sortedStringPairs(values map[string]string) []map[string]string {
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]map[string]string, len(keys))
	for i, key := range keys {
		out[i] = map[string]string{"key": key, "value": values[key]}
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
	var object map[string]any
	for _, candidate := range c.physical.Objects {
		if ObjectID(candidate.ID) == fact.object {
			object = physicalObjectValue(candidate)
			break
		}
	}
	if object == nil {
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
		if fact.path != "$" {
			if _, object := value.(map[string]any); object {
				return fmt.Errorf("%w: equality path %s selects an object", ErrInvalidFact, fact.path)
			}
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
func mustJSON(value any) []byte { b, _ := marshalNoHTML(value); return b }
func walkFactPath(root map[string]any, path string) (bool, any, error) {
	if path == "$" {
		return true, root, nil
	}
	if !strings.HasPrefix(path, "/") {
		return false, nil, fmt.Errorf("%w: path must begin with /", ErrInvalidFact)
	}
	current := any(root)
	for _, raw := range strings.Split(path[1:], "/") {
		if !validPointerToken(raw) {
			return false, nil, fmt.Errorf("%w: malformed escape", ErrInvalidFact)
		}
		segment := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		switch value := current.(type) {
		case map[string]any:
			next, ok := value[segment]
			if !ok {
				return false, nil, fmt.Errorf("%w: path segment %q is unknown", ErrFactMismatch, segment)
			}
			current = next
		case []any:
			if segment == "" || (len(segment) > 1 && segment[0] == '0') {
				return false, nil, fmt.Errorf("%w: array index %q is non-canonical", ErrInvalidFact, segment)
			}
			index, err := strconv.Atoi(segment)
			if err != nil || index < 0 || index >= len(value) {
				return false, nil, fmt.Errorf("%w: array index %q is invalid", ErrFactMismatch, segment)
			}
			current = value[index]
		default:
			return false, nil, fmt.Errorf("%w: path crosses scalar", ErrInvalidFact)
		}
	}
	return true, current, nil
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
