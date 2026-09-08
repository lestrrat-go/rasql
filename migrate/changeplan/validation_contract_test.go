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
	"time"

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
	require.True(t, json.Valid(encoded))
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
		{name: "finite float", path: []any{"operations", 9, "statements", 0, "args", 4, "value"}, value: json.Number("1.25"), index: 4, want: float64(1.25)},
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
	encoded := validationContractBytes(t, root)
	require.True(t, json.Valid(encoded))
	return validationContractRehash(t, encoded)
}

func validationContractRawTokenMutation(t *testing.T, data []byte, token string, path ...any) []byte {
	t.Helper()
	root := validationContractClone(t, validationContractJSON(t, data))
	const marker = "__validation_contract_raw_token__"
	validationContractSet(root, marker, path...)
	encoded := validationContractBytes(t, root)
	quotedMarker, err := json.Marshal(marker)
	require.NoError(t, err)
	require.Equal(t, 1, bytes.Count(encoded, quotedMarker))
	encoded = bytes.Replace(encoded, quotedMarker, []byte(token), 1)
	return encoded
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
	path            []any
	value           any
	allowNull       bool
	missingFragment string
	nullFragment    string
	typeFragment    string
}

func validationContractFieldFragments(path []any, value any) (string, string, string) {
	key := path[len(path)-1].(string)
	missing := `missing required wire field "` + key + `"`
	if key == "value" {
		for _, part := range path {
			if part == "args" {
				return "", "", ""
			}
		}
	}
	if key == "result_digest" {
		return missing, "", ""
	}
	switch value.(type) {
	case string:
		return missing, key + " must be a string", key + " must be a string"
	case bool:
		return missing, key + " must be a boolean", key + " must be a boolean"
	case json.Number:
		return missing, key + " must be an integer", key + " must be an integer"
	case []any:
		return missing, "null array " + key, "array " + key
	case map[string]any:
		return missing, "object expected", "object expected"
	default:
		return missing, "", ""
	}
}

func validationContractWireFields(t *testing.T, root any) []validationContractField {
	t.Helper()
	fields := make([]validationContractField, 0, 320)
	add := func(path ...any) {
		value := validationContractAt(root, path...)
		missing, null, wrong := validationContractFieldFragments(path, value)
		fields = append(fields, validationContractField{path: path, value: value, missingFragment: missing, nullFragment: null, typeFragment: wrong})
	}
	for _, key := range []string{"format", "id"} {
		add(key)
	}
	for _, path := range [][]any{
		{"profile"}, {"baseline"}, {"history"}, {"decisions"}, {"operations"},
		{"profile", "version"}, {"profile", "capabilities"},
		{"baseline", "objects"}, {"baseline", "renames"},
	} {
		add(path...)
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
	}
	for _, row := range []struct {
		path []any
	}{
		{path: []any{"operations", 0, "preconditions", 0}},
		{path: []any{"operations", 1, "preconditions", 0}},
		{path: []any{"operations", 9, "postconditions", 0}},
		{path: []any{"operations", 10, "preconditions", 0}},
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
		{path: []any{"operations", 10, "statements", 0, "args", 0}, allowNull: true},
		{path: []any{"operations", 10, "statements", 0, "args", 1}},
		{path: []any{"operations", 10, "statements", 0, "args", 2}},
		{path: []any{"operations", 10, "statements", 0, "args", 3}},
		{path: []any{"operations", 10, "statements", 0, "args", 4}},
		{path: []any{"operations", 10, "statements", 0, "args", 5}},
		{path: []any{"operations", 10, "statements", 0, "args", 6}},
		{path: []any{"operations", 10, "statements", 0, "args", 7}},
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

func validationContractObservedFields(root any) []string {
	var paths []string
	var walk func(any, []any)
	walk = func(value any, path []any) {
		switch current := value.(type) {
		case map[string]any:
			for key, child := range current {
				childPath := append(append([]any(nil), path...), key)
				paths = append(paths, validationContractPath(childPath))
				walk(child, childPath)
			}
		case []any:
			for index, child := range current {
				walk(child, append(append([]any(nil), path...), index))
			}
		}
	}
	walk(root, nil)
	sort.Strings(paths)
	return paths
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
	fields := validationContractWireFields(t, root)
	staticPaths := make([]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		path := validationContractPath(field.path)
		_, duplicate := seen[path]
		require.False(t, duplicate, "duplicate static field path %s", path)
		seen[path] = struct{}{}
		staticPaths = append(staticPaths, path)
	}
	sort.Strings(staticPaths)
	require.Equal(t, validationContractObservedFields(root), staticPaths)

	for _, field := range fields {
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
					fragment := field.typeFragment
					switch test.name {
					case "missing":
						fragment = field.missingFragment
					case "null":
						fragment = field.nullFragment
					}
					validationContractDecodeError(t, data, ErrInvalidWire, fragment)
				})
			}
		})
	}

	for _, test := range []struct {
		name            string
		path            []any
		missingFragment string
		nullFragment    string
		typeFragment    string
	}{
		{"decisions", []any{"decisions"}, `missing required wire field "decisions"`, "null array decisions", "array decisions"},
		{"operations", []any{"operations"}, `missing required wire field "operations"`, "null array operations", "array operations"},
		{"baseline.objects", []any{"baseline", "objects"}, `missing required wire field "objects"`, "null array objects", "array objects"},
		{"baseline.renames", []any{"baseline", "renames"}, `missing required wire field "renames"`, "null array renames", "array renames"},
		{"operations[1].depends_on", []any{"operations", 1, "depends_on"}, `missing required wire field "depends_on"`, "null array depends_on", "array depends_on"},
		{"operations[0].objects", []any{"operations", 0, "objects"}, `missing required wire field "objects"`, "null array objects", "array objects"},
		{"operations[1].preconditions", []any{"operations", 1, "preconditions"}, `missing required wire field "preconditions"`, "null array preconditions", "array preconditions"},
		{"operations[9].postconditions", []any{"operations", 9, "postconditions"}, `missing required wire field "postconditions"`, "null array postconditions", "array postconditions"},
		{"operations[0].statements", []any{"operations", 0, "statements"}, `missing required wire field "statements"`, "null array statements", "array statements"},
		{"operations[10].reverse_statements", []any{"operations", 10, "reverse_statements"}, `missing required wire field "reverse_statements"`, "null array reverse_statements", "array reverse_statements"},
		{"operations[9].statements[0].args", []any{"operations", 9, "statements", 0, "args"}, `missing required wire field "args"`, "null array args", "array args"},
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
				fragment := test.typeFragment
				switch shape.name {
				case "missing":
					fragment = test.missingFragment
				case "null":
					fragment = test.nullFragment
				}
				validationContractDecodeError(t, validationContractBytes(t, copyRoot), ErrInvalidWire, fragment)
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
		{"precondition create", []any{"operations", 0, "preconditions", 0}},
		{"precondition", []any{"operations", 1, "preconditions", 0}},
		{"postcondition", []any{"operations", 9, "postconditions", 0}},
		{"precondition native", []any{"operations", 10, "preconditions", 0}},
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
				validationContractDecodeError(t, validationContractBytes(t, copyRoot), ErrInvalidWire, "object expected")
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
		{"precondition native", []any{"operations", 10, "preconditions", 0}, "preconditions"},
		{"forward argument", []any{"operations", 9, "statements", 0, "args", 0}, "args"},
		{"native argument", []any{"operations", 10, "statements", 0, "args", 0}, "args"},
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
				validationContractDecodeError(t, validationContractBytes(t, copyRoot), ErrInvalidWire, "object expected")
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
		}{"create precondition", []any{"operations", 0, "preconditions", 0}},
		struct {
			name string
			path []any
		}{"precondition", []any{"operations", 1, "preconditions", 0}},
		struct {
			name string
			path []any
		}{"postcondition", []any{"operations", 9, "postconditions", 0}},
		struct {
			name string
			path []any
		}{"native precondition", []any{"operations", 10, "preconditions", 0}},
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
		{"native null", []any{"operations", 10, "statements", 0, "args", 0}},
		{"native bool", []any{"operations", 10, "statements", 0, "args", 1}},
		{"native int64", []any{"operations", 10, "statements", 0, "args", 2}},
		{"native uint64", []any{"operations", 10, "statements", 0, "args", 3}},
		{"native float64", []any{"operations", 10, "statements", 0, "args", 4}},
		{"native string", []any{"operations", 10, "statements", 0, "args", 5}},
		{"native bytes_base64", []any{"operations", 10, "statements", 0, "args", 6}},
		{"native time_rfc3339nano", []any{"operations", 10, "statements", 0, "args", 7}},
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
	for _, row := range []struct {
		name  string
		value string
		want  string
	}{
		{"plan id short", "abcd", ""},
		{"plan id long", strings.Repeat("a", 65), ""},
		{"plan id nonhex", strings.Repeat("z", 64), ""},
		{"plan id uppercase", strings.Repeat("A", 64), ""},
		{"plan id stale valid hex", strings.Repeat("0", 64), "plan ID mismatch"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			root := validationContractJSON(t, valid)
			validationContractSet(root, row.value, "id")
			validationContractDecodeError(t, validationContractBytes(t, root), ErrInvalidWire, row.want)
		})
	}
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
		rehash bool
	}{
		{"valid bind limit", json.Number("999"), nil, true},
		{"zero bind limit", json.Number("0"), engineprofile.ErrInvalidProfile, true},
		{"negative bind limit", json.Number("-1"), engineprofile.ErrInvalidProfile, true},
		{"bind limit integer overflow", json.Number("9223372036854775808"), ErrInvalidWire, false},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			var data []byte
			if token, ok := row.value.(json.Number); ok && !row.rehash {
				data = validationContractRawTokenMutation(t, valid, token.String(), "profile", "max_bind_parameters")
				require.True(t, json.Valid(data))
			} else {
				data = validationContractMutation(t, valid, row.value, "profile", "max_bind_parameters")
			}
			if row.target == nil {
				plan, err := Decode(data)
				require.NoError(t, err)
				require.Equal(t, 999, plan.Profile().Limits().MaxBindParameters)
				return
			}
			validationContractDecodeError(t, data, row.target, "")
		})
	}
	for _, field := range []string{"major", "minor", "patch"} {
		for _, row := range []struct {
			name  string
			value json.Number
		}{
			{"below minimum", json.Number("-1")},
			{"above maximum", json.Number("65536")},
			{"fractional", json.Number("1.5")},
			{"string", json.Number(`"1"`)},
		} {
			row := row
			t.Run(field+" "+row.name, func(t *testing.T) {
				root := validationContractJSON(t, valid)
				var data []byte
				if strings.HasPrefix(row.value.String(), `"`) {
					validationContractSet(root, "1", "profile", "version", field)
					data = validationContractBytes(t, root)
				} else {
					validationContractSet(root, row.value, "profile", "version", field)
					data = validationContractBytes(t, root)
				}
				validationContractDecodeError(t, data, ErrInvalidWire, "")
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
	reverseValid := validationContractReverseArguments(t, valid)
	argumentPaths := []struct {
		name string
		data []byte
		path []any
	}{
		{"forward", valid, []any{"operations", 9, "statements", 0, "args"}},
		{"native", valid, []any{"operations", 10, "statements", 0, "args"}},
		{"reverse", reverseValid, []any{"operations", 10, "reverse_statements", 0, "args"}},
	}
	tagPaths := []struct {
		name     string
		index    int
		fragment string
	}{
		{"null", 0, "null argument value"},
		{"bool", 1, "argument bool cannot be null"},
		{"int64", 2, "argument int64 cannot be null"},
		{"uint64", 3, "argument uint64 cannot be null"},
		{"float64", 4, "argument float64 cannot be null"},
		{"string", 5, "argument string cannot be null"},
		{"bytes_base64", 6, "argument bytes_base64 cannot be null"},
		{"time_rfc3339nano", 7, "argument time_rfc3339nano cannot be null"},
	}
	for _, source := range argumentPaths {
		source := source
		for _, tag := range tagPaths {
			tag := tag
			if tag.name == "null" {
				continue
			}
			t.Run(source.name+" "+tag.name+" null value", func(t *testing.T) {
				path := append(append([]any(nil), source.path...), tag.index, "value")
				data := validationContractMutation(t, source.data, nil, path...)
				validationContractDecodeError(t, data, ErrInvalidWire, tag.fragment)
			})
		}
	}

	for _, source := range argumentPaths {
		source := source
		for _, tag := range tagPaths {
			tag := tag
			path := append(append([]any(nil), source.path...), tag.index)
			for _, row := range []struct {
				name  string
				value any
				want  string
			}{
				{"false value", false, "null argument value"},
				{"string value", "value", "null argument value"},
				{"object value", map[string]any{}, "null argument value"},
			} {
				row := row
				if tag.name != "null" {
					break
				}
				t.Run(source.name+" "+tag.name+" "+row.name, func(t *testing.T) {
					data := validationContractMutation(t, source.data, row.value, append(path, "value")...)
					validationContractDecodeError(t, data, ErrInvalidWire, row.want)
				})
			}
		}
	}

	for _, source := range argumentPaths {
		source := source
		for _, row := range []struct {
			name     string
			tag      string
			index    int
			value    any
			fragment string
		}{
			{"bool string", "bool", 1, "false", "bool argument"},
			{"bool number", "bool", 1, json.Number("1"), "bool argument"},
			{"bool object", "bool", 1, map[string]any{}, "bool argument"},
			{"int64 string", "int64", 2, "1", "int64 argument"},
			{"int64 fraction", "int64", 2, json.Number("1.5"), "int64 argument"},
			{"uint64 string", "uint64", 3, "1", "uint64 argument"},
			{"uint64 fraction", "uint64", 3, json.Number("1.5"), "uint64 argument"},
			{"uint64 negative", "uint64", 3, json.Number("-1"), "uint64 argument"},
			{"float64 string", "float64", 4, "1.5", "float64 argument"},
			{"float64 object", "float64", 4, map[string]any{}, "float64 argument"},
			{"string boolean", "string", 5, true, "string argument"},
			{"string number", "string", 5, json.Number("1"), "string argument"},
			{"string object", "string", 5, map[string]any{}, "string argument"},
			{"bytes non-string", "bytes_base64", 6, json.Number("1"), "string argument"},
			{"bytes invalid alphabet", "bytes_base64", 6, "%%%", "bytes argument"},
			{"bytes bad padding", "bytes_base64", 6, "Yg", "bytes argument"},
			{"bytes trailing junk", "bytes_base64", 6, "Yg==junk", "bytes argument"},
			{"time non-string", "time_rfc3339nano", 7, json.Number("1"), "string argument"},
			{"time malformed", "time_rfc3339nano", 7, "not-time", "time argument"},
			{"time offset not canonical", "time_rfc3339nano", 7, "2024-01-02T03:04:05+00:00", "non-canonical time argument"},
			{"time redundant fractional zeros", "time_rfc3339nano", 7, "2024-01-02T03:04:05.000000006000Z", "non-canonical time argument"},
			{"time date out of range", "time_rfc3339nano", 7, "2024-13-02T03:04:05Z", "time argument"},
		} {
			row := row
			t.Run(source.name+" "+row.name, func(t *testing.T) {
				path := append(append([]any(nil), source.path...), row.index, "value")
				data := validationContractMutation(t, source.data, row.value, path...)
				validationContractDecodeError(t, data, ErrInvalidWire, row.fragment)
			})
		}
	}

	for _, source := range argumentPaths {
		source := source
		for _, row := range []struct {
			name  string
			index int
			token json.Number
			want  string
		}{
			{"int64 below minimum", 2, json.Number("-9223372036854775809"), "int64 argument"},
			{"int64 above maximum", 2, json.Number("9223372036854775808"), "int64 argument"},
			{"uint64 above maximum", 3, json.Number("18446744073709551616"), "uint64 argument"},
			{"float64 positive overflow", 4, json.Number("1e309"), "float64 argument"},
			{"float64 negative overflow", 4, json.Number("-1e309"), "float64 argument"},
		} {
			row := row
			t.Run(source.name+" "+row.name, func(t *testing.T) {
				path := append(append([]any(nil), source.path...), row.index, "value")
				data := validationContractNumberMutation(t, source.data, row.token, path...)
				validationContractDecodeError(t, data, ErrInvalidWire, row.want)
			})
		}
	}

	for _, source := range argumentPaths {
		source := source
		for _, row := range []struct {
			name  string
			index int
			token string
		}{
			{"float64 NaN", 4, "NaN"},
			{"float64 Infinity", 4, "Infinity"},
		} {
			row := row
			t.Run(source.name+" "+row.name, func(t *testing.T) {
				path := append(append([]any(nil), source.path...), row.index, "value")
				data := validationContractRawTokenMutation(t, source.data, row.token, path...)
				require.False(t, json.Valid(data))
				validationContractDecodeError(t, data, ErrInvalidWire, "")
			})
		}
	}

	for _, source := range argumentPaths {
		source := source
		t.Run(source.name+" exact nanosecond time", func(t *testing.T) {
			path := append(append([]any(nil), source.path...), 7, "value")
			data := validationContractMutation(t, source.data, "2024-01-02T03:04:05.000000006Z", path...)
			plan, err := Decode(data)
			require.NoError(t, err)
			var got any
			switch source.name {
			case "reverse":
				got = plan.Operations()[10].ReverseStatements()[0].Args()[7]
			case "native":
				got = plan.Operations()[10].Statements()[0].Args()[7]
			default:
				got = plan.Operations()[9].Statements()[0].Args()[7]
			}
			want, err := time.Parse(time.RFC3339Nano, "2024-01-02T03:04:05.000000006Z")
			require.NoError(t, err)
			require.Equal(t, want, got)
		})
	}

	for _, source := range argumentPaths {
		source := source
		t.Run(source.name+" unknown argument tag", func(t *testing.T) {
			path := append(append([]any(nil), source.path...), 1, "kind")
			data := validationContractMutation(t, source.data, "unknown", path...)
			validationContractDecodeError(t, data, ErrInvalidWire, "argument kind")
		})
	}
}

func validationContractFactCatalog(t *testing.T) Catalog {
	t.Helper()
	object, err := NewCatalogObject("orders-id", schema.TableDef{
		Schema: "public", Name: "orders", Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "status", Type: schema.OpaqueType{}, NativeType: &schema.NativeTypeDef{
				Dialect: "postgresql", Schema: "public", Name: "status", Kind: schema.NativeEnum,
				Arguments: []string{"new", "done"},
			}},
		},
		UniqueConstraints:    []schema.UniqueDef{{Name: "orders_unique", Columns: []string{"id"}}},
		Indexes:              []schema.IndexDef{{Name: "orders_index", Columns: []string{"id"}}},
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
	t.Run("noncanonical whitespace value", func(t *testing.T) {
		data := validationContractValid(t)
		root := validationContractJSON(t, data)
		validationContractSet(root, `{ "x": 1 }`, "operations", 9, "postconditions", 0, "value")
		data = validationContractBytes(t, root)
		require.True(t, json.Valid(data))
		validationContractDecodeError(t, validationContractRehash(t, data), ErrInvalidWire, "non-canonical fact value")
	})

	catalog := validationContractFactCatalog(t)
	for _, row := range []struct {
		name, path, operator, value, contains string
		target                                error
	}{
		{"leading zero index", "/columns/00/name", "equal", `"id"`, "array index \"00\" is non-canonical", ErrInvalidFact},
		{"negative index", "/columns/-1/name", "equal", `"id"`, "array index \"-1\" is invalid", ErrFactMismatch},
		{"nonnumeric index", "/columns/nope/name", "equal", `"id"`, "array index \"nope\" is invalid", ErrFactMismatch},
		{"out of range index", "/columns/4/name", "equal", `"id"`, "array index \"4\" is invalid", ErrFactMismatch},
		{"path crosses null", "/columns/0/native/name", "equal", `"x"`, "path crosses null", ErrInvalidFact},
		{"path crosses string", "/columns/0/name/value", "equal", `"x"`, "path crosses scalar", ErrInvalidFact},
		{"path crosses boolean", "/columns/0/nullable/value", "equal", `"x"`, "path crosses scalar", ErrInvalidFact},
		{"unknown object field", "/does_not_exist", "equal", `"x"`, "path segment \"does_not_exist\" is unknown", ErrFactMismatch},
		{"unknown nested field", "/columns/0/does_not_exist", "equal", `"x"`, "path segment \"does_not_exist\" is unknown", ErrFactMismatch},
		{"malformed tilde escape", "/columns~2/name", "equal", `"x"`, "malformed escape", ErrInvalidFact},
		{"trailing tilde", "/columns/0/name~", "equal", `"x"`, "malformed escape", ErrInvalidFact},
		{"equal column object", "/columns/0", "equal", `{}`, "equality path /columns/0 selects an object", ErrInvalidFact},
		{"equal native object", "/columns/1/native", "equal", `{}`, "equality path /columns/1/native selects an object", ErrInvalidFact},
		{"equal constraint object", "/constraints/0", "equal", `{}`, "equality path /constraints/0 selects an object", ErrInvalidFact},
		{"equal index object", "/indexes/0", "equal", `{}`, "equality path /indexes/0 selects an object", ErrInvalidFact},
		{"equal exclusion object", "/exclusion_constraints/0", "equal", `{}`, "equality path /exclusion_constraints/0 selects an object", ErrInvalidFact},
		{"present missing object", "$", "present", "", "object \"missing\" is missing", ErrFactMismatch},
		{"equal missing object", "/does_not_exist", "equal", `"x"`, "object \"missing\" is missing", ErrFactMismatch},
		{"absent existing object", "$", "absent", "", "$ is present", ErrFactMismatch},
		{"absent missing object", "$", "absent", "", "", nil},
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
				require.ErrorContains(t, err, row.contains)
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
		{"decision empty id", DecisionAcceptNativeSQL, "", "", "", true},
		{"decision empty object", DecisionAcceptNativeSQL, "object", "", "", true},
		{"rename rejected", DecisionRenameObject, "a", "b", "", false}, {"rename empty from", DecisionRenameObject, "", "b", "", true}, {"rename empty to", DecisionRenameObject, "a", "", "", true}, {"rename same from to", DecisionRenameObject, "a", "a", "", true}, {"rename nonempty reason", DecisionRenameObject, "a", "b", "reason", true},
		{"destructive rejected", DecisionAcceptDestructive, "", "", "approved", false}, {"destructive nonempty from", DecisionAcceptDestructive, "a", "", "approved", true}, {"destructive nonempty to", DecisionAcceptDestructive, "", "b", "approved", true}, {"destructive blank reason", DecisionAcceptDestructive, "", "", " ", true},
		{"native rejected", DecisionAcceptNativeSQL, "", "", "approved", false}, {"native nonempty from", DecisionAcceptNativeSQL, "a", "", "approved", true}, {"native nonempty to", DecisionAcceptNativeSQL, "", "b", "approved", true}, {"native blank reason", DecisionAcceptNativeSQL, "", "", " ", true},
		{"backfill rejected", DecisionSupplyBackfill, "", "", "approved", false}, {"backfill nonempty from", DecisionSupplyBackfill, "a", "", "approved", true}, {"backfill nonempty to", DecisionSupplyBackfill, "", "b", "approved", true}, {"backfill blank reason", DecisionSupplyBackfill, "", "", " ", true},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			object := "object"
			if row.name == "decision empty object" {
				object = ""
			}
			id := DecisionID("decision")
			if row.name == "decision empty id" {
				id = ""
			}
			_, err := NewDecision(id, row.kind, ObjectID(object), row.from, row.to, row.accepted, row.reason)
			require.ErrorIs(t, err, ErrInvalidDecision)
			if strings.HasPrefix(row.name, "decision empty") {
				require.ErrorContains(t, err, "id and object")
			} else {
				require.ErrorContains(t, err, "invalid")
			}
		})
	}
}

func TestValidationContractDecisionBindings(t *testing.T) {
	for _, row := range []struct {
		name, decisionID, object, contains string
	}{
		{"decision empty id", "", "object-a", "id and object"},
		{"decision empty object", "decision", "", "id and object"},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			decision := validationContractDecisionWire(row.decisionID, DecisionAcceptNativeSQL, row.object, "", "", true, "approved")
			operation := validationContractDecisionOperationWire("native", OperationNativeSQL, []string{"object-a"}, []string{})
			data := validationContractDecisionPlan(t, []map[string]any{operation}, []map[string]any{decision}, nil, "object-a")
			validationContractDecodeError(t, data, ErrInvalidDecision, row.contains)
		})
	}

	validRename := validationContractDecisionWire("rename-decision", DecisionRenameObject, "object-a", "main.users", "main.accounts", true, "")
	renameBinding := map[string]any{"operation": "rename", "object": "object-a", "to_schema": "main", "to_name": "accounts"}
	for _, row := range []struct {
		name       string
		operations []map[string]any
		decisions  []map[string]any
		objects    []string
	}{
		{
			name:       "rename decision without operation",
			operations: []map[string]any{validationContractDecisionOperationWire("rename", OperationRenameTable, []string{"object-a"}, nil)},
			decisions: []map[string]any{
				validRename,
				validationContractDecisionWire("unused", DecisionRenameObject, "object-a", "main.accounts", "main.final", true, ""),
			},
			objects: []string{"object-a"},
		},
		{
			name: "rename decision for non-rename operation",
			operations: []map[string]any{
				validationContractDecisionOperationWire("rename", OperationRenameTable, []string{"object-a"}, nil),
				validationContractDecisionOperationWire("native", OperationNativeSQL, []string{"object-a"}, []string{"rename"}),
			},
			decisions: []map[string]any{
				validRename,
				validationContractDecisionWire("native-decision", DecisionAcceptNativeSQL, "object-a", "", "", true, "approved"),
				validationContractDecisionWire("unused", DecisionRenameObject, "object-a", "main.accounts", "main.final", true, ""),
			},
			objects: []string{"object-a"},
		},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			data := validationContractDecisionPlan(t, row.operations, row.decisions, []map[string]any{renameBinding}, row.objects...)
			validationContractDecodeError(t, data, ErrInvalidDecision, "unused")
		})
	}

	valid := validationContractValid(t)
	for _, row := range []struct {
		name, contains string
		mutate         func(map[string]any)
	}{
		{"missing destructive decision", "lacks destructive decision", func(root map[string]any) {
			root["decisions"] = validationContractWithoutDecision(root["decisions"].([]any), "destructive")
		}},
		{"wrong-object destructive decision", "lacks destructive decision", func(root map[string]any) { validationContractDecisionByID(root, "destructive")["object"] = "other" }},
		{"missing backfill decision", "lacks backfill decision", func(root map[string]any) {
			root["decisions"] = validationContractWithoutDecision(root["decisions"].([]any), "backfill")
		}},
		{"wrong-object backfill decision", "lacks backfill decision", func(root map[string]any) { validationContractDecisionByID(root, "backfill")["object"] = "other" }},
		{"destructive decision cannot approve backfill", "lacks backfill decision", func(root map[string]any) {
			validationContractDecisionByID(root, "backfill")["kind"] = "accept_destructive"
		}},
		{"missing native decision", "lacks native SQL decision", func(root map[string]any) {
			root["decisions"] = validationContractWithoutDecision(root["decisions"].([]any), "native")
		}},
		{"wrong-object native decision", "lacks native SQL decision", func(root map[string]any) { validationContractDecisionByID(root, "native")["object"] = "other" }},
		{"backfill decision cannot approve native", "lacks native SQL decision", func(root map[string]any) { validationContractDecisionByID(root, "native")["kind"] = "supply_backfill" }},
		{"duplicate decision id", "duplicate decision ID", func(root map[string]any) {
			decisions := root["decisions"].([]any)
			decisions[1].(map[string]any)["id"] = decisions[0].(map[string]any)["id"]
		}},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			root := validationContractJSON(t, valid).(map[string]any)
			row.mutate(root)
			validationContractDecodeError(t, validationContractRehash(t, validationContractBytes(t, root)), ErrInvalidDecision, row.contains)
		})
	}

	for _, kind := range []OperationKind{OperationDropTable, OperationDropColumn, OperationDropIndex, OperationDropConstraint} {
		kind := kind
		for _, wrongObject := range []bool{false, true} {
			name := string(kind) + " missing destructive decision"
			if wrongObject {
				name = string(kind) + " wrong-object destructive decision"
			}
			t.Run(name, func(t *testing.T) {
				operationID := string(kind) + "-binding"
				object := "object-a"
				decisionObject := object
				if wrongObject {
					decisionObject = "object-b"
				}
				decisions := []map[string]any{}
				if wrongObject {
					decisions = append(decisions, validationContractDecisionWire("destructive", DecisionAcceptDestructive, decisionObject, "", "", true, "approved"))
				}
				operation := validationContractDecisionOperationWire(operationID, kind, []string{object}, nil)
				data := validationContractDecisionPlan(t, []map[string]any{operation}, decisions, nil, object)
				require.Contains(t, string(data), operationID)
				_, err := Decode(data)
				require.ErrorIs(t, err, ErrInvalidDecision)
				require.ErrorContains(t, err, "lacks destructive decision")
				require.ErrorContains(t, err, operationID)
			})
		}
	}

	for _, row := range []struct {
		name, operationID, decisionKind, contains string
		kind                                      OperationKind
	}{
		{"two-object drop only first approved", "drop-two", string(DecisionAcceptDestructive), "lacks destructive decision", OperationDropTable},
		{"two-object drop only second approved", "drop-two-second", string(DecisionAcceptDestructive), "lacks destructive decision", OperationDropTable},
		{"two-object backfill only first supplied", "backfill-two", string(DecisionSupplyBackfill), "lacks backfill decision", OperationBackfill},
		{"two-object native only first approved", "native-two", string(DecisionAcceptNativeSQL), "lacks native SQL decision", OperationNativeSQL},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			decisionObject := "object-a"
			unapproved := "object-b"
			if strings.Contains(row.name, "second") {
				decisionObject, unapproved = "object-b", "object-a"
			}
			operation := validationContractDecisionOperationWire(row.operationID, row.kind, []string{"object-a", "object-b"}, nil)
			decision := validationContractDecisionWire("policy", DecisionKind(row.decisionKind), decisionObject, "", "", true, "approved")
			data := validationContractDecisionPlan(t, []map[string]any{operation}, []map[string]any{decision}, nil, "object-a", "object-b")
			require.Contains(t, string(data), unapproved)
			_, err := Decode(data)
			require.ErrorIs(t, err, ErrInvalidDecision)
			require.ErrorContains(t, err, row.contains)
			require.ErrorContains(t, err, row.operationID)
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

func validationContractDecisionWire(id string, kind DecisionKind, object, from, to string, accepted bool, reason string) map[string]any {
	return map[string]any{
		"id": id, "kind": string(kind), "object": object, "from": from, "to": to,
		"accepted": accepted, "reason": reason,
	}
}

func validationContractDecisionOperationWire(id string, kind OperationKind, objects, dependsOn []string) map[string]any {
	if dependsOn == nil {
		dependsOn = []string{}
	}
	sqlText := "DROP TABLE users"
	if kind == OperationRenameTable {
		sqlText = "ALTER TABLE users RENAME TO accounts"
	}
	if kind == OperationBackfill || kind == OperationNativeSQL {
		sqlText = "SELECT 1"
	}
	return map[string]any{
		"id": id, "kind": string(kind), "depends_on": dependsOn, "objects": objects,
		"preconditions": []any{}, "postconditions": []any{}, "result_digest": digestHex(Digest{1}),
		"statements":  []any{map[string]any{"sql": sqlText, "args": []any{}}},
		"transaction": string(TransactionForbidden), "reversible": false, "reverse_statements": []any{},
	}
}

func validationContractDecisionPlan(t *testing.T, operations []map[string]any, decisions []map[string]any, renames []map[string]any, objectIDs ...string) []byte {
	t.Helper()
	if renames == nil {
		renames = []map[string]any{}
	}
	root := validationContractJSON(t, validationContractValid(t)).(map[string]any)
	objects := make([]any, 0, len(objectIDs))
	for i, id := range objectIDs {
		name := "users"
		if i > 0 {
			name = "orders"
		}
		objects = append(objects, map[string]any{
			"id": id, "kind": "table", "schema": "main", "name": name, "introduced_by": "",
		})
	}
	baseline := root["baseline"].(map[string]any)
	baseline["objects"] = objects
	baseline["renames"] = renames
	root["decisions"] = decisions
	root["operations"] = operations
	return validationContractRehash(t, validationContractBytes(t, root))
}

func validationContractOperation(t *testing.T, kind OperationKind, objects []ObjectID, statements []stmt.Statement, reverse []stmt.Statement, reversible bool, args ...any) (Operation, error) {
	t.Helper()
	if statements == nil && args != nil {
		statements = []stmt.Statement{stmt.New(sqltext.Text("SELECT ?"), args...)}
	}
	return NewOperation("validation-operation", kind, nil, objects, nil, nil, Digest{1}, statements, TransactionForbidden, reversible, reverse)
}

func validationContractDependencyOperation(t *testing.T, id string, dependsOn []OperationID) Operation {
	t.Helper()
	operation, err := NewOperation(OperationID(id), OperationNativeSQL, dependsOn, []ObjectID{"object"}, nil, nil,
		Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, TransactionForbidden, false, nil)
	require.NoError(t, err)
	return operation
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
	t.Run("operation unknown kind", func(t *testing.T) {
		_, err := NewOperation("unknown-kind", OperationKind("unknown"), nil, []ObjectID{"object"}, nil, nil,
			Digest{1}, validSQL, TransactionForbidden, false, nil)
		require.ErrorIs(t, err, ErrInvalidOperation)
		require.ErrorContains(t, err, "unknown kind or transaction")
	})
	t.Run("operation unknown transaction", func(t *testing.T) {
		_, err := NewOperation("unknown-transaction", OperationAddColumn, nil, []ObjectID{"object"}, nil, nil,
			Digest{1}, validSQL, TransactionMode("unknown"), false, nil)
		require.ErrorIs(t, err, ErrInvalidOperation)
		require.ErrorContains(t, err, "unknown kind or transaction")
	})
	t.Run("constructor self dependency", func(t *testing.T) {
		_, err := NewOperation("self", OperationNativeSQL, []OperationID{"self"}, []ObjectID{"object"}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, TransactionForbidden, false, nil)
		require.ErrorIs(t, err, ErrInvalidOperation)
		require.ErrorContains(t, err, "depends on itself")
	})
	t.Run("irreversible empty reverse succeeds", func(t *testing.T) {
		operation, err := NewOperation("irreversible-empty-reverse", OperationNativeSQL, nil, []ObjectID{"object"}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, TransactionForbidden, false, []stmt.Statement{})
		require.NoError(t, err)
		require.Empty(t, operation.ReverseStatements())
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
	t.Run("rename table two objects plan remains aggregate", func(t *testing.T) {
		profile, identity, object, history := validationContractIdentity(t)
		rename, err := NewOperation("rename-plan", OperationRenameTable, nil, []ObjectID{object.ID()}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("ALTER TABLE users RENAME TO renamed"))}, TransactionForbidden, false, nil)
		require.NoError(t, err)
		rename.objects = []ObjectID{object.ID(), "other"}
		binding, err := NewBaselineRename("rename-plan", object.ID(), "main", "renamed")
		require.NoError(t, err)
		baseline, err := NewBaselineIdentity(identity, "validation", []BaselineObject{object}, []BaselineRename{binding})
		require.NoError(t, err)
		_, err = newPlan(profile, baseline, history, nil, []Operation{rename})
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "rename binding mismatch")
	})
	for _, kind := range []OperationKind{OperationAddColumn, OperationBackfill, OperationNativeSQL} {
		kind := kind
		t.Run(string(kind)+" nil forward statements", func(t *testing.T) {
			_, err := validationContractOperation(t, kind, []ObjectID{"object"}, nil, nil, false)
			require.ErrorIs(t, err, ErrInvalidOperation)
			require.ErrorContains(t, err, "needs forward SQL")
		})
		t.Run(string(kind)+" empty forward statements", func(t *testing.T) {
			_, err := validationContractOperation(t, kind, []ObjectID{"object"}, []stmt.Statement{}, nil, false)
			require.ErrorIs(t, err, ErrInvalidOperation)
			require.ErrorContains(t, err, "needs forward SQL")
		})
	}
	for _, kind := range []OperationKind{OperationAddColumn, OperationBackfill, OperationNativeSQL} {
		kind := kind
		for _, sqlText := range []string{"", "  "} {
			name := string(kind) + " empty SQL"
			if sqlText != "" {
				name = string(kind) + " whitespace SQL"
			}
			t.Run(name, func(t *testing.T) {
				_, err := validationContractOperation(t, kind, []ObjectID{"object"}, []stmt.Statement{stmt.New(sqltext.Text(sqlText))}, nil, false)
				require.ErrorIs(t, err, ErrInvalidOperation)
				require.ErrorContains(t, err, "statement SQL is empty")
			})
		}
	}
	for _, sqlText := range []string{"", "  "} {
		name := "reversible reverse empty SQL"
		if sqlText != "" {
			name = "reversible reverse whitespace SQL"
		}
		t.Run(name, func(t *testing.T) {
			_, err := validationContractOperation(t, OperationNativeSQL, []ObjectID{"object"},
				[]stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))},
				[]stmt.Statement{stmt.New(sqltext.Text(sqlText))}, true)
			require.ErrorIs(t, err, ErrInvalidOperation)
			require.ErrorContains(t, err, "statement SQL is empty")
		})
	}
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

type validationContractBaselineFixture struct {
	profile       Profile
	identity      CatalogIdentity
	source        string
	starting      []BaselineObject
	future        []BaselineObject
	objects       []BaselineObject
	creates       []Operation
	renameRecords []BaselineRename
	renames       []Operation
	decisions     []Decision
	history       HistoryIdentity
	plan          Plan
}

func newValidationContractBaselineFixture(t *testing.T) validationContractBaselineFixture {
	t.Helper()
	profile := sourceRepairProfile(t)
	profileDigest, err := ProfileDigest(profile)
	require.NoError(t, err)
	identity, err := NewCatalogIdentity(profile.Engine(), profileDigest, Digest{1}, Digest{2})
	require.NoError(t, err)
	source := "baseline-validation-source"
	starting := []BaselineObject{
		validationContractBaselineObject(t, "starting-a", "main", "users"),
		validationContractBaselineObject(t, "starting-b", "main", "orders"),
	}
	future := make([]BaselineObject, 0, 2)
	for _, row := range []struct{ operation, schemaName, name string }{
		{"create-a", "main", "created_a"}, {"create-b", "main", "created_b"},
	} {
		object, objectErr := NewIntroducedBaselineObject(source, OperationID(row.operation), schema.TableDef{
			Schema: row.schemaName, Name: row.name,
			Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
		})
		require.NoError(t, objectErr)
		future = append(future, object)
	}
	objects := append(append([]BaselineObject(nil), starting...), future...)
	creates := make([]Operation, 0, len(future))
	for i, object := range future {
		operationID := OperationID("create-" + string(rune('a'+i)))
		operation, operationErr := NewOperation(operationID, OperationCreateTable, nil, []ObjectID{object.ID()}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text("CREATE TABLE " + object.Name()))},
			TransactionRequired, false, nil)
		require.NoError(t, operationErr)
		creates = append(creates, operation)
	}
	renameRecords := []BaselineRename{}
	renames := []Operation{}
	decisions := []Decision{}
	for i, object := range starting {
		operationID := OperationID("rename-" + string(rune('a'+i)))
		toName := []string{"accounts", "purchases"}[i]
		record, recordErr := NewBaselineRename(operationID, object.ID(), "main", toName)
		require.NoError(t, recordErr)
		renameRecords = append(renameRecords, record)
		renameSQL := "ALTER TABLE " + object.Name() + " RENAME TO " + toName
		dependsOn := []OperationID(nil)
		if i == 1 {
			dependsOn = []OperationID{"rename-a"}
		}
		rename, renameErr := NewOperation(operationID, OperationRenameTable, dependsOn, []ObjectID{object.ID()}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text(renameSQL))}, TransactionEngineDefault, false, nil)
		require.NoError(t, renameErr)
		renames = append(renames, rename)
		decision, decisionErr := NewDecision(DecisionID("decision-"+string(rune('a'+i))), DecisionRenameObject, object.ID(),
			"main."+object.Name(), "main."+toName, true, "")
		require.NoError(t, decisionErr)
		decisions = append(decisions, decision)
	}
	baseline, err := NewBaselineIdentity(identity, source, objects, renameRecords)
	require.NoError(t, err)
	history, err := NewHistoryIdentity("main", "schema_migrations")
	require.NoError(t, err)
	operations := append(append([]Operation(nil), creates...), renames...)
	plan, err := newPlan(profile, baseline, history, decisions, operations)
	require.NoError(t, err)
	return validationContractBaselineFixture{
		profile: profile, identity: identity, source: source, starting: starting, future: future, objects: objects,
		creates: creates, renameRecords: renameRecords, renames: renames, decisions: decisions, history: history, plan: plan,
	}
}

func validationContractIdentityWithEngine(identity CatalogIdentity, engine EngineID) CatalogIdentity {
	identity.engine = engine
	return identity
}

func TestValidationContractBaselineSemantics(t *testing.T) {
	fixture := newValidationContractBaselineFixture(t)
	allObjects := func() []BaselineObject { return append([]BaselineObject(nil), fixture.objects...) }
	engineZero := validationContractIdentityWithEngine(fixture.identity, 0)
	engineAboveCustom := validationContractIdentityWithEngine(fixture.identity, CustomEngine+1)
	for _, row := range []struct {
		name, id, kind, objectName, schemaName string
		valid                                  bool
	}{
		{"constructor baseline object blank id", "", "table", "users", "main", false},
		{"constructor baseline object blank kind", "object", "", "users", "main", false},
		{"constructor baseline object blank name", "object", "table", "", "main", false},
		{"constructor baseline object blank schema succeeds", "object", "table", "users", "", true},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			object, err := NewBaselineObject(ObjectID(row.id), row.kind, row.schemaName, row.objectName)
			if row.valid {
				require.NoError(t, err)
				require.Equal(t, "", object.Schema())
				return
			}
			require.ErrorIs(t, err, ErrInvalidIdentity)
			require.ErrorContains(t, err, "baseline object fields are required")
		})
	}
	for _, row := range []struct {
		name     string
		identity CatalogIdentity
		source   string
		objects  func() []BaselineObject
		target   error
		contains string
	}{
		{
			"baseline catalog engine zero", engineZero, fixture.source,
			allObjects, ErrInvalidIdentity, "catalog or source identity is empty",
		},
		{
			"baseline catalog engine above custom", engineAboveCustom, fixture.source,
			allObjects, ErrInvalidIdentity, "catalog or source identity is empty",
		},
		{"baseline source empty", fixture.identity, "", allObjects, ErrInvalidIdentity, "source identity is required"},
		{
			"baseline source whitespace", fixture.identity, "   ", allObjects,
			ErrInvalidIdentity, "source identity is required",
		},
		{"baseline object blank id", fixture.identity, fixture.source, func() []BaselineObject {
			objects := allObjects()
			objects[0].id = ""
			return objects
		}, ErrInvalidIdentity, "incomplete baseline object"},
		{"baseline object blank kind", fixture.identity, fixture.source, func() []BaselineObject {
			objects := allObjects()
			objects[0].kind = ""
			return objects
		}, ErrInvalidIdentity, "incomplete baseline object"},
		{"baseline object blank name", fixture.identity, fixture.source, func() []BaselineObject {
			objects := allObjects()
			objects[0].name = ""
			return objects
		}, ErrInvalidIdentity, "incomplete baseline object"},
		{"baseline object blank schema remains valid", fixture.identity, fixture.source, func() []BaselineObject {
			objects := allObjects()
			objects[0].schema = ""
			return objects
		}, nil, ""},
		{"duplicate starting id", fixture.identity, fixture.source, func() []BaselineObject {
			objects := allObjects()
			objects[1].id = objects[0].id
			return objects
		}, ErrInvalidIdentity, "duplicate object ID"},
		{"duplicate starting and future id", fixture.identity, fixture.source, func() []BaselineObject {
			objects := allObjects()
			objects[2].id = objects[0].id
			return objects
		}, ErrInvalidIdentity, "duplicate object ID"},
		{"duplicate future id", fixture.identity, fixture.source, func() []BaselineObject {
			objects := allObjects()
			objects[3].id = objects[2].id
			return objects
		}, ErrInvalidIdentity, "duplicate object ID"},
		{"duplicate active qualified name", fixture.identity, fixture.source, func() []BaselineObject {
			objects := allObjects()
			objects[1].name = objects[0].name
			return objects
		}, ErrInvalidIdentity, "duplicate qualified name"},
		{"equal names different schemas succeeds", fixture.identity, fixture.source, func() []BaselineObject {
			objects := allObjects()
			objects[1].schema = "other"
			objects[1].name = objects[0].name
			return objects
		}, nil, ""},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			_, err := NewBaselineIdentity(row.identity, row.source, row.objects(), fixture.renameRecords)
			if row.target == nil {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, row.target)
			require.ErrorContains(t, err, row.contains)
		})
	}
}

func TestValidationContractBaselineRenameSemantics(t *testing.T) {
	fixture := newValidationContractBaselineFixture(t)
	for _, row := range []struct {
		name, operation  string
		object           ObjectID
		toSchema, toName string
		valid            bool
	}{
		{"rename constructor blank operation", "", fixture.starting[0].ID(), "main", "accounts", false},
		{"rename constructor blank object", "rename", "", "main", "accounts", false},
		{"rename constructor blank to name", "rename", fixture.starting[0].ID(), "main", "", false},
		{"rename constructor blank schema succeeds", "rename", fixture.starting[0].ID(), "", "accounts", true},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			_, err := NewBaselineRename(OperationID(row.operation), row.object, row.toSchema, row.toName)
			if row.valid {
				require.NoError(t, err)
				return
			}
			require.ErrorIs(t, err, ErrInvalidIdentity)
			require.ErrorContains(t, err, "baseline rename fields are required")
		})
	}
	t.Run("rename missing object", func(t *testing.T) {
		renames := append([]BaselineRename(nil), fixture.renameRecords...)
		renames[0].object = "missing"
		_, err := NewBaselineIdentity(fixture.identity, fixture.source, fixture.objects, renames)
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "rename object \"missing\" is absent")
	})
	t.Run("duplicate rename operation binding", func(t *testing.T) {
		renames := append([]BaselineRename(nil), fixture.renameRecords...)
		renames[1].operation = renames[0].operation
		_, err := NewBaselineIdentity(fixture.identity, fixture.source, fixture.objects, renames)
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "duplicate rename operation")
	})
	for _, row := range []struct {
		name   string
		mutate func(*BaselineIdentity, []Operation) []Operation
	}{
		{"rename binding missing operation", func(baseline *BaselineIdentity, operations []Operation) []Operation {
			baseline.renames[0].operation = "missing"
			return operations
		}},
		{"rename binding wrong kind", func(_ *BaselineIdentity, operations []Operation) []Operation {
			out := append([]Operation(nil), operations...)
			for i := range out {
				if out[i].ID() == "rename-a" {
					out[i].kind = OperationAddColumn
				}
			}
			return out
		}},
		{"rename binding multiple objects", func(_ *BaselineIdentity, operations []Operation) []Operation {
			out := append([]Operation(nil), operations...)
			for i := range out {
				if out[i].ID() == "rename-a" {
					out[i].objects = append(out[i].objects, "starting-b")
				}
			}
			return out
		}},
		{"rename binding wrong object", func(_ *BaselineIdentity, operations []Operation) []Operation {
			out := append([]Operation(nil), operations...)
			for i := range out {
				if out[i].ID() == "rename-a" {
					out[i].objects = []ObjectID{"starting-b"}
				}
			}
			return out
		}},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			baseline := fixture.plan.Baseline()
			operations := row.mutate(&baseline, fixture.plan.Operations())
			_, err := newPlan(fixture.profile, baseline, fixture.history, fixture.decisions, operations)
			require.ErrorIs(t, err, ErrInvalidIdentity)
			require.ErrorContains(t, err, "rename binding mismatch")
		})
	}
}

func TestValidationContractFutureBindingSemantics(t *testing.T) {
	fixture := newValidationContractBaselineFixture(t)
	// Future identity entry points remain covered by TestFutureIdentityRejectedAtNewPlan,
	// TestFutureIdentityRejectedAtEncode, TestFutureIdentityRejectedAtDecode,
	// TestFutureIdentityRejectedAtNewResolvedChanges, and TestFutureIdentityRejectedAtFromLock.
	for _, row := range []struct {
		name, contains string
		mutate         func(*BaselineIdentity, []Operation)
		target         error
	}{
		{
			"future missing create", "does not name its create_table operation",
			func(baseline *BaselineIdentity, _ []Operation) { baseline.objects[2].introducedBy = "missing-create" },
			ErrInvalidIdentity,
		},
		{
			"future wrong create kind", "does not name its create_table operation",
			func(baseline *BaselineIdentity, _ []Operation) { baseline.objects[2].introducedBy = "rename-a" },
			ErrInvalidIdentity,
		},
		{
			"future create has multiple objects", "create_table needs one object",
			func(_ *BaselineIdentity, operations []Operation) {
				operations[0].objects = append(operations[0].objects, "starting-a")
			},
			ErrInvalidOperation,
		},
		{
			"future create names other object", "does not name its create_table operation",
			func(baseline *BaselineIdentity, operations []Operation) {
				operations[0].objects[0] = baseline.objects[3].ID()
			},
			ErrInvalidIdentity,
		},
		{
			"create lacks future object", "does not name its create_table operation",
			func(_ *BaselineIdentity, operations []Operation) { operations[0].objects[0] = "starting-a" },
			ErrInvalidIdentity,
		},
		{
			"create future introduced_by differs", "does not name its create_table operation",
			func(baseline *BaselineIdentity, _ []Operation) { baseline.objects[2].introducedBy = "create-b" },
			ErrInvalidIdentity,
		},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			baseline := fixture.plan.Baseline()
			operations := fixture.plan.Operations()
			row.mutate(&baseline, operations)
			_, err := newPlan(fixture.profile, baseline, fixture.history, fixture.decisions, operations)
			require.ErrorIs(t, err, row.target)
			require.ErrorContains(t, err, row.contains)
			if row.name != "future create has multiple objects" {
				require.ErrorContains(t, err, string(fixture.future[0].ID()))
			}
		})
	}
}

type validationContractRenameDependencyFixture struct {
	profile    Profile
	baseline   BaselineIdentity
	history    HistoryIdentity
	operations []Operation
	decisions  []Decision
}

func newValidationContractRenameDependencyFixture(t *testing.T) validationContractRenameDependencyFixture {
	t.Helper()
	profile, identity, object, history := validationContractIdentity(t)
	records := []BaselineRename{}
	operations := []Operation{}
	decisions := []Decision{}
	for i, names := range [][2]string{{"accounts", "main.users"}, {"customers", "main.accounts"}} {
		operationID := OperationID("rename-chain-" + string(rune('a'+i)))
		record, err := NewBaselineRename(operationID, object.ID(), "main", names[0])
		require.NoError(t, err)
		records = append(records, record)
		dependsOn := []OperationID{}
		if i == 1 {
			dependsOn = []OperationID{"rename-chain-a"}
		}
		renameSQL := "ALTER TABLE users RENAME TO " + names[0]
		operation, err := NewOperation(operationID, OperationRenameTable, dependsOn, []ObjectID{object.ID()}, nil, nil,
			Digest{1}, []stmt.Statement{stmt.New(sqltext.Text(renameSQL))}, TransactionEngineDefault, false, nil)
		require.NoError(t, err)
		operations = append(operations, operation)
		decision, err := NewDecision(DecisionID("rename-chain-decision-"+string(rune('a'+i))), DecisionRenameObject,
			object.ID(), names[1], "main."+names[0], true, "")
		require.NoError(t, err)
		decisions = append(decisions, decision)
	}
	baseline, err := NewBaselineIdentity(identity, "validation", []BaselineObject{object}, records)
	require.NoError(t, err)
	return validationContractRenameDependencyFixture{
		profile: profile, baseline: baseline, history: history, operations: operations, decisions: decisions,
	}
}

func TestValidationContractRenameDependencySemantics(t *testing.T) {
	fixture := newValidationContractRenameDependencyFixture(t)
	t.Run("dependency ordered renames succeed", func(t *testing.T) {
		plan, err := newPlan(fixture.profile, fixture.baseline, fixture.history, fixture.decisions, fixture.operations)
		require.NoError(t, err)
		order, err := plan.StableOperationOrder()
		require.NoError(t, err)
		require.Equal(t, []OperationID{"rename-chain-a", "rename-chain-b"}, order)
	})
	for _, row := range []struct {
		name   string
		mutate func([]Operation) []Operation
	}{
		{"second rename lacks first dependency", func(operations []Operation) []Operation {
			out := append([]Operation(nil), operations...)
			out[1].dependsOn = []OperationID{}
			return out
		}},
		{"second rename depends on unrelated operation", func(operations []Operation) []Operation {
			out := append([]Operation(nil), operations...)
			unrelatedSQL := "ALTER TABLE users ADD COLUMN value TEXT"
			unrelated, err := NewOperation("unrelated", OperationAddColumn, nil, []ObjectID{"object"}, nil, nil,
				Digest{1}, []stmt.Statement{stmt.New(sqltext.Text(unrelatedSQL))}, TransactionForbidden, false, nil)
			require.NoError(t, err)
			out[1].dependsOn = []OperationID{"unrelated"}
			return append(out, unrelated)
		}},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			operations := row.mutate(fixture.operations)
			_, err := newPlan(fixture.profile, fixture.baseline, fixture.history, fixture.decisions, operations)
			require.ErrorIs(t, err, ErrInvalidIdentity)
			require.ErrorContains(t, err, "rename chain")
			require.ErrorContains(t, err, "not dependency ordered")
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
	t.Run("decode self dependency", func(t *testing.T) {
		root := validationContractJSON(t, validationContractValid(t)).(map[string]any)
		validationContractSet(root, []any{"native-sql"}, "operations", 10, "depends_on")
		validationContractDecodeError(t, validationContractRehash(t, validationContractBytes(t, root)), ErrInvalidOperation, "depends on itself")
	})
	t.Run("missing dependency later", func(t *testing.T) {
		first := operation
		first.id = "first-later-missing"
		first.dependsOn = []OperationID{"later-missing"}
		later := operation
		later.id = "later"
		_, err := newPlan(profile, baseline, history, []Decision{decision}, []Operation{first, later})
		require.ErrorIs(t, err, ErrInvalidOperation)
		require.ErrorContains(t, err, "missing dependency")
	})
	for _, row := range []struct {
		name       string
		operations []Operation
	}{
		{"two-node cycle", []Operation{
			validationContractDependencyOperation(t, "cycle-a", []OperationID{"cycle-b"}),
			validationContractDependencyOperation(t, "cycle-b", []OperationID{"cycle-a"}),
		}},
		{"three-node cycle", []Operation{
			validationContractDependencyOperation(t, "cycle-a", []OperationID{"cycle-b"}),
			validationContractDependencyOperation(t, "cycle-b", []OperationID{"cycle-c"}),
			validationContractDependencyOperation(t, "cycle-c", []OperationID{"cycle-a"}),
		}},
	} {
		row := row
		t.Run(row.name, func(t *testing.T) {
			_, err := newPlan(profile, baseline, history, []Decision{decision}, row.operations)
			require.ErrorIs(t, err, ErrDependencyCycle)
		})
	}
	t.Run("later-declared dependency succeeds", func(t *testing.T) {
		first, err := NewOperation("first", OperationNativeSQL, nil, []ObjectID{"object"}, nil, nil, Digest{2}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 1"))}, TransactionForbidden, false, nil)
		require.NoError(t, err)
		second, err := NewOperation("second", OperationNativeSQL, []OperationID{"first"}, []ObjectID{"object"}, nil, nil, Digest{3}, []stmt.Statement{stmt.New(sqltext.Text("SELECT 2"))}, TransactionForbidden, false, nil)
		require.NoError(t, err)
		plan, err := newPlan(profile, baseline, history, []Decision{decision}, []Operation{second, first})
		require.NoError(t, err)
		order, err := plan.StableOperationOrder()
		require.NoError(t, err)
		require.Equal(t, []OperationID{"first", "second"}, order)
	})
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
	t.Run("empty history table", func(t *testing.T) {
		_, err := NewHistoryIdentity("main", "")
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "history table is required")
	})
	t.Run("blank history table", func(t *testing.T) {
		_, err := NewHistoryIdentity("main", "   ")
		require.ErrorIs(t, err, ErrInvalidIdentity)
		require.ErrorContains(t, err, "history table is required")
	})
	t.Run("encode zero plan id", func(t *testing.T) {
		_, err := Encode(Plan{})
		require.ErrorIs(t, err, ErrInvalidPlan)
	})
	t.Run("encode stale plan id", func(t *testing.T) {
		plan, err := newPlan(profile, baseline, history, []Decision{decision}, []Operation{operation})
		require.NoError(t, err)
		plan.operations[0].resultDigest = Digest{8}
		_, err = Encode(plan)
		require.ErrorIs(t, err, ErrInvalidPlan)
	})

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
