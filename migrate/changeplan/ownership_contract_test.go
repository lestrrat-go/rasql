package changeplan_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/migrate/changeplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func TestFullWireInvalidCorpus(t *testing.T) {
	valid, err := os.ReadFile("testdata/v1/valid/full.json")
	require.NoError(t, err)
	mutate := func(t *testing.T, key string, value []byte) []byte {
		t.Helper()
		var object map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(valid, &object))
		object[key] = value
		mutated, marshalErr := json.Marshal(object)
		require.NoError(t, marshalErr)
		return mutated
	}
	cases := []struct {
		name string
		data func(*testing.T) []byte
	}{
		{name: "missing format", data: func(t *testing.T) []byte {
			var object map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(valid, &object))
			delete(object, "format")
			value, marshalErr := json.Marshal(object)
			require.NoError(t, marshalErr)
			return value
		}},
		{name: "unknown root", data: func(t *testing.T) []byte { return mutate(t, "unknown", json.RawMessage(`true`)) }},
		{name: "null ID", data: func(t *testing.T) []byte { return mutate(t, "id", json.RawMessage(`null`)) }},
		{name: "wrong ID type", data: func(t *testing.T) []byte { return mutate(t, "id", json.RawMessage(`1`)) }},
		{name: "wrong profile object", data: func(t *testing.T) []byte { return mutate(t, "profile", json.RawMessage(`null`)) }},
		{name: "trailing JSON", data: func(t *testing.T) []byte { return append(append([]byte(nil), valid...), []byte(`{}`)...) }},
		{name: "bad format value", data: func(t *testing.T) []byte {
			return bytes.Replace(valid, []byte(`rasql.migration-plan/v1`), []byte(`rasql.migration-plan/v0`), 1)
		}},
		{name: "uppercase ID", data: func(t *testing.T) []byte {
			var object map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(valid, &object))
			var id string
			require.NoError(t, json.Unmarshal(object["id"], &id))
			encoded, marshalErr := json.Marshal(strings.ToUpper(id))
			require.NoError(t, marshalErr)
			return mutate(t, "id", encoded)
		}},
		{name: "non-hex ID", data: func(t *testing.T) []byte {
			return mutate(t, "id", json.RawMessage(`"gggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggggg"`))
		}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, decodeErr := changeplan.Decode(test.data(t))
			require.Error(t, decodeErr)
			require.True(t, errors.Is(decodeErr, changeplan.ErrInvalidWire), decodeErr)
		})
	}

	// A null whose zero value is what the author wrote re-encodes to the authored bytes, so the
	// plan ID matches and Decode returns the same plan.  want nil records that, and
	// wireDecodeRoundTrips proves the accepted plan is the fixture's plan.
	for _, test := range []struct {
		name                string
		needle, replacement string
		want                error
	}{
		{name: "null custom name", needle: `"custom_name":""`, replacement: `"custom_name":null`, want: nil},
		{name: "null reversible", needle: `"reversible":false`, replacement: `"reversible":null`, want: nil},
		{name: "null bool argument", needle: `"kind":"bool","value":false`, replacement: `"kind":"bool","value":null`, want: nil},
		{name: "wrong bool argument", needle: `"kind":"bool","value":false`, replacement: `"kind":"bool","value":"false"`, want: changeplan.ErrInvalidWire},
		{name: "unknown argument kind", needle: `"kind":"bool","value":false`, replacement: `"kind":"unknown","value":false`, want: changeplan.ErrInvalidWire},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := bytes.Replace(valid, []byte(test.needle), []byte(test.replacement), 1)
			require.NotEqual(t, valid, mutated)
			if test.want == nil {
				wireDecodeRoundTrips(t, mutated, valid)
				return
			}
			_, decodeErr := changeplan.Decode(mutated)
			require.Error(t, decodeErr, test.name)
			require.True(t, errors.Is(decodeErr, test.want), decodeErr)
		})
	}
	// A wrong JSON type is still a wire error: encoding/json reports it and Decode wraps it.  A
	// null array decodes to nil, so the constructor that needs its contents reports the domain
	// error instead, and depends_on:null re-encodes as the authored empty array.
	for _, test := range []struct {
		name  string
		field string
		value string
		want  error
	}{
		{name: "null statement array", field: "statements", value: "null", want: changeplan.ErrInvalidOperation},
		{name: "null dependency array", field: "depends_on", value: "null", want: nil},
		{name: "null object array", field: "objects", value: "null", want: changeplan.ErrInvalidOperation},
		{name: "null dependency element", field: "depends_on", value: "[null]", want: changeplan.ErrInvalidOperation},
		{name: "boolean object element", field: "objects", value: "[true]", want: changeplan.ErrInvalidWire},
		{name: "numeric dependency element", field: "depends_on", value: "[1]", want: changeplan.ErrInvalidWire},
		{name: "object object element", field: "objects", value: "[{}]", want: changeplan.ErrInvalidWire},
		{name: "nested dependency element", field: "depends_on", value: `[["create-table"]]`, want: changeplan.ErrInvalidWire},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := mutateOperationField(t, valid, test.field, json.RawMessage(test.value))
			require.True(t, json.Valid(mutated))
			if test.want == nil {
				wireDecodeRoundTrips(t, mutated, valid)
				return
			}
			_, decodeErr := changeplan.Decode(mutated)
			require.Error(t, decodeErr, test.name)
			require.True(t, errors.Is(decodeErr, test.want), decodeErr)
		})
	}

	// A wrong type and an unknown field are reported by the typed decode, so they stay
	// ErrInvalidWire.  A missing key or a null is whatever the plan ID digest makes of it: either
	// Decode rejects the file, or the file decodes to the very plan the fixture authored.
	for _, test := range wireFieldCases(t, valid) {
		t.Run(test.name, func(t *testing.T) {
			mutated := test.data()
			require.True(t, json.Valid(mutated), test.name)
			if strings.HasSuffix(test.name, " wrong type") || strings.HasSuffix(test.name, " unknown field") {
				_, decodeErr := changeplan.Decode(mutated)
				require.Error(t, decodeErr, test.name)
				require.True(t, errors.Is(decodeErr, changeplan.ErrInvalidWire), decodeErr)
				return
			}
			wireRejectsOrRoundTrips(t, mutated, valid)
		})
	}
}

// wireRejectsOrRoundTrips states what the plan ID digest in Decode guarantees.  Decode computes
// the digest of the plan it built and compares it to the file's id, so it returns either an error
// or the exact plan whose canonical bytes the author hashed - never a third, different plan.
func wireRejectsOrRoundTrips(t *testing.T, data, baseline []byte) {
	t.Helper()
	plan, err := changeplan.Decode(data)
	if err != nil {
		return
	}
	encoded, encodeErr := changeplan.Encode(plan)
	require.NoError(t, encodeErr)
	require.Equal(t, string(baseline), string(encoded), "accepted wire bytes must decode to the authored plan")
}

// wireDecodeRoundTrips is wireRejectsOrRoundTrips for an input that must be accepted.
func wireDecodeRoundTrips(t *testing.T, data, baseline []byte) {
	t.Helper()
	plan, err := changeplan.Decode(data)
	require.NoError(t, err)
	encoded, encodeErr := changeplan.Encode(plan)
	require.NoError(t, encodeErr)
	require.Equal(t, string(baseline), string(encoded))
}

// wireFieldCases walks every DTO object in the canonical fixture.  Each field
// gets a missing, null, wrong-type, and unknown-field mutation so the shape
// contract stays complete when a DTO gains a field.
func wireFieldCases(t *testing.T, valid []byte) []struct {
	name string
	data func() []byte
} {
	t.Helper()
	var root any
	require.NoError(t, json.Unmarshal(valid, &root))
	type field struct {
		path  []any
		key   string
		value any
	}
	var fields []field
	var walk func(any, []any)
	walk = func(value any, path []any) {
		switch current := value.(type) {
		case map[string]any:
			for key, child := range current {
				childPath := append(append([]any(nil), path...), key)
				fields = append(fields, field{path: path, key: key, value: child})
				walk(child, childPath)
			}
		case []any:
			for index, child := range current {
				walk(child, append(append([]any(nil), path...), index))
			}
		}
	}
	walk(root, nil)
	cases := make([]struct {
		name string
		data func() []byte
	}, 0, len(fields)*4)
	for _, current := range fields {
		current := current
		cases = append(cases,
			struct {
				name string
				data func() []byte
			}{name: "field " + fieldPath(current.path) + "." + current.key + " missing", data: func() []byte {
				copyRoot := cloneJSON(root)
				object, ok := valueAt(copyRoot, current.path).(map[string]any)
				if !ok {
					panic("wire field parent is not an object")
				}
				delete(object, current.key)
				return mustJSON(copyRoot)
			}},
			struct {
				name string
				data func() []byte
			}{name: "field " + fieldPath(current.path) + "." + current.key + " null", data: func() []byte {
				copyRoot := cloneJSON(root)
				setValue(copyRoot, append(append([]any(nil), current.path...), current.key), nil)
				return mustJSON(copyRoot)
			}},
			struct {
				name string
				data func() []byte
			}{name: "field " + fieldPath(current.path) + "." + current.key + " wrong type", data: func() []byte {
				copyRoot := cloneJSON(root)
				setValue(copyRoot, append(append([]any(nil), current.path...), current.key), wrongWireType(current.value))
				return mustJSON(copyRoot)
			}},
			struct {
				name string
				data func() []byte
			}{name: "object " + fieldPath(current.path) + " unknown field", data: func() []byte {
				copyRoot := cloneJSON(root)
				object, ok := valueAt(copyRoot, current.path).(map[string]any)
				if !ok {
					panic("wire field parent is not an object")
				}
				object["unknown_contract_field"] = true
				return mustJSON(copyRoot)
			}},
		)
	}
	return cases
}

func cloneJSON(value any) any {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	var out any
	if err := json.Unmarshal(data, &out); err != nil {
		panic(err)
	}
	return out
}

func valueAt(root any, path []any) any {
	value := root
	for _, part := range path {
		switch key := part.(type) {
		case string:
			value = value.(map[string]any)[key]
		case int:
			value = value.([]any)[key]
		}
	}
	return value
}

func setValue(root any, path []any, replacement any) {
	if len(path) == 0 {
		panic("cannot replace root")
	}
	parent := valueAt(root, path[:len(path)-1])
	switch key := path[len(path)-1].(type) {
	case string:
		parent.(map[string]any)[key] = replacement
	case int:
		parent.([]any)[key] = replacement
	}
}

func wrongWireType(value any) any {
	switch value.(type) {
	case string:
		return 1
	case bool:
		return "wrong"
	case []any:
		return map[string]any{}
	case map[string]any:
		return []any{}
	case float64:
		return "wrong"
	case nil:
		return map[string]any{}
	default:
		return "wrong"
	}
}

func fieldPath(path []any) string {
	if len(path) == 0 {
		return "$"
	}
	var out strings.Builder
	for _, part := range path {
		switch value := part.(type) {
		case string:
			out.WriteByte('.')
			out.WriteString(value)
		case int:
			out.WriteByte('[')
			out.WriteString(strconv.Itoa(value))
			out.WriteByte(']')
		}
	}
	return out.String()
}

func mustJSON(value any) []byte {
	data, err := json.Marshal(value)
	if err != nil {
		panic(err)
	}
	return data
}

func mutateOperationField(t *testing.T, input []byte, field string, value json.RawMessage) []byte {
	t.Helper()
	var root map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(input, &root))
	var operations []json.RawMessage
	require.NoError(t, json.Unmarshal(root["operations"], &operations))
	var operation map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(operations[0], &operation))
	operation[field] = value
	mutatedOperation, err := json.Marshal(operation)
	require.NoError(t, err)
	operations[0] = mutatedOperation
	root["operations"], err = json.Marshal(operations)
	require.NoError(t, err)
	mutated, err := json.Marshal(root)
	require.NoError(t, err)
	return mutated
}

func TestWireSemanticInvalidCorpus(t *testing.T) {
	valid, err := os.ReadFile("testdata/v1/valid/full.json")
	require.NoError(t, err)
	mutateText := func(needle, replacement string) []byte {
		mutated := bytes.Replace(valid, []byte(needle), []byte(replacement), 1)
		require.NotEqual(t, valid, mutated, needle)
		return mutated
	}
	for _, test := range []struct {
		name   string
		data   []byte
		target error
	}{
		{name: "unknown operation kind", data: mutateText(`"kind":"create_table"`, `"kind":"unknown"`), target: changeplan.ErrInvalidOperation},
		{name: "unknown transaction", data: mutateText(`"transaction":"required"`, `"transaction":"unknown"`), target: changeplan.ErrInvalidOperation},
		{name: "bad fact operator", data: mutateText(`"operator":"present"`, `"operator":"unknown"`), target: changeplan.ErrInvalidFact},
		{name: "bad fact canonical value", data: mutateText(`"value":"\"users\""`, `"value":"not-json"`), target: changeplan.ErrInvalidWire},
		{name: "bad base64", data: mutateText(`"value":"Ynl0ZXM="`, `"value":"%%%"`), target: changeplan.ErrInvalidWire},
		{name: "bad time", data: mutateText(`"value":"2024-01-02T03:04:05.000000006Z"`, `"value":"not-time"`), target: changeplan.ErrInvalidWire},
		{name: "bad integer encoding", data: mutateText(`"kind":"uint64","value":9`, `"kind":"uint64","value":-1`), target: changeplan.ErrInvalidWire},
		{name: "rejected decision", data: mutateText(`"kind":"accept_destructive","object":"starting","from":"","to":"","accepted":true`, `"kind":"accept_destructive","object":"starting","from":"","to":"","accepted":false`), target: changeplan.ErrInvalidDecision},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, decodeErr := changeplan.Decode(test.data)
			require.ErrorIs(t, decodeErr, test.target)
		})
	}
	for _, test := range []struct {
		name   string
		mutate func(map[string]json.RawMessage)
		target error
	}{
		{name: "duplicate decision ID", mutate: func(root map[string]json.RawMessage) {
			root["decisions"] = duplicateRawArray(t, root["decisions"])
		}, target: changeplan.ErrInvalidDecision},
		{name: "duplicate operation ID", mutate: func(root map[string]json.RawMessage) {
			root["operations"] = duplicateRawArray(t, root["operations"])
		}, target: changeplan.ErrInvalidOperation},
		{name: "duplicate baseline object ID", mutate: func(root map[string]json.RawMessage) {
			var baseline map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(root["baseline"], &baseline))
			baseline["objects"] = duplicateRawArray(t, baseline["objects"])
			root["baseline"], err = json.Marshal(baseline)
			require.NoError(t, err)
		}, target: changeplan.ErrInvalidIdentity},
		{name: "future ID corruption", mutate: func(root map[string]json.RawMessage) {
			var baseline map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(root["baseline"], &baseline))
			var objects []map[string]any
			require.NoError(t, json.Unmarshal(baseline["objects"], &objects))
			objects[1]["id"] = "future-corruption"
			baseline["objects"], err = json.Marshal(objects)
			require.NoError(t, err)
			root["baseline"], err = json.Marshal(baseline)
			require.NoError(t, err)
		}, target: changeplan.ErrInvalidIdentity},
	} {
		t.Run(test.name, func(t *testing.T) {
			var root map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(valid, &root))
			test.mutate(root)
			mutated, marshalErr := json.Marshal(root)
			require.NoError(t, marshalErr)
			_, decodeErr := changeplan.Decode(mutated)
			require.ErrorIs(t, decodeErr, test.target)
		})
	}
}

func duplicateRawArray(t *testing.T, input json.RawMessage) json.RawMessage {
	t.Helper()
	var values []json.RawMessage
	require.NoError(t, json.Unmarshal(input, &values))
	require.NotEmpty(t, values)
	values = append(values, values[0])
	out, err := json.Marshal(values)
	require.NoError(t, err)
	return out
}

func TestConstructorValidationCorpus(t *testing.T) {
	_, err := changeplan.NewFact("object", "/name", changeplan.FactOperatorAbsent, `"unexpected"`)
	require.ErrorIs(t, err, changeplan.ErrInvalidFact)
	_, err = changeplan.NewFact("object", "name", changeplan.FactOperatorEqual, `"value"`)
	require.ErrorIs(t, err, changeplan.ErrInvalidFact)
	_, err = changeplan.NewDecision("rename", changeplan.DecisionRenameObject, "object", "same", "same", true, "")
	require.ErrorIs(t, err, changeplan.ErrInvalidDecision)
	_, err = changeplan.NewDecision("approval", changeplan.DecisionAcceptDestructive, "object", "", "", false, "approved")
	require.ErrorIs(t, err, changeplan.ErrInvalidDecision)
	_, err = changeplan.NewOperation("empty", changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"object"}, nil, nil, changeplan.Digest{}, nil, changeplan.TransactionForbidden, false, nil)
	require.ErrorIs(t, err, changeplan.ErrInvalidOperation)
	_, err = changeplan.NewOperation("reversible", changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"object"}, nil, nil,
		changeplan.Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, true, nil)
	require.ErrorIs(t, err, changeplan.ErrInvalidOperation)
	_, err = changeplan.NewOperation("irreversible", changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"object"}, nil, nil,
		changeplan.Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{stmt.New(sqltext.Text("SELECT 0"))})
	require.ErrorIs(t, err, changeplan.ErrInvalidOperation)
	_, err = changeplan.NewOperation("self", changeplan.OperationNativeSQL, []changeplan.OperationID{"self"}, []changeplan.ObjectID{"object"}, nil, nil,
		changeplan.Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{})
	require.ErrorIs(t, err, changeplan.ErrInvalidOperation)
	missing, err := changeplan.NewOperation("missing", changeplan.OperationNativeSQL, []changeplan.OperationID{"absent"}, []changeplan.ObjectID{"object"}, nil, nil,
		changeplan.Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{})
	require.NoError(t, err)
	cycleA, err := changeplan.NewOperation("cycle-a", changeplan.OperationNativeSQL, []changeplan.OperationID{"cycle-b"}, []changeplan.ObjectID{"object"}, nil, nil,
		changeplan.Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{})
	require.NoError(t, err)
	cycleB, err := changeplan.NewOperation("cycle-b", changeplan.OperationNativeSQL, []changeplan.OperationID{"cycle-a"}, []changeplan.ObjectID{"object"}, nil, nil,
		changeplan.Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{})
	require.NoError(t, err)
	profile := testProfile(t)
	profileDigest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalogIdentity(profile.Engine(), profileDigest, changeplan.Digest{}, changeplan.Digest{})
	require.NoError(t, err)
	baselineObject, err := changeplan.NewBaselineObject("object", "table", "main", "object")
	require.NoError(t, err)
	baseline, err := changeplan.NewBaselineIdentity(catalog, "constructor-source", []changeplan.BaselineObject{baselineObject}, nil)
	require.NoError(t, err)
	history, err := changeplan.NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	nativeDecision := mustDecision(t, "native", changeplan.DecisionAcceptNativeSQL, "object", "", "", "approved")
	_, err = changeplan.NewPlan(profile, baseline, history, []changeplan.Decision{nativeDecision}, []changeplan.Operation{missing})
	require.ErrorIs(t, err, changeplan.ErrInvalidOperation)
	_, err = changeplan.NewPlan(profile, baseline, history, []changeplan.Decision{nativeDecision}, []changeplan.Operation{cycleA, cycleB})
	require.ErrorIs(t, err, changeplan.ErrDependencyCycle)
}

func TestPublicImmutabilityContract(t *testing.T) {
	plan := fullContractPlan(t)
	encoded, err := changeplan.Encode(plan)
	require.NoError(t, err)
	operations := plan.Operations()
	arguments := operations[9].Statements()[0].Args()
	arguments[6].([]byte)[0] = 'X'
	decisions := plan.Decisions()
	require.Len(t, decisions, 6)
	objects := plan.Baseline().Objects()
	objects[0] = changeplan.BaselineObject{}
	require.Equal(t, encoded, mustEncode(t, plan))
	decoded, err := changeplan.Decode(encoded)
	require.NoError(t, err)
	encoded[0] = 'X'
	require.Equal(t, encoded[0], byte('X'))
	require.Equal(t, plan.ID(), decoded.ID())
	require.Equal(t, mustEncode(t, plan), mustEncode(t, decoded))

	physical, diagnostics := compilerir.PhysicalFromTableDefs(
		compilerir.EngineIdentity{Dialect: "sqlite", Version: "3.35", Profile: "sqlite-3.35"},
		[]schema.TableDef{{Schema: "main", Name: "tasks", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}})
	require.Empty(t, diagnostics)
	assigned, diagnostics := compilerir.AssignObjectIDs(physical, compilerir.IdentityInput{SourceIdentity: "ownership-contract"})
	require.Empty(t, diagnostics)
	catalog, err := changeplan.NewCatalogFromPhysical(assigned, "ownership-contract")
	require.NoError(t, err)
	objectID, ok := catalog.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	assigned.Objects[0].ID = "mutated-after-call"
	gotID, ok := catalog.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	require.Equal(t, objectID, gotID)
}

type mutableContractProfileSource struct{ value engineprofile.Profile }

func (s *mutableContractProfileSource) ID() string                        { return s.value.ID }
func (s *mutableContractProfileSource) Engine() changeplan.EngineID       { return s.value.Engine }
func (s *mutableContractProfileSource) Version() changeplan.EngineVersion { return s.value.Version }
func (s *mutableContractProfileSource) Capabilities() changeplan.EngineCapabilities {
	return s.value.Capabilities
}
func (s *mutableContractProfileSource) Limits() changeplan.EngineLimits { return s.value.Limits }

func TestPublicDeepCopyContract(t *testing.T) {
	profile := testProfile(t)
	profileSource := &mutableContractProfileSource{value: engineprofile.Profile{
		ID: profile.ID(), Engine: profile.Engine(), Version: profile.Version(),
		Capabilities: profile.Capabilities(), Limits: profile.Limits(),
	}}
	snapshot, err := changeplan.NewProfile(profileSource)
	require.NoError(t, err)
	profileSource.value.ID = "changed"
	profileSource.value.Limits.MaxBindParameters = 1
	require.Equal(t, "sqlite-3.35", snapshot.ID())
	require.Equal(t, profile.Limits(), snapshot.Limits())

	definition := schema.TableDef{Schema: "main", Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
	catalogObject, err := changeplan.NewCatalogObject("object", definition)
	require.NoError(t, err)
	definition.Columns[0].Name = "changed"
	returnedDefinition := catalogObject.Definition()
	returnedDefinition.Columns[0].Name = "changed-again"
	require.Equal(t, "id", catalogObject.Definition().Columns[0].Name)

	depends := []changeplan.OperationID{"previous"}
	objects := []changeplan.ObjectID{"object"}
	fact, err := changeplan.NewFact("object", "/name", changeplan.FactOperatorEqual, `"users"`)
	require.NoError(t, err)
	preconditions := []changeplan.Fact{fact}
	argument := []byte("payload")
	statements := []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), argument)}
	operation, err := changeplan.NewOperation("operation", changeplan.OperationNativeSQL, depends, objects, preconditions, nil,
		changeplan.Digest{1}, statements, changeplan.TransactionForbidden, false, nil)
	require.NoError(t, err)
	depends[0] = "changed"
	objects[0] = "changed"
	preconditions[0] = changeplan.Fact{}
	argument[0] = 'X'
	statements[0].Args()[0].([]byte)[0] = 'Y'
	require.Equal(t, []changeplan.OperationID{"previous"}, operation.DependsOn())
	require.Equal(t, []changeplan.ObjectID{"object"}, operation.Objects())
	require.Equal(t, []changeplan.Fact{fact}, operation.Preconditions())
	require.Equal(t, []byte("payload"), operation.Statements()[0].Args()[0].([]byte))

	baselineObjects := []changeplan.BaselineObject{mustBaselineObject(t, "object", "main", "users")}
	profileDigest, err := changeplan.ProfileDigest(profile)
	require.NoError(t, err)
	catalogIdentity, err := changeplan.NewCatalogIdentity(profile.Engine(), profileDigest, changeplan.Digest{}, changeplan.Digest{})
	require.NoError(t, err)
	baseline, err := changeplan.NewBaselineIdentity(catalogIdentity, "copy-source", baselineObjects, nil)
	require.NoError(t, err)
	baselineObjects[0] = changeplan.BaselineObject{}
	objectsCopy := baseline.Objects()
	objectsCopy[0] = changeplan.BaselineObject{}
	require.Equal(t, changeplan.ObjectID("object"), baseline.Objects()[0].ID())
}

func mustBaselineObject(t *testing.T, id, schemaName, name string) changeplan.BaselineObject {
	t.Helper()
	object, err := changeplan.NewBaselineObject(changeplan.ObjectID(id), "table", schemaName, name)
	require.NoError(t, err)
	return object
}

func TestExclusionCatalogContract(t *testing.T) {
	definition := schema.TableDef{
		Schema: "public", Name: "orders", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		ExclusionConstraints: []schema.ExclusionDef{{
			Name: "orders_excl", Method: "gist",
			Elements: []schema.ExclusionElementDef{{Expression: "id", Operator: "="}},
		}},
	}
	object, err := changeplan.NewCatalogObject("orders-id", definition)
	require.NoError(t, err)
	catalog, err := changeplan.NewCatalog(testProfile(t), "exclusion-source", []changeplan.CatalogObject{object})
	require.NoError(t, err)
	wantDigest, err := changeplan.ParseDigest("2dba21deab986af9b26bb1beb8f073d216888add5afd863e786570ee6a69cdca")
	require.NoError(t, err)
	gotDigest, err := changeplan.CatalogDigest(catalog)
	require.NoError(t, err)
	require.Equal(t, wantDigest, gotDigest)
	expression, err := changeplan.NewFact("orders-id", "/exclusion_constraints/0/elements/0/expression_sql", changeplan.FactOperatorEqual, `"id"`)
	require.NoError(t, err)
	operator, err := changeplan.NewFact("orders-id", "/exclusion_constraints/0/elements/0/operator", changeplan.FactOperatorEqual, `"="`)
	require.NoError(t, err)
	require.NoError(t, changeplan.EvaluateFact(catalog, expression))
	require.NoError(t, changeplan.EvaluateFact(catalog, operator))
}

func mustEncode(t *testing.T, plan changeplan.Plan) []byte {
	t.Helper()
	encoded, err := changeplan.Encode(plan)
	require.NoError(t, err)
	return encoded
}
