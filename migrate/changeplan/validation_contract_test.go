package changeplan

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"io"
	"math"
	"os"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

func validationContractValid(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile("testdata/v1/valid/full.json")
	require.NoError(t, err)
	return data
}

func validationContractJSON(t *testing.T, data []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var value any
	require.NoError(t, decoder.Decode(&value))
	var extra any
	require.ErrorIs(t, decoder.Decode(&extra), io.EOF)
	return value
}

func validationContractBytes(t *testing.T, value any) []byte {
	t.Helper()
	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}

func validationContractClone(t *testing.T, value any) any {
	t.Helper()
	return validationContractJSON(t, validationContractBytes(t, value))
}

func validationContractNumberMutation(t *testing.T, data []byte, token json.Number, path ...any) []byte {
	t.Helper()
	root := validationContractClone(t, validationContractJSON(t, data))
	validationContractSet(root, token, path...)
	encoded := validationContractBytes(t, root)
	require.Contains(t, string(encoded), token.String())
	return validationContractRehash(t, encoded)
}

func validationContractRequireValidNumericRoundTrip(t *testing.T) {
	t.Helper()
	for _, row := range []struct {
		name  string
		path  []any
		value json.Number
		index int
		want  any
	}{
		{name: "int64 minimum", path: []any{"operations", 9, "statements", 0, "args", 2, "value"}, value: json.Number("-9223372036854775808"), index: 2, want: int64(-9223372036854775808)},
		{name: "int64 maximum", path: []any{"operations", 9, "statements", 0, "args", 2, "value"}, value: json.Number("9223372036854775807"), index: 2, want: int64(9223372036854775807)},
		{name: "uint64 maximum", path: []any{"operations", 9, "statements", 0, "args", 3, "value"}, value: json.Number("18446744073709551615"), index: 3, want: uint64(18446744073709551615)},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			data := validationContractNumberMutation(t, validationContractValid(t), row.value, row.path...)
			plan, err := Decode(data)
			require.NoError(t, err)
			require.Equal(t, row.want, plan.Operations()[9].Statements()[0].Args()[row.index])
		})
	}
}

func validationContractAt(root any, path ...any) any {
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

func validationContractSet(root any, value any, path ...any) {
	parent := validationContractAt(root, path[:len(path)-1]...)
	switch key := path[len(path)-1].(type) {
	case string:
		parent.(map[string]any)[key] = value
	case int:
		parent.([]any)[key] = value
	}
}

func validationContractRehash(t *testing.T, data []byte) []byte {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var wire planWire
	require.NoError(t, decoder.Decode(&wire))
	wire.ID = ""
	withoutID, err := marshalNoHTML(wire)
	require.NoError(t, err)
	wire.ID = digestHex(Digest(sha256.Sum256(withoutID)))
	encoded, err := marshalNoHTML(wire)
	require.NoError(t, err)
	return append(encoded, '\n')
}

func validationContractMutation(t *testing.T, data []byte, value any, path ...any) []byte {
	t.Helper()
	root := validationContractClone(t, validationContractJSON(t, data))
	validationContractSet(root, value, path...)
	return validationContractRehash(t, validationContractBytes(t, root))
}

func validationContractDecodeError(t *testing.T, data []byte, target error, contains string) {
	t.Helper()
	_, err := Decode(data)
	require.Error(t, err)
	require.ErrorIs(t, err, target)
	if contains != "" {
		require.ErrorContains(t, err, contains)
	}
}

func validationContractWrongType(value any) any {
	switch value.(type) {
	case string:
		return 1
	case json.Number:
		return "wrong"
	case bool:
		return "wrong"
	case []any:
		return map[string]any{}
	case map[string]any:
		return []any{}
	default:
		return "wrong"
	}
}

func validationContractPath(path []any) string {
	var out strings.Builder
	out.WriteString("$")
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

type validationContractField struct {
	path      []any
	value     any
	allowNull bool
}

func validationContractWireFields(t *testing.T, root any) []validationContractField {
	t.Helper()
	fields := make([]validationContractField, 0, 320)
	add := func(path ...any) {
		fields = append(fields, validationContractField{path: path, value: validationContractAt(root, path...)})
	}
	for _, key := range []string{"format", "id"} {
		add(key)
	}
	for _, key := range []string{"engine", "custom_name", "version_known", "max_bind_parameters"} {
		add("profile", key)
	}
	for _, key := range []string{"major", "minor", "patch"} {
		add("profile", "version", key)
	}
	for _, key := range []string{
		"returning", "upsert", "conflict_target", "default_values", "empty_insert", "default_values_upsert",
		"subquery_limit", "write_subquery_target", "partial_index", "aggregate_filter", "qualified_reference",
		"qualified_index_target", "qualified_index_name", "match_operator", "select_for_update", "select_for_share",
		"select_lock_of", "select_lock_no_wait", "select_lock_skip_locked", "upsert_conflict_where", "upsert_update_where",
		"window_functions", "lateral_joins", "savepoints", "transactional_ddl", "explicit_null_ordering",
		"tuple_comparison", "per_parent_limit", "update_default",
	} {
		add("profile", "capabilities", key)
	}
	for _, key := range []string{"engine", "profile_digest", "catalog_digest", "source_digest", "source_identity"} {
		add("baseline", key)
	}
	for _, key := range []string{"schema", "table"} {
		add("history", key)
	}
	for _, index := range []int{0, 1} {
		for _, key := range []string{"id", "kind", "schema", "name", "introduced_by"} {
			add("baseline", "objects", index, key)
		}
		for _, key := range []string{"operation", "object", "to_schema", "to_name"} {
			add("baseline", "renames", index, key)
		}
	}
	for _, index := range []int{0, 1, 2, 3, 4, 5} {
		for _, key := range []string{"id", "kind", "object", "from", "to", "accepted", "reason"} {
			add("decisions", index, key)
		}
	}
	for _, index := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13} {
		for _, key := range []string{"id", "kind", "depends_on", "objects", "preconditions", "postconditions", "result_digest", "statements", "transaction", "reversible", "reverse_statements"} {
			add("operations", index, key)
		}
		add("operations", index, "statements", 0, "sql")
		add("operations", index, "statements", 0, "args")
	}
	for _, row := range []struct {
		path []any
	}{
		{path: []any{"operations", 1, "preconditions", 0}},
		{path: []any{"operations", 9, "postconditions", 0}},
	} {
		for _, key := range []string{"object", "path", "operator", "value"} {
			path := append(append([]any(nil), row.path...), key)
			add(path...)
		}
	}
	for _, row := range []struct {
		path []any
	}{
		{path: []any{"operations", 0, "statements", 0}},
		{path: []any{"operations", 1, "statements", 0}},
		{path: []any{"operations", 2, "statements", 0}},
		{path: []any{"operations", 3, "statements", 0}},
		{path: []any{"operations", 4, "statements", 0}},
		{path: []any{"operations", 5, "statements", 0}},
		{path: []any{"operations", 6, "statements", 0}},
		{path: []any{"operations", 7, "statements", 0}},
		{path: []any{"operations", 8, "statements", 0}},
		{path: []any{"operations", 9, "statements", 0}},
		{path: []any{"operations", 10, "statements", 0}},
		{path: []any{"operations", 11, "statements", 0}},
		{path: []any{"operations", 12, "statements", 0}},
		{path: []any{"operations", 13, "statements", 0}},
		{path: []any{"operations", 10, "reverse_statements", 0}},
	} {
		for _, key := range []string{"sql", "args"} {
			add(append(append([]any(nil), row.path...), key)...)
		}
	}
	for _, row := range []struct {
		path      []any
		allowNull bool
	}{
		{path: []any{"operations", 9, "statements", 0, "args", 0}, allowNull: true},
		{path: []any{"operations", 9, "statements", 0, "args", 1}},
		{path: []any{"operations", 9, "statements", 0, "args", 2}},
		{path: []any{"operations", 9, "statements", 0, "args", 3}},
		{path: []any{"operations", 9, "statements", 0, "args", 4}},
		{path: []any{"operations", 9, "statements", 0, "args", 5}},
		{path: []any{"operations", 9, "statements", 0, "args", 6}},
		{path: []any{"operations", 9, "statements", 0, "args", 7}},
		{path: []any{"operations", 10, "reverse_statements", 0, "args", 0}, allowNull: true},
		{path: []any{"operations", 10, "reverse_statements", 0, "args", 1}},
		{path: []any{"operations", 10, "reverse_statements", 0, "args", 2}},
		{path: []any{"operations", 10, "reverse_statements", 0, "args", 3}},
		{path: []any{"operations", 10, "reverse_statements", 0, "args", 4}},
		{path: []any{"operations", 10, "reverse_statements", 0, "args", 5}},
		{path: []any{"operations", 10, "reverse_statements", 0, "args", 6}},
		{path: []any{"operations", 10, "reverse_statements", 0, "args", 7}},
	} {
		add("operations", row.path[1].(int), row.path[2].(string), row.path[3].(int), row.path[4].(string), row.path[5].(int), "kind")
		valuePath := append(append([]any(nil), row.path...), "value")
		fields = append(fields, validationContractField{path: valuePath, value: validationContractAt(root, valuePath...), allowNull: row.allowNull})
	}
	sort.Slice(fields, func(i, j int) bool {
		return validationContractPath(fields[i].path) < validationContractPath(fields[j].path)
	})
	return fields
}

func validationContractReverseArguments(t *testing.T, data []byte) []byte {
	t.Helper()
	root := validationContractJSON(t, data).(map[string]any)
	forward := validationContractAt(root, "operations", 9, "statements", 0, "args")
	validationContractSet(root, validationContractClone(t, forward), "operations", 10, "reverse_statements", 0, "args")
	data = validationContractBytes(t, root)
	data = validationContractRehash(t, data)
	_, err := Decode(data)
	require.NoError(t, err)
	return data
}

func validationContractUnknownField(t *testing.T, data []byte, path ...any) []byte {
	t.Helper()
	root := validationContractClone(t, validationContractJSON(t, data))
	object := validationContractAt(root, path...).(map[string]any)
	object["unexpected"] = true
	return validationContractBytes(t, root)
}

func TestValidationContractWireShape(t *testing.T) {
	valid := validationContractValid(t)
	for _, test := range []struct {
		name string
		data []byte
	}{
		{"malformed json", []byte(`{"format"`)},
		{"null root", []byte(`null`)},
		{"scalar root", []byte(`1`)},
		{"array root", []byte(`[]`)},
		{"trailing json", append(append([]byte(nil), valid...), []byte(`{}`)...)},
	} {
		t.Run(test.name, func(t *testing.T) { validationContractDecodeError(t, test.data, ErrInvalidWire, "") })
	}

	validationContractRequireValidNumericRoundTrip(t)
	reverseValid := validationContractReverseArguments(t, valid)
	root := validationContractJSON(t, reverseValid).(map[string]any)

	for _, field := range validationContractWireFields(t, root) {
		field := field
		label := validationContractPath(field.path)
		t.Run("field "+label, func(t *testing.T) {
			for _, test := range []struct {
				name   string
				mutate func(any)
			}{
				{"missing", func(copyRoot any) {
					parent := validationContractAt(copyRoot, field.path[:len(field.path)-1]...).(map[string]any)
					delete(parent, field.path[len(field.path)-1].(string))
				}},
				{"null", func(copyRoot any) { validationContractSet(copyRoot, nil, field.path...) }},
				{"wrong type", func(copyRoot any) {
					validationContractSet(copyRoot, validationContractWrongType(field.value), field.path...)
				}},
			} {
				test := test
				t.Run(test.name, func(t *testing.T) {
					copyRoot := validationContractClone(t, root)
					test.mutate(copyRoot)
					data := validationContractBytes(t, copyRoot)
					if test.name == "null" && field.allowNull {
						_, err := Decode(data)
						require.NoError(t, err)
						return
					}
					validationContractDecodeError(t, data, ErrInvalidWire, "")
				})
			}
		})
	}

	for _, test := range []struct {
		name string
		path []any
	}{
		{"decisions", []any{"decisions"}},
		{"operations", []any{"operations"}},
		{"baseline.objects", []any{"baseline", "objects"}},
		{"baseline.renames", []any{"baseline", "renames"}},
		{"operations[1].depends_on", []any{"operations", 1, "depends_on"}},
		{"operations[0].objects", []any{"operations", 0, "objects"}},
		{"operations[1].preconditions", []any{"operations", 1, "preconditions"}},
		{"operations[9].postconditions", []any{"operations", 9, "postconditions"}},
		{"operations[0].statements", []any{"operations", 0, "statements"}},
		{"operations[10].reverse_statements", []any{"operations", 10, "reverse_statements"}},
		{"operations[9].statements[0].args", []any{"operations", 9, "statements", 0, "args"}},
	} {
		test := test
		for _, shape := range []struct {
			name  string
			value any
		}{
			{"missing", nil},
			{"null", nil},
			{"object", map[string]any{}},
			{"scalar", true},
		} {
			shape := shape
			t.Run(test.name+" "+shape.name, func(t *testing.T) {
				copyRoot := validationContractClone(t, root)
				if shape.name == "missing" {
					parent := validationContractAt(copyRoot, test.path[:len(test.path)-1]...).(map[string]any)
					delete(parent, test.path[len(test.path)-1].(string))
				} else {
					validationContractSet(copyRoot, shape.value, test.path...)
				}
				validationContractDecodeError(t, validationContractBytes(t, copyRoot), ErrInvalidWire, "")
			})
		}
	}

	for _, test := range []struct {
		name string
		path []any
	}{
		{"decision", []any{"decisions", 0}},
		{"operation", []any{"operations", 0}},
		{"baseline object starting", []any{"baseline", "objects", 0}},
		{"baseline object future", []any{"baseline", "objects", 1}},
		{"baseline rename starting", []any{"baseline", "renames", 0}},
		{"baseline rename future", []any{"baseline", "renames", 1}},
		{"precondition", []any{"operations", 1, "preconditions", 0}},
		{"postcondition", []any{"operations", 9, "postconditions", 0}},
		{"statement", []any{"operations", 0, "statements", 0}},
		{"reverse statement", []any{"operations", 10, "reverse_statements", 0}},
	} {
		test := test
		for _, element := range []struct {
			name  string
			value any
		}{
			{"null", nil},
			{"boolean", true},
			{"number", json.Number("1")},
			{"string", "wrong"},
			{"nested array", []any{}},
		} {
			element := element
			t.Run(test.name+" element "+element.name, func(t *testing.T) {
				copyRoot := validationContractClone(t, root)
				validationContractSet(copyRoot, element.value, test.path...)
				validationContractDecodeError(t, validationContractBytes(t, copyRoot), ErrInvalidWire, "")
			})
		}
	}

	for _, test := range []struct {
		name string
		path []any
		key  string
	}{
		{"precondition", []any{"operations", 1, "preconditions", 0}, "preconditions"},
		{"postcondition", []any{"operations", 9, "postconditions", 0}, "postconditions"},
		{"forward argument", []any{"operations", 9, "statements", 0, "args", 0}, "args"},
		{"reverse argument", []any{"operations", 10, "reverse_statements", 0, "args", 0}, "args"},
	} {
		test := test
		for _, element := range []struct {
			name  string
			value any
		}{
			{"null", nil},
			{"boolean", true},
			{"number", json.Number("1")},
			{"string", "wrong"},
			{"nested array", []any{}},
		} {
			element := element
			t.Run(test.name+" element "+element.name, func(t *testing.T) {
				copyRoot := validationContractClone(t, root)
				validationContractSet(copyRoot, element.value, test.path...)
				validationContractDecodeError(t, validationContractBytes(t, copyRoot), ErrInvalidWire, "")
			})
		}
	}

	for _, test := range []struct {
		name string
		path []any
		key  string
	}{
		{"depends_on", []any{"operations", 1, "depends_on", 0}, "depends_on"},
		{"objects", []any{"operations", 0, "objects", 0}, "objects"},
	} {
		test := test
		for _, element := range []struct {
			name  string
			value any
		}{
			{"null", nil},
			{"boolean", true},
			{"number", json.Number("1")},
			{"object", map[string]any{}},
			{"nested array", []any{}},
		} {
			element := element
			t.Run(test.name+" element "+element.name, func(t *testing.T) {
				copyRoot := validationContractClone(t, root)
				validationContractSet(copyRoot, element.value, test.path...)
				data := validationContractBytes(t, copyRoot)
				validationContractDecodeError(t, data, ErrInvalidWire, test.key+" element must be a string")
			})
		}
	}

	unknownLocations := []struct {
		name string
		path []any
	}{
		{"root", nil},
		{"profile", []any{"profile"}},
		{"version", []any{"profile", "version"}},
		{"capabilities", []any{"profile", "capabilities"}},
		{"baseline", []any{"baseline"}},
		{"history", []any{"history"}},
		{"baseline object starting", []any{"baseline", "objects", 0}},
		{"baseline object future", []any{"baseline", "objects", 1}},
		{"baseline rename starting", []any{"baseline", "renames", 0}},
		{"baseline rename future", []any{"baseline", "renames", 1}},
	}
	for _, index := range []int{0, 1, 2, 3, 4, 5} {
		unknownLocations = append(unknownLocations, struct {
			name string
			path []any
		}{"decision " + strconv.Itoa(index), []any{"decisions", index}})
	}
	for _, index := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13} {
		unknownLocations = append(unknownLocations, struct {
			name string
			path []any
		}{"operation " + strconv.Itoa(index), []any{"operations", index}})
		unknownLocations = append(unknownLocations, struct {
			name string
			path []any
		}{"statement " + strconv.Itoa(index), []any{"operations", index, "statements", 0}})
	}
	unknownLocations = append(unknownLocations,
		struct {
			name string
			path []any
		}{"reverse statement", []any{"operations", 10, "reverse_statements", 0}},
		struct {
			name string
			path []any
		}{"precondition", []any{"operations", 1, "preconditions", 0}},
		struct {
			name string
			path []any
		}{"postcondition", []any{"operations", 9, "postconditions", 0}},
	)
	for _, location := range unknownLocations {
		location := location
		t.Run(location.name+" unexpected field", func(t *testing.T) {
			data := validationContractUnknownField(t, reverseValid, location.path...)
			validationContractDecodeError(t, data, ErrInvalidWire, "unknown field")
		})
	}

	for _, row := range []struct {
		name string
		path []any
	}{
		{"forward null", []any{"operations", 9, "statements", 0, "args", 0}},
		{"forward bool", []any{"operations", 9, "statements", 0, "args", 1}},
		{"forward int64", []any{"operations", 9, "statements", 0, "args", 2}},
		{"forward uint64", []any{"operations", 9, "statements", 0, "args", 3}},
		{"forward float64", []any{"operations", 9, "statements", 0, "args", 4}},
		{"forward string", []any{"operations", 9, "statements", 0, "args", 5}},
		{"forward bytes_base64", []any{"operations", 9, "statements", 0, "args", 6}},
		{"forward time_rfc3339nano", []any{"operations", 9, "statements", 0, "args", 7}},
		{"reverse null", []any{"operations", 10, "reverse_statements", 0, "args", 0}},
		{"reverse bool", []any{"operations", 10, "reverse_statements", 0, "args", 1}},
		{"reverse int64", []any{"operations", 10, "reverse_statements", 0, "args", 2}},
		{"reverse uint64", []any{"operations", 10, "reverse_statements", 0, "args", 3}},
		{"reverse float64", []any{"operations", 10, "reverse_statements", 0, "args", 4}},
		{"reverse string", []any{"operations", 10, "reverse_statements", 0, "args", 5}},
		{"reverse bytes_base64", []any{"operations", 10, "reverse_statements", 0, "args", 6}},
		{"reverse time_rfc3339nano", []any{"operations", 10, "reverse_statements", 0, "args", 7}},
	} {
		row := row
		t.Run(row.name+" unexpected field", func(t *testing.T) {
			data := validationContractUnknownField(t, reverseValid, row.path...)
			validationContractDecodeError(t, data, ErrInvalidWire, "unknown field")
		})
	}
}

func TestValidationContractScalarGrammar(t *testing.T) {
	valid := validationContractValid(t)
	for _, test := range []struct {
		name   string
		path   []any
		value  any
		target error
		rehash bool
	}{
		{"empty format", []any{"format"}, "", ErrInvalidWire, true},
		{"wrong format", []any{"format"}, "rasql.migration-plan/v0", ErrInvalidWire, true},
		{"case changed format", []any{"format"}, "RASQL.MIGRATION-PLAN/V1", ErrInvalidWire, true},
		{"profile engine empty", []any{"profile", "engine"}, "", ErrInvalidWire, true},
		{"profile engine uppercase", []any{"profile", "engine"}, "SQLITE", ErrInvalidWire, true},
		{"profile engine unknown", []any{"profile", "engine"}, "unknown", ErrInvalidWire, true},
		{"baseline engine unknown", []any{"baseline", "engine"}, "unknown", ErrInvalidWire, true},
		{"operation kind empty", []any{"operations", 0, "kind"}, "", ErrInvalidOperation, true},
		{"operation kind uppercase", []any{"operations", 0, "kind"}, "CREATE_TABLE", ErrInvalidOperation, true},
		{"operation kind unknown", []any{"operations", 0, "kind"}, "unknown", ErrInvalidOperation, true},
		{"transaction empty", []any{"operations", 0, "transaction"}, "", ErrInvalidOperation, true},
		{"transaction uppercase", []any{"operations", 0, "transaction"}, "REQUIRED", ErrInvalidOperation, true},
		{"transaction unknown", []any{"operations", 0, "transaction"}, "unknown", ErrInvalidOperation, true},
		{"decision kind empty", []any{"decisions", 0, "kind"}, "", ErrInvalidDecision, true},
		{"decision kind uppercase", []any{"decisions", 0, "kind"}, "RENAME_OBJECT", ErrInvalidDecision, true},
		{"decision kind unknown", []any{"decisions", 0, "kind"}, "unknown", ErrInvalidDecision, true},
		{"fact operator empty", []any{"operations", 1, "preconditions", 0, "operator"}, "", ErrInvalidFact, true},
		{"fact operator uppercase", []any{"operations", 1, "preconditions", 0, "operator"}, "PRESENT", ErrInvalidFact, true},
		{"fact operator unknown", []any{"operations", 1, "preconditions", 0, "operator"}, "unknown", ErrInvalidFact, true},
		{"baseline engine differs from profile", []any{"baseline", "engine"}, "mysql", ErrInvalidWire, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			data := validationContractMutation(t, valid, test.value, test.path...)
			validationContractDecodeError(t, data, test.target, "")
		})
	}
	for _, field := range []string{"profile_digest", "catalog_digest", "source_digest"} {
		for _, test := range []struct{ name, value string }{{"short", "abcd"}, {"long", strings.Repeat("a", 65)}, {"nonhex", strings.Repeat("z", 64)}, {"uppercase", strings.Repeat("A", 64)}} {
			t.Run(field+" "+test.name, func(t *testing.T) {
				copyRoot := validationContractJSON(t, valid)
				validationContractSet(copyRoot, test.value, "baseline", field)
				validationContractDecodeError(t, validationContractBytes(t, copyRoot), ErrInvalidWire, "")
			})
		}
	}
	for _, field := range []string{"returning", "upsert", "per_parent_limit", "update_default"} {
		for _, value := range []string{"unknown", "UNKNOWN"} {
			t.Run(field+" enum "+value, func(t *testing.T) {
				validationContractDecodeError(t, validationContractMutation(t, valid, value, "profile", "capabilities", field), ErrInvalidWire, "")
			})
		}
	}
	for _, row := range []struct {
		name   string
		value  any
		target error
	}{
		{"zero bind limit", 0, engineprofile.ErrInvalidProfile}, {"negative bind limit", -1, engineprofile.ErrInvalidProfile},
		{"bind limit integer overflow", 9223372036854775807.0, ErrInvalidWire},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			root := validationContractJSON(t, valid)
			validationContractSet(root, row.value, "profile", "max_bind_parameters")
			data := validationContractBytes(t, root)
			if row.name != "bind limit integer overflow" {
				data = validationContractRehash(t, data)
			}
			validationContractDecodeError(t, data, row.target, "")
		})
	}
	for _, field := range []string{"major", "minor", "patch"} {
		for _, value := range []int{-1, 65536} {
			t.Run(field+" overflow", func(t *testing.T) {
				copyRoot := validationContractJSON(t, valid)
				validationContractSet(copyRoot, value, "profile", "version", field)
				validationContractDecodeError(t, validationContractBytes(t, copyRoot), ErrInvalidWire, "")
			})
		}
	}
}

func TestValidationContractProfileSemantics(t *testing.T) {
	valid := validationContractValid(t)
	rows := []struct {
		name   string
		mutate func(map[string]any)
		target error
	}{
		{"builtin unknown version", func(root map[string]any) {
			root["profile"].(map[string]any)["version_known"] = false
		}, engineprofile.ErrInvalidProfile},
		{"unknown version has numbers", func(root map[string]any) {
			profile := root["profile"].(map[string]any)
			profile["engine"], profile["custom_name"], profile["version_known"] = "custom", "custom", false
			profile["version"].(map[string]any)["major"] = 1
		}, engineprofile.ErrInvalidProfile},
		{"known version zero major", func(root map[string]any) {
			profile := root["profile"].(map[string]any)
			profile["engine"], profile["custom_name"] = "custom", "custom"
			profile["version_known"] = true
			profile["version"].(map[string]any)["major"] = 0
		}, engineprofile.ErrInvalidProfile},
		{"builtin custom name", func(root map[string]any) { root["profile"].(map[string]any)["custom_name"] = "extra" }, engineprofile.ErrInvalidProfile},
		{"custom missing name", func(root map[string]any) { root["profile"].(map[string]any)["engine"] = "custom" }, engineprofile.ErrInvalidProfile},
		{"sqlite version below range", func(root map[string]any) {
			version := root["profile"].(map[string]any)["version"].(map[string]any)
			version["minor"], version["patch"] = 34, 65535
		}, engineprofile.ErrUnsupportedVersion},
		{"sqlite wrong major", func(root map[string]any) { root["profile"].(map[string]any)["version"].(map[string]any)["major"] = 4 }, engineprofile.ErrUnsupportedVersion},
		{"builtin changed boolean capability", func(root map[string]any) {
			root["profile"].(map[string]any)["capabilities"].(map[string]any)["savepoints"] = false
		}, engineprofile.ErrInvalidProfile},
		{"builtin changed returning", func(root map[string]any) {
			root["profile"].(map[string]any)["capabilities"].(map[string]any)["returning"] = "insert"
		}, engineprofile.ErrInvalidProfile},
		{"builtin changed upsert", func(root map[string]any) {
			root["profile"].(map[string]any)["capabilities"].(map[string]any)["upsert"] = "none"
		}, engineprofile.ErrInvalidProfile},
		{"builtin changed update default", func(root map[string]any) {
			root["profile"].(map[string]any)["capabilities"].(map[string]any)["update_default"] = "expression"
		}, engineprofile.ErrInvalidProfile},
		{"builtin changed bind limit", func(root map[string]any) { root["profile"].(map[string]any)["max_bind_parameters"] = 998 }, engineprofile.ErrInvalidProfile},
	}
	for _, row := range rows {
		row := row
		t.Run(row.name, func(t *testing.T) {
			root := validationContractJSON(t, valid).(map[string]any)
			row.mutate(root)
			validationContractDecodeError(t, validationContractRehash(t, validationContractBytes(t, root)), row.target, "")
		})
	}
	for _, row := range []struct {
		name       string
		strategy   string
		capability string
	}{
		{"window strategy without windows", "window", "window_functions"},
		{"lateral strategy without lateral", "lateral", "lateral_joins"},
	} {
		t.Run(row.name, func(t *testing.T) {
			root := validationContractJSON(t, valid).(map[string]any)
			profile := root["profile"].(map[string]any)
			profile["engine"], profile["custom_name"], profile["version_known"] = "custom", "custom", false
			profile["version"] = map[string]any{"major": 0, "minor": 0, "patch": 0}
			caps := profile["capabilities"].(map[string]any)
			caps["per_parent_limit"], caps["upsert"], caps[row.capability] = row.strategy, "on_conflict", false
			validationContractDecodeError(t, validationContractRehash(t, validationContractBytes(t, root)), engineprofile.ErrInvalidProfile, "")
		})
	}
	for _, capability := range []string{"conflict_target", "default_values_upsert", "upsert_conflict_where", "upsert_update_where"} {
		t.Run("upsert capability without form "+capability, func(t *testing.T) {
			root := validationContractJSON(t, valid).(map[string]any)
			profile := root["profile"].(map[string]any)
			profile["engine"], profile["custom_name"], profile["version_known"] = "custom", "custom", false
			profile["version"] = map[string]any{"major": 0, "minor": 0, "patch": 0}
			caps := profile["capabilities"].(map[string]any)
			caps["upsert"] = "none"
			caps[capability] = true
			validationContractDecodeError(t, validationContractRehash(t, validationContractBytes(t, root)), engineprofile.ErrInvalidProfile, "")
		})
	}
}

func TestValidationContractArgumentGrammar(t *testing.T) {
	valid := validationContractValid(t)
	argumentPaths := map[string][]any{
		"null":             {"operations", 9, "statements", 0, "args", 0},
		"bool":             {"operations", 9, "statements", 0, "args", 1},
		"int64":            {"operations", 9, "statements", 0, "args", 2},
		"uint64":           {"operations", 9, "statements", 0, "args", 3},
		"float64":          {"operations", 9, "statements", 0, "args", 4},
		"string":           {"operations", 9, "statements", 0, "args", 5},
		"bytes_base64":     {"operations", 9, "statements", 0, "args", 6},
		"time_rfc3339nano": {"operations", 9, "statements", 0, "args", 7},
	}
	for tag, path := range argumentPaths {
		if tag == "null" {
			continue
		}
		t.Run(tag+" null value", func(t *testing.T) {
			validationContractDecodeError(t, validationContractMutation(t, valid, nil, append(path, "value")...), ErrInvalidWire, "")
		})
	}
	for _, row := range []struct {
		name  string
		path  []any
		value any
	}{
		{"null tag with false", argumentPaths["null"], false}, {"null tag with string", argumentPaths["null"], "value"}, {"null tag with object", argumentPaths["null"], map[string]any{}},
		{"bool string", argumentPaths["bool"], "false"}, {"bool number", argumentPaths["bool"], 1}, {"bool object", argumentPaths["bool"], map[string]any{}},
		{"int64 string", argumentPaths["int64"], "1"}, {"int64 fraction", argumentPaths["int64"], 1.5}, {"int64 below minimum", argumentPaths["int64"], -9223372036854775809.0}, {"int64 above maximum", argumentPaths["int64"], 9223372036854775808.0},
		{"uint64 string", argumentPaths["uint64"], "1"}, {"uint64 fraction", argumentPaths["uint64"], 1.5}, {"uint64 negative", argumentPaths["uint64"], -1}, {"uint64 above maximum", argumentPaths["uint64"], 18446744073709551616.0},
		{"float64 string", argumentPaths["float64"], "1.5"}, {"float64 object", argumentPaths["float64"], map[string]any{}},
		{"string boolean", argumentPaths["string"], true}, {"string number", argumentPaths["string"], 1}, {"string object", argumentPaths["string"], map[string]any{}},
		{"bytes non-string", argumentPaths["bytes_base64"], 1}, {"bytes invalid alphabet", argumentPaths["bytes_base64"], "%%%"}, {"bytes bad padding", argumentPaths["bytes_base64"], "Yg"}, {"bytes trailing junk", argumentPaths["bytes_base64"], "Yg==junk"},
		{"time non-string", argumentPaths["time_rfc3339nano"], 1}, {"time malformed", argumentPaths["time_rfc3339nano"], "not-time"}, {"time offset not canonical", argumentPaths["time_rfc3339nano"], "2024-01-02T03:04:05+09"}, {"time redundant fractional zeros", argumentPaths["time_rfc3339nano"], "2024-01-02T03:04:05.000000006000Z"}, {"time date out of range", argumentPaths["time_rfc3339nano"], "2024-13-02T03:04:05Z"},
		{"unknown argument tag", append(append([]any(nil), argumentPaths["bool"]...), "kind"), "unknown"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			path := row.path
			if row.name == "unknown argument tag" {
				validationContractDecodeError(t, validationContractMutation(t, valid, row.value, path...), ErrInvalidWire, "")
			} else {
				validationContractDecodeError(t, validationContractMutation(t, valid, row.value, append(path, "value")...), ErrInvalidWire, "")
			}
		})
	}
}

func validationContractFactCatalog(t *testing.T) Catalog {
	t.Helper()
	object, err := NewCatalogObject("orders-id", schema.TableDef{
		Schema: "public", Name: "orders", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		ExclusionConstraints: []schema.ExclusionDef{{Name: "orders_exclusion", Method: "gist", Elements: []schema.ExclusionElementDef{{Expression: "id", Operator: "="}}}},
	})
	require.NoError(t, err)
	catalog, err := NewCatalog(sourceRepairProfile(t), "validation-facts", []CatalogObject{object})
	require.NoError(t, err)
	return catalog
}

func TestValidationContractFactSemantics(t *testing.T) {
	for _, row := range []struct{ name, object, path, operator, value, contains string }{
		{"fact empty object", "", "/name", "equal", `"x"`, "object"},
		{"fact empty path", "orders-id", "", "equal", `"x"`, "path"},
		{"fact relative path", "orders-id", "name", "equal", `"x"`, "path"},
		{"malformed canonical value", "orders-id", "/name", "equal", "not-json", "canonical"},
		{"trailing canonical value", "orders-id", "/name", "equal", `"x" "y"`, "canonical"},
		{"present nonempty value", "orders-id", "$", "present", `"x"`, "no value"},
		{"absent nonempty value", "orders-id", "$", "absent", `"x"`, "no value"},
		{"present leaf path", "orders-id", "/columns/0/name", "present", "", "object path"},
		{"absent leaf path", "orders-id", "/columns/0/name", "absent", "", "object path"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			_, err := NewFact(ObjectID(row.object), row.path, row.operator, row.value)
			require.ErrorIs(t, err, ErrInvalidFact)
			require.ErrorContains(t, err, row.contains)
		})
	}
	catalog := validationContractFactCatalog(t)
	for _, row := range []struct {
		name, path, operator, value string
		target                      error
	}{
		{"leading zero index", "/columns/00/name", "equal", `"id"`, ErrInvalidFact},
		{"negative index", "/columns/-1/name", "equal", `"id"`, ErrFactMismatch},
		{"nonnumeric index", "/columns/nope/name", "equal", `"id"`, ErrFactMismatch},
		{"out of range index", "/columns/4/name", "equal", `"id"`, ErrFactMismatch},
		{"path crosses null", "/columns/0/native/name", "equal", `"x"`, ErrInvalidFact},
		{"path crosses string", "/columns/0/name/value", "equal", `"x"`, ErrInvalidFact},
		{"path crosses boolean", "/columns/0/nullable/value", "equal", `"x"`, ErrInvalidFact},
		{"unknown object field", "/does_not_exist", "equal", `"x"`, ErrFactMismatch},
		{"unknown nested field", "/columns/0/does_not_exist", "equal", `"x"`, ErrFactMismatch},
		{"malformed tilde escape", "/columns~2/name", "equal", `"x"`, ErrInvalidFact},
		{"trailing tilde", "/columns/0/name~", "equal", `"x"`, ErrInvalidFact},
		{"equal column object", "/columns/0", "equal", `{}`, ErrInvalidFact},
		{"equal exclusion object", "/exclusion_constraints/0", "equal", `{}`, ErrInvalidFact},
		{"present missing object", "$", "present", "", ErrFactMismatch},
		{"equal missing object", "/does_not_exist", "equal", `"x"`, ErrFactMismatch},
		{"absent existing object", "$", "absent", "", ErrFactMismatch},
		{"absent missing object", "$", "absent", "", nil},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			object := ObjectID("orders-id")
			if strings.Contains(row.name, "missing object") {
				object = "missing"
			}
			fact, err := NewFact(object, row.path, row.operator, row.value)
			require.NoError(t, err)
			err = EvaluateFact(catalog, fact)
			if row.target == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, row.target)
			}
		})
	}
}

func TestValidationContractDecisionSemantics(t *testing.T) {
	for _, row := range []struct {
		name             string
		kind             DecisionKind
		from, to, reason string
		accepted         bool
	}{
		{"rename rejected", DecisionRenameObject, "a", "b", "", false}, {"rename empty from", DecisionRenameObject, "", "b", "", true}, {"rename empty to", DecisionRenameObject, "a", "", "", true}, {"rename same from to", DecisionRenameObject, "a", "a", "", true}, {"rename nonempty reason", DecisionRenameObject, "a", "b", "reason", true},
		{"destructive rejected", DecisionAcceptDestructive, "", "", "approved", false}, {"destructive nonempty from", DecisionAcceptDestructive, "a", "", "approved", true}, {"destructive nonempty to", DecisionAcceptDestructive, "", "b", "approved", true}, {"destructive blank reason", DecisionAcceptDestructive, "", "", " ", true},
		{"native rejected", DecisionAcceptNativeSQL, "", "", "approved", false}, {"native nonempty from", DecisionAcceptNativeSQL, "a", "", "approved", true}, {"native nonempty to", DecisionAcceptNativeSQL, "", "b", "approved", true}, {"native blank reason", DecisionAcceptNativeSQL, "", "", " ", true},
		{"backfill rejected", DecisionSupplyBackfill, "", "", "approved", false}, {"backfill nonempty from", DecisionSupplyBackfill, "a", "", "approved", true}, {"backfill nonempty to", DecisionSupplyBackfill, "", "b", "approved", true}, {"backfill blank reason", DecisionSupplyBackfill, "", "", " ", true},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			_, err := NewDecision("decision", row.kind, "object", row.from, row.to, row.accepted, row.reason)
			require.ErrorIs(t, err, ErrInvalidDecision)
			require.ErrorContains(t, err, "invalid")
		})
	}
}

func TestValidationContractDecisionBindings(t *testing.T) {
	valid := validationContractValid(t)
	for _, row := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing destructive decision", func(root map[string]any) {
			root["decisions"] = validationContractWithoutDecision(root["decisions"].([]any), "destructive")
		}},
		{"wrong-object destructive decision", func(root map[string]any) { validationContractDecisionByID(root, "destructive")["object"] = "other" }},
		{"missing backfill decision", func(root map[string]any) {
			root["decisions"] = validationContractWithoutDecision(root["decisions"].([]any), "backfill")
		}},
		{"wrong-object backfill decision", func(root map[string]any) { validationContractDecisionByID(root, "backfill")["object"] = "other" }},
		{"destructive decision cannot approve backfill", func(root map[string]any) {
			validationContractDecisionByID(root, "backfill")["kind"] = "accept_destructive"
		}},
		{"missing native decision", func(root map[string]any) {
			root["decisions"] = validationContractWithoutDecision(root["decisions"].([]any), "native")
		}},
		{"wrong-object native decision", func(root map[string]any) { validationContractDecisionByID(root, "native")["object"] = "other" }},
		{"backfill decision cannot approve native", func(root map[string]any) { validationContractDecisionByID(root, "native")["kind"] = "supply_backfill" }},
		{"duplicate decision id", func(root map[string]any) {
			decisions := root["decisions"].([]any)
			decisions[1].(map[string]any)["id"] = decisions[0].(map[string]any)["id"]
		}},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			root := validationContractJSON(t, valid).(map[string]any)
			row.mutate(root)
			validationContractDecodeError(t, validationContractRehash(t, validationContractBytes(t, root)), ErrInvalidDecision, "")
		})
	}
}

func validationContractDecisionByID(root map[string]any, id string) map[string]any {
	for _, value := range root["decisions"].([]any) {
		decision := value.(map[string]any)
		if decision["id"] == id {
			return decision
		}
	}
	panic("missing decision")
}

func validationContractWithoutDecision(values []any, id string) []any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		if value.(map[string]any)["id"] != id {
			out = append(out, value)
		}
	}
	return out
}

func validationContractOperation(t *testing.T, kind OperationKind, objects []ObjectID, statements []stmt.Statement, reverse []stmt.Statement, reversible bool, args ...any) (Operation, error) {
	t.Helper()
	if statements == nil && args != nil {
		statements = []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), args...)}
	}
	return NewOperation("validation-operation", kind, nil, objects, nil, nil, Digest{1}, statements, TransactionForbidden, reversible, reverse)
}

func TestValidationContractOperationSemantics(t *testing.T) {
	validSQL := []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users ADD COLUMN value TEXT"))}
	for _, row := range []struct {
		name                string
		kind                OperationKind
		objects             []ObjectID
		statements, reverse []stmt.Statement
		reversible          bool
		target              error
		contains            string
	}{
		{"operation empty id", OperationAddColumn, []ObjectID{"object"}, validSQL, nil, false, ErrInvalidOperation, "id and kind"},
		{"operation empty kind", "", []ObjectID{"object"}, validSQL, nil, false, ErrInvalidOperation, "id and kind"},
		{"operation nil objects", OperationAddColumn, nil, validSQL, nil, false, ErrInvalidOperation, "needs an object"},
		{"operation empty objects", OperationAddColumn, []ObjectID{}, validSQL, nil, false, ErrInvalidOperation, "needs an object"},
		{"nil forward statements", OperationAddColumn, []ObjectID{"object"}, nil, nil, false, ErrInvalidOperation, "forward SQL"},
		{"empty forward statements", OperationAddColumn, []ObjectID{"object"}, []stmt.Statement{}, nil, false, ErrInvalidOperation, "forward SQL"},
		{"empty SQL", OperationAddColumn, []ObjectID{"object"}, []stmt.Statement{stmt.New(sqltext.Text(""))}, nil, false, ErrInvalidOperation, "statement SQL is empty"},
		{"whitespace SQL", OperationAddColumn, []ObjectID{"object"}, []stmt.Statement{stmt.New(sqltext.Text("  "))}, nil, false, ErrInvalidOperation, "statement SQL is empty"},
		{"reversible nil reverse", OperationNativeSQL, []ObjectID{"object"}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, nil, true, ErrInvalidOperation, "reverse"},
		{"reversible empty reverse", OperationNativeSQL, []ObjectID{"object"}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, []stmt.Statement{}, true, ErrInvalidOperation, "reverse"},
		{"irreversible one reverse", OperationNativeSQL, []ObjectID{"object"}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, validSQL, false, ErrInvalidOperation, "reverse"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			id := OperationID("validation-operation")
			if row.name == "operation empty id" {
				id = ""
			}
			operation, err := NewOperation(id, row.kind, nil, row.objects, nil, nil, Digest{1}, row.statements, TransactionForbidden, row.reversible, row.reverse)
			if row.name == "irreversible one reverse" {
				require.ErrorIs(t, err, row.target)
				return
			}
			require.ErrorIs(t, err, row.target)
			require.ErrorContains(t, err, row.contains)
			_ = operation
		})
	}
	t.Run("create table one object succeeds", func(t *testing.T) {
		operation, err := NewOperation("create-one", OperationCreateTable, nil, []ObjectID{"object"}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("CREATE TABLE created (id INTEGER)"))}, TransactionRequired, false, nil)
		require.NoError(t, err)
		require.Equal(t, []ObjectID{"object"}, operation.Objects())
	})
	t.Run("create table two objects", func(t *testing.T) {
		_, err := NewOperation("create-two", OperationCreateTable, nil, []ObjectID{"object", "other"}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("CREATE TABLE created (id INTEGER)"))}, TransactionRequired, false, nil)
		require.ErrorIs(t, err, ErrInvalidOperation)
		require.ErrorContains(t, err, "create_table needs one object")
	})
	t.Run("rename table two objects remains aggregate", func(t *testing.T) {
		_, identity, object, _ := validationContractIdentity(t)
		rename, err := NewOperation("rename", OperationRenameTable, nil, []ObjectID{object.ID()}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users RENAME TO renamed"))}, TransactionForbidden, false, nil)
		require.NoError(t, err)
		rename.objects = []ObjectID{object.ID(), "other"}
		binding, err := NewBaselineRename("rename", object.ID(), "main", "renamed")
		require.NoError(t, err)
		baseline, err := NewBaselineIdentity(identity, "validation", []BaselineObject{object}, []BaselineRename{binding})
		require.NoError(t, err)
		err = validateRenameDecisions(baseline, nil, []Operation{rename})
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "rename binding mismatch")
	})
	for _, row := range []struct {
		name  string
		value any
	}{
		{"sql.NamedArg", sql.Named("value", "x")}, {"struct", struct{ Value string }{"x"}}, {"NaN", math.NaN()}, {"positive infinity", math.Inf(1)}, {"negative infinity", math.Inf(-1)},
	} {
		row := row
		for _, kind := range []OperationKind{OperationBackfill, OperationNativeSQL} {
			kind := kind
			t.Run(string(kind)+" forward/"+row.name, func(t *testing.T) {
				_, err := validationContractOperation(t, kind, []ObjectID{"object"}, nil, nil, false, row.value)
				require.ErrorIs(t, err, ErrInvalidOperation)
				require.ErrorIs(t, err, ErrUnsupportedArg)
				require.ErrorContains(t, err, "unsupported statement argument")
			})
			t.Run(string(kind)+" reverse/"+row.name, func(t *testing.T) {
				forward := []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}
				reverse := []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), row.value)}
				_, err := NewOperation("validation-reverse-"+OperationID(kind), kind, nil, []ObjectID{"object"}, nil, nil,
					Digest{1}, forward, TransactionForbidden, true, reverse)
				require.ErrorIs(t, err, ErrInvalidOperation)
				require.ErrorIs(t, err, ErrUnsupportedArg)
				require.ErrorContains(t, err, "unsupported statement argument")
			})
		}
	}
	for _, kind := range []OperationKind{OperationCreateTable, OperationDropTable, OperationRenameTable, OperationAddColumn, OperationDropColumn, OperationRenameColumn, OperationAlterColumn, OperationCreateIndex, OperationDropIndex, OperationAddConstraint, OperationDropConstraint} {
		t.Run("DDL argument "+string(kind), func(t *testing.T) {
			_, err := validationContractOperation(t, kind, []ObjectID{"object"}, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE x ADD COLUMN y TEXT"), true)}, nil, false)
			require.ErrorIs(t, err, ErrInvalidOperation)
			require.ErrorContains(t, err, "DDL statement has arguments")
		})
	}
}

func validationContractIdentity(t *testing.T) (Profile, CatalogIdentity, BaselineObject, HistoryIdentity) {
	t.Helper()
	profile := sourceRepairProfile(t)
	profileDigest, err := ProfileDigest(profile)
	require.NoError(t, err)
	identity, err := NewCatalogIdentity(profile.Engine(), profileDigest, Digest{1}, Digest{2})
	require.NoError(t, err)
	object, err := NewBaselineObject("object", "table", "main", "users")
	require.NoError(t, err)
	history, err := NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	return profile, identity, object, history
}

func validationContractBaselineObject(t *testing.T, id, schemaName, name string) BaselineObject {
	t.Helper()
	object, err := NewBaselineObject(ObjectID(id), "table", schemaName, name)
	require.NoError(t, err)
	return object
}

func TestValidationContractBaselineSemantics(t *testing.T) {
	_, identity, object, _ := validationContractIdentity(t)
	for _, row := range []struct {
		name     string
		mutate   func([]BaselineObject, []BaselineRename) (BaselineObject, []BaselineRename)
		contains string
	}{
		{"baseline catalog engine zero", func(objects []BaselineObject, renames []BaselineRename) (BaselineObject, []BaselineRename) {
			return object, renames
		}, "catalog"},
	} {
		_ = row
	}
	for _, row := range []struct {
		name                            string
		id, kind, nameValue, schemaName string
		target                          error
		contains                        string
	}{
		{"blank id", "", "table", "users", "main", ErrInvalidIdentity, "baseline object fields are required"},
		{"blank kind", "object", "", "users", "main", ErrInvalidIdentity, "baseline object fields are required"},
		{"blank name", "object", "table", "", "main", ErrInvalidIdentity, "baseline object fields are required"},
		{"blank schema remains valid", "object", "table", "users", "", nil, ""},
	} {
		row := row
		t.Run("baseline object "+row.name, func(t *testing.T) {
			candidate, err := NewBaselineObject(ObjectID(row.id), row.kind, row.schemaName, row.nameValue)
			if row.target == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, row.target)
			require.ErrorContains(t, err, row.contains)
			_ = candidate
		})
	}
	for _, row := range []struct {
		name     string
		objects  []BaselineObject
		target   error
		contains string
	}{
		{"duplicate starting id", []BaselineObject{object, object}, ErrInvalidIdentity, "duplicate object ID"},
		{"duplicate active qualified name", []BaselineObject{object, validationContractBaselineObject(t, "second", "main", "users")}, ErrInvalidIdentity, "duplicate qualified name"},
		{"equal names different schemas", []BaselineObject{object, validationContractBaselineObject(t, "second", "other", "users")}, nil, ""},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			_, err := NewBaselineIdentity(identity, "validation", row.objects, nil)
			if row.target == nil {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, row.target)
				require.ErrorContains(t, err, row.contains)
			}
		})
	}
}

func validationContractPlanParts(t *testing.T) (Profile, BaselineIdentity, HistoryIdentity, Operation, Decision) {
	t.Helper()
	profile, identity, object, history := validationContractIdentity(t)
	baseline, err := NewBaselineIdentity(identity, "validation", []BaselineObject{object}, nil)
	require.NoError(t, err)
	operation, err := NewOperation("native", OperationNativeSQL, nil, []ObjectID{object.ID()}, nil, nil, Digest{1},
		[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, TransactionForbidden, false, nil)
	require.NoError(t, err)
	decision, err := NewDecision("native-decision", DecisionAcceptNativeSQL, object.ID(), "", "", true, "approved")
	require.NoError(t, err)
	return profile, baseline, history, operation, decision
}

func TestValidationContractPlanSemantics(t *testing.T) {
	profile, baseline, history, operation, decision := validationContractPlanParts(t)
	for _, row := range []struct {
		name     string
		mutate   func([]Operation) []Operation
		target   error
		contains string
	}{
		{"duplicate operation id", func(operations []Operation) []Operation { return []Operation{operations[0], operations[0]} }, ErrInvalidOperation, "duplicate operation ID"},
		{"missing dependency earlier", func(operations []Operation) []Operation {
			value := operations[0]
			value.dependsOn = []OperationID{"missing"}
			return []Operation{value}
		}, ErrInvalidOperation, "missing dependency"},
		{"self cycle", func(operations []Operation) []Operation {
			value := operations[0]
			value.dependsOn = []OperationID{value.ID()}
			return []Operation{value}
		}, ErrInvalidOperation, "depends on itself"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			_, err := newPlan(profile, baseline, history, []Decision{decision}, row.mutate([]Operation{operation}))
			require.ErrorIs(t, err, row.target)
			require.ErrorContains(t, err, row.contains)
		})
	}
	first, err := NewOperation("first", OperationNativeSQL, nil, []ObjectID{"object"}, nil, nil, Digest{2}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, TransactionForbidden, false, nil)
	require.NoError(t, err)
	second, err := NewOperation("second", OperationNativeSQL, []OperationID{"first"}, []ObjectID{"object"}, nil, nil, Digest{3}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 2"))}, TransactionForbidden, false, nil)
	require.NoError(t, err)
	plan, err := newPlan(profile, baseline, history, []Decision{decision}, []Operation{second, first})
	require.NoError(t, err)
	order, err := plan.StableOperationOrder()
	require.NoError(t, err)
	require.Equal(t, []OperationID{"first", "second"}, order)
	for _, row := range []struct {
		name     string
		mutate   func(*BaselineIdentity)
		target   error
		contains string
	}{
		{"baseline engine differs from profile", func(value *BaselineIdentity) { value.catalog.engine = MySQLEngine }, ErrInvalidPlan, "baseline engine does not match profile"},
		{"baseline profile digest differs", func(value *BaselineIdentity) { value.catalog.profileDigest = Digest{9} }, ErrInvalidPlan, "baseline profile digest does not match profile"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			bad := cloneBaseline(baseline)
			row.mutate(&bad)
			_, err := newPlan(profile, bad, history, []Decision{decision}, []Operation{operation})
			require.ErrorIs(t, err, row.target)
			require.ErrorContains(t, err, row.contains)
		})
	}
	zero := Plan{}
	_, err = Encode(zero)
	require.ErrorIs(t, err, ErrInvalidPlan)
	plan.operations[0].resultDigest = Digest{8}
	_, err = Encode(plan)
	require.ErrorIs(t, err, ErrInvalidPlan)

	future, err := NewIntroducedBaselineObject("validation", "create", schema.TableDef{Schema: "main", Name: "created", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	futureBaseline, err := NewBaselineIdentity(baseline.catalog, "validation", []BaselineObject{objectFromBaseline(baseline), future}, nil)
	require.NoError(t, err)
	create, err := NewOperation("create", OperationCreateTable, nil, []ObjectID{future.ID()}, nil, nil, Digest{4}, []stmt.Statement{stmt.New(sqltext.Text("CREATE TABLE created (id INTEGER)"))}, TransactionRequired, false, nil)
	require.NoError(t, err)
	create.objects = []ObjectID{future.ID(), "object"}
	err = validateFutureObjectIDs(futureBaseline, []Operation{create})
	require.ErrorIs(t, err, ErrInvalidIdentity)
	require.ErrorContains(t, err, "no matching create operation")
}

func objectFromBaseline(b BaselineIdentity) BaselineObject { return b.objects[0] }
