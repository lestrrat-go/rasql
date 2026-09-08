package changeplan_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"

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

	for _, test := range []struct {
		name                string
		needle, replacement string
	}{
		{name: "null custom name", needle: `"custom_name":""`, replacement: `"custom_name":null`},
		{name: "null reversible", needle: `"reversible":false`, replacement: `"reversible":null`},
		{name: "null bool argument", needle: `"kind":"bool","value":false`, replacement: `"kind":"bool","value":null`},
		{name: "wrong bool argument", needle: `"kind":"bool","value":false`, replacement: `"kind":"bool","value":"false"`},
		{name: "unknown argument kind", needle: `"kind":"bool","value":false`, replacement: `"kind":"unknown","value":false`},
		{name: "null statement array", needle: `"statements":[`, replacement: `"statements":null,`},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := bytes.Replace(valid, []byte(test.needle), []byte(test.replacement), 1)
			require.NotEqual(t, valid, mutated)
			_, decodeErr := changeplan.Decode(mutated)
			require.Error(t, decodeErr, test.name)
			require.True(t, errors.Is(decodeErr, changeplan.ErrInvalidWire), decodeErr)
		})
	}
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
	_, err = changeplan.NewOperation("empty", changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"object"}, nil, nil, nil, changeplan.TransactionForbidden, false, nil)
	require.ErrorIs(t, err, changeplan.ErrInvalidOperation)
	_, err = changeplan.NewOperation("reversible", changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"object"}, nil, nil,
		[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, true, nil)
	require.ErrorIs(t, err, changeplan.ErrInvalidOperation)
	_, err = changeplan.NewOperation("irreversible", changeplan.OperationNativeSQL, nil, []changeplan.ObjectID{"object"}, nil, nil,
		[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{stmt.New(sqltext.Text("SELECT 0"))})
	require.ErrorIs(t, err, changeplan.ErrInvalidOperation)
	_, err = changeplan.NewOperation("self", changeplan.OperationNativeSQL, []changeplan.OperationID{"self"}, []changeplan.ObjectID{"object"}, nil, nil,
		[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{})
	require.ErrorIs(t, err, changeplan.ErrInvalidOperation)
	_, err = changeplan.NewOperation("missing", changeplan.OperationNativeSQL, []changeplan.OperationID{"absent"}, []changeplan.ObjectID{"object"}, nil, nil,
		[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{})
	require.NoError(t, err)
	_, err = changeplan.NewOperation("cycle-a", changeplan.OperationNativeSQL, []changeplan.OperationID{"cycle-b"}, []changeplan.ObjectID{"object"}, nil, nil,
		[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{})
	require.NoError(t, err)
	_, err = changeplan.NewOperation("cycle-b", changeplan.OperationNativeSQL, []changeplan.OperationID{"cycle-a"}, []changeplan.ObjectID{"object"}, nil, nil,
		[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, changeplan.TransactionForbidden, false, []stmt.Statement{})
	require.NoError(t, err)
}

func TestPublicImmutabilityContract(t *testing.T) {
	plan := fullContractPlan(t)
	encoded, err := changeplan.Encode(plan)
	require.NoError(t, err)
	operations := plan.Operations()
	arguments := operations[9].Statements()[0].Args()
	arguments[6].([]byte)[0] = 'X'
	decisions := plan.Decisions()
	require.Len(t, decisions, 5)
	objects := plan.Baseline().Objects()
	objects[0] = changeplan.BaselineObject{}
	require.Equal(t, encoded, mustEncode(t, plan))
	decoded, err := changeplan.Decode(encoded)
	require.NoError(t, err)
	encoded[0] = 'X'
	require.Equal(t, encoded[0], byte('X'))
	require.Equal(t, plan.ID(), decoded.ID())
	require.Equal(t, mustEncode(t, plan), mustEncode(t, decoded))

	lock, err := os.ReadFile("testdata/external/lock.json")
	require.NoError(t, err)
	catalog, err := changeplan.CatalogFromLock(lock)
	require.NoError(t, err)
	objectID, ok := catalog.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	lock[0] = 'X'
	gotID, ok := catalog.ObjectID(schema.ObjectTable, "main", "tasks")
	require.True(t, ok)
	require.Equal(t, objectID, gotID)
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
