package changeplan

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type sourceRepairProfileSource struct{ value engineprofile.Profile }

func (s sourceRepairProfileSource) ID() string                       { return s.value.ID }
func (s sourceRepairProfileSource) Engine() EngineID                 { return s.value.Engine }
func (s sourceRepairProfileSource) Version() EngineVersion           { return s.value.Version }
func (s sourceRepairProfileSource) Capabilities() EngineCapabilities { return s.value.Capabilities }
func (s sourceRepairProfileSource) Limits() EngineLimits             { return s.value.Limits }

func sourceRepairProfile(t *testing.T) Profile {
	t.Helper()
	value, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	profile, err := NewProfile(sourceRepairProfileSource{value: value})
	require.NoError(t, err)
	return profile
}

func TestTypedFactWalkerTraversesD2CatalogFields(t *testing.T) {
	definition := schema.TableDef{
		Schema: "public",
		Name:   "orders",
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
		},
		ExclusionConstraints: []schema.ExclusionDef{{
			Name:     "orders_exclusion",
			Method:   "gist",
			Elements: []schema.ExclusionElementDef{{Expression: "id", Operator: "="}},
		}},
	}
	object, err := NewCatalogObject("orders-id", definition)
	require.NoError(t, err)
	catalog, err := NewCatalog(sourceRepairProfile(t), "exclusion-source", []CatalogObject{object})
	require.NoError(t, err)

	for _, test := range []struct {
		path  string
		value string
	}{
		{path: "/id", value: `"orders-id"`},
		{path: "/columns/0/name", value: `"id"`},
		{path: "/exclusion_constraints/0/name", value: `"orders_exclusion"`},
		{path: "/exclusion_constraints/0/elements/0/expression_sql", value: `"id"`},
		{path: "/exclusion_constraints/0/elements/0/operator", value: `"="`},
	} {
		fact, factErr := NewFact(object.ID(), test.path, FactEqual, test.value)
		require.NoError(t, factErr)
		require.NoError(t, EvaluateFact(catalog, fact), test.path)
	}

	structFact, err := NewFact(object.ID(), "/columns/0", FactEqual, `{"name":"id"}`)
	require.NoError(t, err)
	err = EvaluateFact(catalog, structFact)
	require.ErrorIs(t, err, ErrInvalidFact)
}

func TestPlanRenameDecisionUsesStructuredTableBinding(t *testing.T) {
	starting, err := NewBaselineObject("starting", "table", "main", "users")
	require.NoError(t, err)
	profile := sourceRepairProfile(t)
	profileDigest, err := ProfileDigest(profile)
	require.NoError(t, err)
	identity, err := NewCatalogIdentity(profile.Engine(), profileDigest, Digest{1}, Digest{2})
	require.NoError(t, err)
	rename, err := NewBaselineRename("rename", starting.ID(), "main", "accounts")
	require.NoError(t, err)
	baseline, err := NewBaselineIdentity(identity, "source", []BaselineObject{starting}, []BaselineRename{rename})
	require.NoError(t, err)
	history, err := NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	decision, err := NewDecision("rename-decision", DecisionRenameObject, starting.ID(), "main.wrong", "main.other", true, "")
	require.NoError(t, err)
	operation, err := NewOperation("rename", OperationRenameTable, nil, []ObjectID{starting.ID()}, nil, nil,
		Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users RENAME TO accounts"))}, TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	_, err = NewPlan(profile, baseline, history, []Decision{decision}, []Operation{operation})
	require.ErrorIs(t, err, ErrInvalidDecision)
}

func TestResolvedColumnRenameUsesAdjacentCatalogs(t *testing.T) {
	profile := sourceRepairProfile(t)
	beforeObject, err := NewCatalogObject("task", schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{{Name: "old_name", Type: schema.TextType{}}}})
	require.NoError(t, err)
	baseline, err := NewCatalog(profile, "source", []CatalogObject{beforeObject})
	require.NoError(t, err)
	afterObject, err := NewCatalogObject("task", schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{{Name: "new_name", Type: schema.TextType{}}}})
	require.NoError(t, err)
	after, err := NewCatalogLike(baseline, []CatalogObject{afterObject})
	require.NoError(t, err)
	resultDigest, err := CatalogDigest(after)
	require.NoError(t, err)
	operation, err := NewOperation("rename-column", OperationRenameColumn, nil, []ObjectID{"task"}, nil, nil,
		resultDigest, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE tasks RENAME COLUMN old_name TO new_name"))}, TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	step, err := NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	decision, err := NewDecision("rename-column-decision", DecisionRenameObject, "task", "old_name", "new_name", true, "")
	require.NoError(t, err)
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{step}, []Decision{decision}, []Operation{operation}, []BaselineObject{}, []BaselineRename{})
	require.NoError(t, err)

	wrong, err := NewDecision("wrong", DecisionRenameObject, "task", "wrong", "new_name", true, "")
	require.NoError(t, err)
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{step}, []Decision{wrong}, []Operation{operation}, []BaselineObject{}, []BaselineRename{})
	require.ErrorIs(t, err, ErrInvalidDecision)
}

func TestDecodeRejectsNonStringOperationArrayElements(t *testing.T) {
	fixture, err := os.ReadFile("testdata/v1/valid/full.json")
	require.NoError(t, err)
	for _, field := range []string{"depends_on", "objects"} {
		for _, element := range [][]byte{[]byte("null"), []byte("true"), []byte("1"), []byte(`{}`), []byte(`[]`)} {
			needle := []byte(`"` + field + `":[]`)
			replacement := []byte(`"` + field + `":[` + string(element) + `]`)
			if field == "objects" {
				needle = []byte(`"objects":["starting"]`)
				replacement = []byte(`"objects":[` + string(element) + `]`)
			}
			data := bytes.Replace(fixture, needle, replacement, 1)
			_, decodeErr := Decode(data)
			require.Error(t, decodeErr, field)
			require.True(t, errors.Is(decodeErr, ErrInvalidWire), field)
		}
	}
}

func TestReviewTypedWalkerAllDTOFields(t *testing.T) {
	object := catalogObjectWire{
		ID: "id", Kind: "table", Schema: "schema", Name: "name", Strict: true, WithoutRowID: true,
		PrimaryKeyAutoincrement: true, PrimaryKeyOnConflict: "replace", VirtualTableModule: "fts5",
		VirtualTableModuleArguments: []string{"tokenize"},
		Columns: []catalogColumnWire{{
			Name: "column", Ordinal: 3, LogicalKind: "text", Nullable: true, DefaultSQL: "default",
			GeneratedSQL: "generated", GeneratedStorage: "stored", Identity: "always", Collation: "C", Hidden: true,
			Native: &catalogNativeWire{Dialect: "postgresql", Schema: "pg_catalog", Name: "array", Kind: "array",
				Arguments: []string{"arg"}, Element: &catalogNativeWire{Dialect: "postgresql", Name: "text", Kind: "builtin"}},
			Integer: &catalogIntegerWire{Unsigned: true, DisplayWidth: catalogOptionalIntWire{Value: 11, Set: true}, ZeroFill: true},
			Text:    &catalogTextWire{Width: catalogOptionalIntWire{Value: 32, Set: true}, Fixed: true},
			Decimal: &catalogDecimalWire{Precision: 9, Scale: catalogOptionalIntWire{Value: 2, Set: true}, Unsigned: true, ZeroFill: true},
		}, {Name: "nulls"}},
		Constraints: []catalogConstraintWire{{
			Name: "constraint", Kind: "foreign_key", Columns: []string{"column"},
			Reference:     &catalogReferenceWire{Schema: "other", Object: "parent", Columns: []string{"id"}},
			ExpressionSQL: "check", Deferrable: true, InitiallyDeferred: true, OnUpdate: "cascade", OnDelete: "set_null",
			Deferrability: "deferred", Match: "full", NullsNotDistinct: true, IncludeColumns: []string{"included"},
			OnConflict: "abort", Keys: []catalogIndexPartWire{{Column: "column", ExpressionSQL: "lower(column)",
				Direction: "desc", Nulls: "first", Collation: "C", OperatorClass: "text_ops", PrefixLength: 5}},
			Temporal: true, StorageParameters: []catalogPairWire{{Key: "fillfactor", Value: "70"}}, Tablespace: "space",
			ReplicaIdentity: true, Collations: []catalogPairWire{{Key: "column", Value: "C"}}, NoInherit: true,
			NotValid: true, NotEnforced: true, DeleteSetColumns: []string{"column"},
		}},
		Indexes: []catalogIndexWire{{
			Name: "index", Unique: true, Method: "btree", KeyForm: "expressions",
			Parts: []catalogIndexPartWire{{Column: "column", ExpressionSQL: "expression", Direction: "asc",
				Nulls: "last", Collation: "C", OperatorClass: "ops", PrefixLength: 4}},
			PredicateSQL: "predicate", IncludeColumns: []string{"included"}, Invisible: true, NotValid: true,
			NullsNotDistinct: true, StorageParameters: []catalogPairWire{{Key: "fillfactor", Value: "80"}},
			Tablespace: "index_space", ReplicaIdentity: true,
		}},
		ExclusionConstraints: []catalogExclusionWire{{
			Name: "exclusion", Method: "gist", Elements: []catalogExclusionElementWire{{ExpressionSQL: "span", Operator: "&&"}},
			PredicateSQL: "predicate", Deferrability: "immediate",
		}},
	}
	tests := map[string]string{
		"/id": `"id"`, "/kind": `"table"`, "/schema": `"schema"`, "/name": `"name"`,
		"/strict": `true`, "/without_rowid": `true`, "/primary_key_autoincrement": `true`,
		"/primary_key_on_conflict": `"replace"`, "/virtual_table_module": `"fts5"`,
		"/virtual_table_module_arguments/0": `"tokenize"`, "/columns/0/name": `"column"`, "/columns/0/ordinal": `3`,
		"/columns/0/logical_kind": `"text"`, "/columns/0/nullable": `true`, "/columns/0/default_sql": `"default"`,
		"/columns/0/generated_sql": `"generated"`, "/columns/0/generated_storage": `"stored"`,
		"/columns/0/identity": `"always"`, "/columns/0/collation": `"C"`, "/columns/0/hidden": `true`,
		"/columns/0/native/dialect": `"postgresql"`, "/columns/0/native/schema": `"pg_catalog"`,
		"/columns/0/native/name": `"array"`, "/columns/0/native/kind": `"array"`,
		"/columns/0/native/arguments/0": `"arg"`, "/columns/0/native/element/name": `"text"`,
		"/columns/0/integer/unsigned": `true`, "/columns/0/integer/display_width/value": `11`,
		"/columns/0/integer/display_width/set": `true`, "/columns/0/integer/zero_fill": `true`,
		"/columns/0/text/width/value": `32`, "/columns/0/text/width/set": `true`, "/columns/0/text/fixed": `true`,
		"/columns/0/decimal/precision": `9`, "/columns/0/decimal/scale/value": `2`,
		"/columns/0/decimal/scale/set": `true`, "/columns/0/decimal/unsigned": `true`,
		"/columns/0/decimal/zero_fill": `true`, "/columns/1/native": `null`, "/columns/1/integer": `null`,
		"/columns/1/text": `null`, "/columns/1/decimal": `null`, "/constraints/0/name": `"constraint"`,
		"/constraints/0/kind": `"foreign_key"`, "/constraints/0/columns/0": `"column"`,
		"/constraints/0/reference/schema": `"other"`, "/constraints/0/reference/object": `"parent"`,
		"/constraints/0/reference/columns/0": `"id"`, "/constraints/0/expression_sql": `"check"`,
		"/constraints/0/deferrable": `true`, "/constraints/0/initially_deferred": `true`,
		"/constraints/0/on_update": `"cascade"`, "/constraints/0/on_delete": `"set_null"`,
		"/constraints/0/deferrability": `"deferred"`, "/constraints/0/match": `"full"`,
		"/constraints/0/nulls_not_distinct": `true`, "/constraints/0/include_columns/0": `"included"`,
		"/constraints/0/on_conflict": `"abort"`, "/constraints/0/keys/0/column": `"column"`,
		"/constraints/0/keys/0/expression_sql": `"lower(column)"`, "/constraints/0/keys/0/direction": `"desc"`,
		"/constraints/0/keys/0/nulls": `"first"`, "/constraints/0/keys/0/collation": `"C"`,
		"/constraints/0/keys/0/operator_class": `"text_ops"`, "/constraints/0/keys/0/prefix_length": `5`,
		"/constraints/0/temporal": `true`, "/constraints/0/storage_parameters/0/key": `"fillfactor"`,
		"/constraints/0/storage_parameters/0/value": `"70"`, "/constraints/0/tablespace": `"space"`,
		"/constraints/0/replica_identity": `true`, "/constraints/0/collations/0/key": `"column"`,
		"/constraints/0/collations/0/value": `"C"`, "/constraints/0/no_inherit": `true`,
		"/constraints/0/not_valid": `true`, "/constraints/0/not_enforced": `true`,
		"/constraints/0/delete_set_columns/0": `"column"`, "/indexes/0/name": `"index"`,
		"/indexes/0/unique": `true`, "/indexes/0/method": `"btree"`, "/indexes/0/key_form": `"expressions"`,
		"/indexes/0/parts/0/column": `"column"`, "/indexes/0/parts/0/expression_sql": `"expression"`,
		"/indexes/0/parts/0/direction": `"asc"`, "/indexes/0/parts/0/nulls": `"last"`,
		"/indexes/0/parts/0/collation": `"C"`, "/indexes/0/parts/0/operator_class": `"ops"`,
		"/indexes/0/parts/0/prefix_length": `4`, "/indexes/0/predicate_sql": `"predicate"`,
		"/indexes/0/include_columns/0": `"included"`, "/indexes/0/invisible": `true`,
		"/indexes/0/not_valid": `true`, "/indexes/0/nulls_not_distinct": `true`,
		"/indexes/0/storage_parameters/0/key": `"fillfactor"`, "/indexes/0/storage_parameters/0/value": `"80"`,
		"/indexes/0/tablespace": `"index_space"`, "/indexes/0/replica_identity": `true`,
		"/exclusion_constraints/0/name": `"exclusion"`, "/exclusion_constraints/0/method": `"gist"`,
		"/exclusion_constraints/0/elements/0/expression_sql": `"span"`, "/exclusion_constraints/0/elements/0/operator": `"&&"`,
		"/exclusion_constraints/0/predicate_sql": `"predicate"`, "/exclusion_constraints/0/deferrability": `"immediate"`,
	}
	for path, expected := range tests {
		present, value, err := walkFactPath(object, path)
		if err != nil || !present {
			t.Fatalf("%s: present=%v err=%v", path, present, err)
		}
		actual, err := canonicalJSON(sourceMustJSON(value))
		if err != nil || string(actual) != expected {
			t.Fatalf("%s: got %s, want %s, err=%v", path, actual, expected, err)
		}
	}
	if _, _, err := walkFactPath(object, "/columns/1/native/name"); !errors.Is(err, ErrInvalidFact) {
		t.Fatalf("path below null: %v", err)
	}
	for _, path := range []string{"/columns/01/name", "/columns/nope/name", "/columns/~2"} {
		if _, _, err := walkFactPath(object, path); err == nil {
			t.Fatalf("invalid path accepted: %s", path)
		}
	}
}

func sourceMustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func TestRepeatedTableRenameDecisionMatrix(t *testing.T) {
	pairs := []struct{ from, to string }{{"main.a", "main.b"}, {"main.b", "main.a"}, {"main.a", "main.b"}}
	plan := newRepeatedTablePlan(t, pairs)
	encoded, err := Encode(plan)
	require.NoError(t, err)
	_, err = Decode(encoded)
	require.NoError(t, err)
	plan.decisions[0].from = "a"
	_, err = Encode(plan)
	require.ErrorIs(t, err, ErrInvalidDecision)

	for _, test := range []struct {
		name   string
		pairs  []struct{ from, to string }
		target error
	}{
		{name: "missing repeated decision", pairs: pairs[:2], target: ErrInvalidDecision},
		{name: "extra decision", pairs: append(append([]struct{ from, to string }{}, pairs...), struct{ from, to string }{"main.a", "main.b"}), target: ErrInvalidDecision},
		{name: "unqualified source", pairs: []struct{ from, to string }{{"a", "main.b"}, {"main.b", "main.a"}, {"main.a", "main.b"}}, target: ErrInvalidDecision},
		{name: "mixed destination", pairs: []struct{ from, to string }{{"main.a", "b"}, {"main.b", "main.a"}, {"main.a", "main.b"}}, target: ErrInvalidDecision},
		{name: "wrong source", pairs: []struct{ from, to string }{{"main.x", "main.b"}, {"main.b", "main.a"}, {"main.a", "main.b"}}, target: ErrInvalidDecision},
		{name: "wrong destination", pairs: []struct{ from, to string }{{"main.a", "main.x"}, {"main.b", "main.a"}, {"main.a", "main.b"}}, target: ErrInvalidDecision},
		{name: "duplicate decision", pairs: []struct{ from, to string }{{"main.a", "main.b"}, {"main.a", "main.b"}, {"main.a", "main.b"}}, target: ErrInvalidDecision},
		{name: "wrong object", pairs: []struct{ from, to string }{{"main.a", "main.b"}, {"main.b", "main.a"}, {"main.a", "main.b"}}, target: ErrInvalidDecision},
	} {
		t.Run(test.name, func(t *testing.T) {
			operationPairs := pairs
			decisionPairs := test.pairs
			_, err := newRepeatedTablePlanWithDecisionPairs(t, operationPairs, decisionPairs, test.name == "wrong object")
			require.ErrorIs(t, err, test.target)
		})
	}

	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(encoded, &root))
	var decisions []map[string]any
	require.NoError(t, json.Unmarshal(root["decisions"], &decisions))
	decisions[0]["from"] = "a"
	root["decisions"], err = json.Marshal(decisions)
	require.NoError(t, err)
	mutated, err := json.Marshal(root)
	require.NoError(t, err)
	_, err = Decode(mutated)
	require.ErrorIs(t, err, ErrInvalidDecision)
}

func newRepeatedTablePlan(t *testing.T, pairs []struct{ from, to string }) Plan {
	plan, err := newRepeatedTablePlanWithDecisions(t, pairs, false)
	require.NoError(t, err)
	return plan
}

func newRepeatedTablePlanWithDecisions(t *testing.T, pairs []struct{ from, to string }, wrongObject bool) (Plan, error) {
	return newRepeatedTablePlanWithDecisionPairs(t, pairs, pairs, wrongObject)
}

func newRepeatedTablePlanWithDecisionPairs(t *testing.T, operationPairs, decisionPairs []struct{ from, to string }, wrongObject bool) (Plan, error) {
	t.Helper()
	profile := sourceRepairProfile(t)
	profileDigest, err := ProfileDigest(profile)
	if err != nil {
		return Plan{}, err
	}
	identity, err := NewCatalogIdentity(profile.Engine(), profileDigest, Digest{}, Digest{})
	if err != nil {
		return Plan{}, err
	}
	object, err := NewBaselineObject("object", "table", "main", "a")
	if err != nil {
		return Plan{}, err
	}
	renamed := make([]BaselineRename, len(operationPairs))
	operations := make([]Operation, len(operationPairs))
	decisions := make([]Decision, len(decisionPairs))
	for index, pair := range operationPairs {
		operationID := OperationID("rename-" + string(rune('1'+index)))
		renamed[index], err = NewBaselineRename(operationID, object.ID(), "main", strings.TrimPrefix(pair.to, "main."))
		if err != nil {
			return Plan{}, err
		}
		depends := []OperationID(nil)
		if index > 0 {
			depends = []OperationID{operations[index-1].ID()}
		}
		operations[index], err = NewOperation(operationID, OperationRenameTable, depends, []ObjectID{object.ID()}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE a RENAME TO b"))}, TransactionEngineDefault, false, nil)
		if err != nil {
			return Plan{}, err
		}
	}
	for index, pair := range decisionPairs {
		decisionObject := object.ID()
		if wrongObject && index == 0 {
			decisionObject = "other"
		}
		decisions[index], err = NewDecision(DecisionID("decision-"+string(rune('1'+index))), DecisionRenameObject,
			decisionObject, pair.from, pair.to, true, "")
		if err != nil {
			return Plan{}, err
		}
	}
	baseline, err := NewBaselineIdentity(identity, "source", []BaselineObject{object}, renamed)
	if err != nil {
		return Plan{}, err
	}
	history, err := NewHistoryIdentity("main", "schema_migrations")
	if err != nil {
		return Plan{}, err
	}
	return newPlan(profile, baseline, history, decisions, operations)
}

func TestResolvedAdjacentColumnRenameMatrix(t *testing.T) {
	profile := sourceRepairProfile(t)
	beforeObject, err := NewCatalogObject("task", schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "a", Type: schema.TextType{}}, {Name: "b", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	baseline := catalogForSourceRepair(t, profile, "source", beforeObject)
	oneObject, err := NewCatalogObject("task", schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "x", Type: schema.TextType{}}, {Name: "b", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	twoObject, err := NewCatalogObject("task", schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "x", Type: schema.TextType{}}, {Name: "y", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	one := catalogForSourceRepair(t, profile, "source", oneObject)
	two := catalogForSourceRepair(t, profile, "source", twoObject)
	first := sourceRepairRenameOperation(t, "rename-1", nil, one, "a", "x")
	second := sourceRepairRenameOperation(t, "rename-2", []OperationID{"rename-1"}, two, "b", "y")
	steps := []ResolvedCatalogStep{sourceRepairStep(t, first, one), sourceRepairStep(t, second, two)}
	decisions := []Decision{
		sourceRepairRenameDecision(t, "decision-1", "task", "a", "x"),
		sourceRepairRenameDecision(t, "decision-2", "task", "b", "y"),
	}
	_, err = NewResolvedChanges(baseline, steps, decisions, []Operation{first, second}, nil, nil)
	require.NoError(t, err)

	badBoth, err := NewCatalogObject("task", schema.TableDef{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{
		{Name: "x", Type: schema.TextType{}}, {Name: "y", Type: schema.TextType{}},
	}})
	require.NoError(t, err)
	badStep, err := NewResolvedCatalogStep(first.ID(), catalogForSourceRepair(t, profile, "source", badBoth))
	require.NoError(t, err)
	badDigest, err := CatalogDigest(badStep.Catalog())
	require.NoError(t, err)
	badOperation, err := NewOperation(first.ID(), first.Kind(), first.DependsOn(), first.Objects(), first.Preconditions(), first.Postconditions(), badDigest, first.Statements(), first.Transaction(), first.Reversible(), first.ReverseStatements())
	require.NoError(t, err)
	_, err = NewResolvedChanges(baseline, []ResolvedCatalogStep{badStep}, []Decision{decisions[0]}, []Operation{badOperation}, nil, nil)
	require.ErrorIs(t, err, ErrInvalidDecision)
	for _, test := range []struct {
		name      string
		decisions []Decision
	}{
		{name: "missing", decisions: decisions[:1]},
		{name: "duplicate", decisions: []Decision{decisions[0], decisions[0]}},
		{name: "wrong source", decisions: []Decision{sourceRepairRenameDecision(t, "wrong-source", "task", "wrong", "x"), decisions[1]}},
		{name: "wrong destination", decisions: []Decision{sourceRepairRenameDecision(t, "wrong-dest", "task", "a", "wrong"), decisions[1]}},
		{name: "extra", decisions: append(append([]Decision{}, decisions...), sourceRepairRenameDecision(t, "extra", "task", "c", "d"))},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := NewResolvedChanges(baseline, steps, test.decisions, []Operation{first, second}, nil, nil)
			require.ErrorIs(t, err, ErrInvalidDecision)
		})
	}
}

func TestTableRenameBindingFromLock(t *testing.T) {
	lock, err := os.ReadFile("testdata/external/lock.json")
	require.NoError(t, err)
	baseProfile := sourceRepairProfile(t)
	lockProfileValue := engineprofile.Profile{
		ID: baseProfile.ID(), Engine: baseProfile.Engine(),
		Version:      engineprofile.Version{Known: true, Major: 3, Minor: 45},
		Capabilities: baseProfile.Capabilities(), Limits: baseProfile.Limits(),
	}
	lockProfile, err := NewProfile(sourceRepairProfileSource{value: lockProfileValue})
	require.NoError(t, err)
	baseline, err := CatalogFromLock(lock)
	require.NoError(t, err)
	tasks, ok := baseline.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	resolvedBaseline := baseline
	mutatedLock := bytes.Replace(lock, []byte(`"name": "tasks"`), []byte(`"name": "accounts"`), 1)
	after, err := CatalogFromLock(mutatedLock)
	require.NoError(t, err)
	resultDigest, err := CatalogDigest(after)
	require.NoError(t, err)
	operation, err := NewOperation("rename", OperationRenameTable, nil, []ObjectID{tasks}, nil, nil,
		resultDigest, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE tasks RENAME TO accounts"))}, TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	decision, err := NewDecision("rename-decision", DecisionRenameObject, tasks, "main.tasks", "main.accounts", true, "")
	require.NoError(t, err)
	step, err := NewResolvedCatalogStep(operation.ID(), after)
	require.NoError(t, err)
	binding, err := NewBaselineRename(operation.ID(), tasks, "main", "accounts")
	require.NoError(t, err)
	resolved, err := NewResolvedChanges(resolvedBaseline, []ResolvedCatalogStep{step}, []Decision{decision}, []Operation{operation}, nil, []BaselineRename{binding})
	require.NoError(t, err)
	history, err := NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	_, err = FromLock(lock, lockProfile, history, resolved)
	require.NoError(t, err)
	resolved.decisions[0].from = "tasks"
	_, err = FromLock(lock, lockProfile, history, resolved)
	require.ErrorIs(t, err, ErrInvalidDecision)
}

func catalogForSourceRepair(t *testing.T, profile Profile, source string, objects ...CatalogObject) Catalog {
	t.Helper()
	catalog, err := NewCatalog(profile, source, objects)
	require.NoError(t, err)
	return catalog
}

func sourceRepairRenameOperation(t *testing.T, id OperationID, depends []OperationID, after Catalog, from, to string) Operation {
	t.Helper()
	resultDigest, err := CatalogDigest(after)
	require.NoError(t, err)
	operation, err := NewOperation(id, OperationRenameColumn, depends, []ObjectID{"task"}, nil, nil,
		resultDigest, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE tasks RENAME COLUMN " + from + " TO " + to))}, TransactionEngineDefault, false, nil)
	require.NoError(t, err)
	return operation
}

func sourceRepairRenameDecision(t *testing.T, id DecisionID, object ObjectID, from, to string) Decision {
	t.Helper()
	decision, err := NewDecision(id, DecisionRenameObject, object, from, to, true, "")
	require.NoError(t, err)
	return decision
}

func sourceRepairStep(t *testing.T, operation Operation, catalog Catalog) ResolvedCatalogStep {
	t.Helper()
	step, err := NewResolvedCatalogStep(operation.ID(), catalog)
	require.NoError(t, err)
	return step
}
