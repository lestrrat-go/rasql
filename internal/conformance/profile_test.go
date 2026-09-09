package conformance

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/stretchr/testify/require"
)

type profileCase struct {
	id                    string
	major, minor, maxBind int
	dialect               dialect.Dialect
	engine                string
	returning             bool
}

type profileArgument struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}

type profileStatement struct {
	Operation string            `json:"operation"`
	Kind      string            `json:"kind"`
	SQL       string            `json:"sql"`
	Args      []profileArgument `json:"args"`
}

type profileCompileRender struct {
	Digest     string             `json:"digest"`
	Statements []profileStatement `json:"statements"`
}

type profileManifest struct {
	Engine                  string                          `json:"engine"`
	Profile                 string                          `json:"profile"`
	PortableSignatureDigest string                          `json:"portable_signature_digest"`
	LockSHA256              string                          `json:"lock_sha256"`
	CompileRender           map[string]profileCompileRender `json:"compile_render"`
}

func TestCompileRenderProfiles(t *testing.T) {
	cases := []profileCase{
		{id: "postgresql-16", major: 16, maxBind: 65535, dialect: dialect.PostgreSQL(), engine: "postgresql", returning: true},
		{id: "postgresql-17", major: 17, maxBind: 65535, dialect: dialect.PostgreSQL(), engine: "postgresql", returning: true},
		{id: "mysql-8.4", major: 8, minor: 4, maxBind: 65535, dialect: dialect.MySQL(), engine: "mysql"},
		{id: "sqlite-3.35", major: 3, minor: 35, maxBind: 999, dialect: dialect.SQLite(), engine: "sqlite", returning: true},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			manifest := readProfileManifest(t, tc.engine)
			reviewed, ok := manifest.CompileRender[tc.id]
			require.True(t, ok, tc.id)
			require.Equal(t, strings.SplitN(tc.id, "-", 2)[0], manifest.Engine)
			digest, err := PortableSignatureDigestChecked()
			require.NoError(t, err)
			require.Equal(t, digest, manifest.PortableSignatureDigest)
			profile, err := rasql.EngineProfileFromVersion(tc.id, tc.major, tc.minor, 0)
			require.NoError(t, err)
			require.Equal(t, tc.id, profile.ID())
			require.Equal(t, tc.maxBind, profile.Limits().MaxBindParameters)
			require.True(t, profile.Capabilities().WindowFunctions)
			require.Equal(t, rasql.EnginePerParentLimitWindow, profile.Capabilities().PerParentLimit)

			records := filepath.Join(t.TempDir(), "compile-render.json")
			require.NoError(t, os.MkdirAll(filepath.Dir(records), 0o755))
			runGeneratedProfile(t, tc, records)
			runGeneratedCardinalityProfile(t, tc)
			data, err := os.ReadFile(records)
			require.NoError(t, err)
			var actual profileCompileRender
			require.NoError(t, json.Unmarshal(data, &actual))
			require.NoError(t, validateCompileRender(actual, reviewed))
			if tc.engine == "postgresql" {
				postgres16, ok := manifest.CompileRender["postgresql-16"]
				require.True(t, ok)
				postgres17, ok := manifest.CompileRender["postgresql-17"]
				require.True(t, ok)
				require.Equal(t, postgres16, postgres17)
			}
		})
	}
}

func runGeneratedCardinalityProfile(t *testing.T, tc profileCase) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	moduleRoot := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, copyGeneratedStore(filepath.Join("testdata", tc.engine, "internal", "store"), filepath.Join(moduleRoot, "internal", "store")))
	for _, declaration := range []struct{ file, marker string }{{"overdue_task_gen.go", "rasql.ExactlyOne"}, {"maybe_overdue_task_gen.go", "rasql.AtMostOne"}, {"overdue_tasks_gen.go", "rasql.Many"}} {
		source, readErr := os.ReadFile(filepath.Join("testdata", tc.engine, "internal", "store", declaration.file))
		require.NoError(t, readErr)
		require.Contains(t, string(source), declaration.marker)
	}
	driverSource, err := os.ReadFile("recording_driver_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, "recording_driver_test.go"), driverSource, 0o600))
	modulePath := "example.test/cardinality/" + tc.engine
	goMod := fmt.Sprintf("module %s\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nrequire %s\n\nreplace github.com/lestrrat-go/rasql => %s\n", modulePath, pinnedRequire(t, repoRoot, "github.com/stretchr/testify"), filepath.ToSlash(repoRoot))
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte(goMod), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, "cardinality_profile_test.go"), []byte(generatedCardinalityProfileTest(tc, modulePath)), 0o600))
	command := exec.Command("go", "test", "-run", "^TestGeneratedCardinalityProfile$")
	command.Dir = moduleRoot
	command.Env = offlineBuildEnv(t.TempDir())
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func generatedCardinalityProfileTest(tc profileCase, modulePath string) string {
	dialectName := "SQLite"
	if tc.engine == "postgresql" {
		dialectName = "PostgreSQL"
	}
	if tc.engine == "mysql" {
		dialectName = "MySQL"
	}
	querySQL := "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at\nFROM tasks\nWHERE project_id = ?\n  AND is_open = ?\n  AND due_on IS NOT NULL\n  AND due_on < ?\nORDER BY id\n"
	if tc.engine == "postgresql" {
		querySQL = "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at\nFROM tasks\nWHERE project_id = $1\n  AND is_open = $2\n  AND due_on IS NOT NULL\n  AND due_on < $3\nORDER BY id\n"
	}
	return fmt.Sprintf(`package conformance

import (
	"database/sql/driver"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"%s/internal/store"
)

func TestGeneratedCardinalityProfile(t *testing.T) {
	due := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	cutoff := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)
	columns := []string{"id", "project_id", "assignee_id", "title", "is_open", "due_on", "created_at"}
	rowOne := []driver.Value{int64(1), int64(1), int64(1), "task-0001", true, time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC), created}
	rowTwo := []driver.Value{int64(3), int64(1), int64(3), "task-0003", true, due, created}
	response := func(rows [][]driver.Value) recordingResponse { return recordingResponse{Kind: "query", SQL: %q, Args: []any{int64(1), true, cutoff}, Columns: columns, Rows: rows} }
	state := &recordingDriverState{strict: true, responses: []recordingResponse{
		response(nil), response([][]driver.Value{rowOne, rowTwo}), response([][]driver.Value{rowOne}),
		response(nil), response([][]driver.Value{rowOne, rowTwo}), response([][]driver.Value{rowOne}),
		response(nil), response([][]driver.Value{rowOne, rowTwo}), response([][]driver.Value{rowOne}),
	}}
	db := openRecordingDB(state)
	defer db.Close()
	raw, err := rasql.New(db, dialect.%s())
	if err != nil { t.Fatal(err) }
	profile, err := rasql.EngineProfileFromVersion("%s", %d, %d, 0)
	if err != nil { t.Fatal(err) }
	executor, err := rasql.AsExecutor(raw, profile)
	if err != nil { t.Fatal(err) }
	one, err := store.OverdueTask(1, true, cutoff); if err != nil { t.Fatal(err) }
	assertSchema(t, one.Schema())
	if rows, allErr := rasql.All(t.Context(), executor, one); !errors.Is(allErr, rasql.ErrNoRows) || len(rows) != 0 { t.Fatalf("one all zero: %%v %%d", allErr, len(rows)) }
	one, err = store.OverdueTask(1, true, cutoff); if err != nil { t.Fatal(err) }
	if rows, allErr := rasql.All(t.Context(), executor, one); !errors.Is(allErr, rasql.ErrMultipleRows) || len(rows) != 0 { t.Fatalf("one all two: %%v %%d", allErr, len(rows)) }
	one, err = store.OverdueTask(1, true, cutoff); if err != nil { t.Fatal(err) }
	if row, oneErr := rasql.One(t.Context(), executor, one); oneErr != nil { t.Fatal(oneErr) } else { assertProfileRow(t, row, 1) }
	maybe, err := store.MaybeOverdueTask(1, true, cutoff); if err != nil { t.Fatal(err) }
	assertSchema(t, maybe.Schema())
	if rows, maybeErr := rasql.All(t.Context(), executor, maybe); maybeErr != nil || len(rows) != 0 { t.Fatalf("maybe all zero: %%v %%d", maybeErr, len(rows)) }
	maybe, err = store.MaybeOverdueTask(1, true, cutoff); if err != nil { t.Fatal(err) }
	if rows, maybeErr := rasql.All(t.Context(), executor, maybe); !errors.Is(maybeErr, rasql.ErrMultipleRows) || len(rows) != 0 { t.Fatalf("maybe all two: %%v %%d", maybeErr, len(rows)) }
	maybe, err = store.MaybeOverdueTask(1, true, cutoff); if err != nil { t.Fatal(err) }
	if row, found, maybeErr := rasql.Maybe(t.Context(), executor, maybe); maybeErr != nil || !found { t.Fatalf("maybe: %%v %%t", maybeErr, found) } else { assertProfileRow(t, row, 1) }
	many, err := store.OverdueTasks(1, true, cutoff); if err != nil { t.Fatal(err) }
	assertSchema(t, many.Schema())
	if rows, manyErr := rasql.All(t.Context(), executor, many); manyErr != nil || len(rows) != 0 { t.Fatalf("many all zero: %%v %%d", manyErr, len(rows)) }
	many, err = store.OverdueTasks(1, true, cutoff); if err != nil { t.Fatal(err) }
	if rows, manyErr := rasql.All(t.Context(), executor, many); manyErr != nil || len(rows) != 2 { t.Fatalf("many all two: %%v %%d", manyErr, len(rows)) } else { assertProfileRow(t, rows[0], 1); assertProfileRow(t, rows[1], 3) }
	many, err = store.OverdueTasks(1, true, cutoff); if err != nil { t.Fatal(err) }
	if rows, manyErr := rasql.All(t.Context(), executor, many); manyErr != nil || len(rows) != 1 { t.Fatalf("many one: %%v %%d", manyErr, len(rows)) } else { assertProfileRow(t, rows[0], 1) }
	state.AssertDrained(t)
	calls := state.Calls()
	if len(calls) != 9 { t.Fatalf("calls = %%d", len(calls)) }
	for _, call := range calls {
		if len(call.args) != 3 || !reflect.DeepEqual(call.args, []any{int64(1), true, cutoff}) { t.Fatalf("typed args = %%#v", call.args) }
		if call.sql != %q { t.Fatalf("SQL = %%q", call.sql) }
	}
}

func assertSchema(t *testing.T, resultSchema rasql.ResultSchema) {
	t.Helper()
	columns := resultSchema.Columns()
	wantNames := []string{"id", "project_id", "assignee_id", "title", "is_open", "due_on", "created_at"}
	wantNullable := []bool{false, false, true, false, false, true, false}
	wantTypes := []any{schema.IntegerType{}, schema.IntegerType{}, schema.IntegerType{}, schema.TextType{}, schema.BooleanType{}, schema.TimeType{}, schema.TimeType{}}
	if len(columns) != len(wantNames) { t.Fatalf("schema columns = %%d", len(columns)) }
	for index, column := range columns {
		if column.Name != wantNames[index] || column.Nullable != wantNullable[index] || column.Codec != "" || !reflect.DeepEqual(column.Type, wantTypes[index]) { t.Fatalf("schema column[%%d] = %%#v", index, column) }
	}
}

func assertProfileRow(t *testing.T, row any, id int64) {
	t.Helper(); value := reflect.ValueOf(row)
	wantDue := time.Date(2024, 1, int(id), 0, 0, 0, 0, time.UTC)
	if value.FieldByName("ID").Interface() != id || value.FieldByName("ProjectID").Interface() != int64(1) { t.Fatalf("row keys = %%#v", row) }
	if !reflect.DeepEqual(value.FieldByName("AssigneeID").Interface(), rasql.Nullable[int64]{Value: id, Valid: true}) || value.FieldByName("Title").Interface() != fmt.Sprintf("task-%%04d", id) { t.Fatalf("row identity = %%#v", row) }
	if value.FieldByName("IsOpen").Interface() != true || !reflect.DeepEqual(value.FieldByName("DueOn").Interface(), rasql.Nullable[time.Time]{Value: wantDue, Valid: true}) { t.Fatalf("row due = %%#v", row) }
	if value.FieldByName("CreatedAt").Interface() != time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC) { t.Fatalf("row created = %%#v", row) }
}
`, modulePath, querySQL, dialectName, tc.id, tc.major, tc.minor, querySQL)
}

func readProfileManifest(t *testing.T, engine string) profileManifest {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", engine, "d4-manifest.json"))
	require.NoError(t, err)
	var envelope struct {
		Engine                  string          `json:"engine"`
		Profile                 string          `json:"profile"`
		PortableSignatureDigest string          `json:"portable_signature_digest"`
		CompileRender           json.RawMessage `json:"compile_render"`
	}
	require.NoError(t, json.Unmarshal(data, &envelope))
	manifest := profileManifest{Engine: envelope.Engine, Profile: envelope.Profile, PortableSignatureDigest: envelope.PortableSignatureDigest}
	if len(envelope.CompileRender) > 0 && envelope.CompileRender[0] == '[' {
		var legacy []struct {
			Kind string `json:"kind"`
			SQL  string `json:"sql"`
			Args []struct {
				Type  string `json:"type"`
				Value any    `json:"value"`
			} `json:"args"`
		}
		require.NoError(t, json.Unmarshal(envelope.CompileRender, &legacy))
		statements := make([]profileStatement, len(legacy))
		for index, value := range legacy {
			args := make([]profileArgument, len(value.Args))
			for argIndex, arg := range value.Args {
				args[argIndex] = profileArgument{Type: arg.Type, Value: fmt.Sprint(arg.Value)}
			}
			statements[index] = profileStatement{Kind: value.Kind, SQL: value.SQL, Args: args}
		}
		manifest.CompileRender = map[string]profileCompileRender{envelope.Profile: {Statements: statements}}
		return manifest
	}
	require.NoError(t, json.Unmarshal(envelope.CompileRender, &manifest.CompileRender))
	return manifest
}

func validateCompileRender(actual, expected profileCompileRender) error {
	if !reflect.DeepEqual(actual.Statements, expected.Statements) {
		return errors.New("compile_render statements differ")
	}
	if actual.Digest != expected.Digest {
		return errors.New("compile_render digest differs")
	}
	if profileInvocationDigest(actual.Statements) != actual.Digest {
		return errors.New("compile_render digest is invalid")
	}
	return nil
}

func profileInvocationDigest(records []profileStatement) string {
	parts := make([]string, 0, len(records)*4)
	for _, record := range records {
		parts = append(parts, record.Operation, record.Kind, record.SQL, fmt.Sprint(len(record.Args)))
		for _, arg := range record.Args {
			parts = append(parts, arg.Type, arg.Value)
		}
	}
	return DigestParts(parts...)
}

func TestCompileRenderManifestMutations(t *testing.T) {
	valid := profileCompileRender{Statements: []profileStatement{
		{Operation: "single_row_read", Kind: "query", SQL: "SELECT 1", Args: []profileArgument{{Type: "int64", Value: "1"}}},
		{Operation: "graph_root", Kind: "query", SQL: "SELECT 2", Args: []profileArgument{{Type: "int64", Value: "2"}}},
	}}
	valid.Digest = profileInvocationDigest(valid.Statements)
	cases := []struct {
		name   string
		mutate func(*profileCompileRender)
	}{
		{name: "sql byte", mutate: func(value *profileCompileRender) { value.Statements[0].SQL = "SELECT 2" }},
		{name: "statement order", mutate: func(value *profileCompileRender) {
			value.Statements[0], value.Statements[1] = value.Statements[1], value.Statements[0]
		}},
		{name: "argument", mutate: func(value *profileCompileRender) { value.Statements[0].Args[0].Value = "2" }},
		{name: "digest", mutate: func(value *profileCompileRender) { value.Digest = strings.Repeat("0", 64) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mutated := valid
			mutated.Statements = append([]profileStatement(nil), valid.Statements...)
			mutated.Statements[0].Args = append([]profileArgument(nil), valid.Statements[0].Args...)
			mutated.Statements[1].Args = append([]profileArgument(nil), valid.Statements[1].Args...)
			tc.mutate(&mutated)
			require.Error(t, validateCompileRender(mutated, valid))
		})
	}
}

func TestGeneratedQueryProvenance(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			root := filepath.Join("testdata", engine)
			lockBytes, err := os.ReadFile(filepath.Join(root, "rasql.lock.json"))
			require.NoError(t, err)
			lock, err := compilerlock.Decode(lockBytes)
			require.NoError(t, err)
			require.Len(t, lock.Queries, 3)
			manifestLock, err := os.ReadFile(filepath.Join(root, "d4-manifest.json"))
			require.NoError(t, err)
			var manifest profileManifest
			require.NoError(t, json.Unmarshal(manifestLock, &manifest))
			lockDigest := sha256.Sum256(lockBytes)
			require.Equal(t, hex.EncodeToString(lockDigest[:]), manifest.LockSHA256)
			require.Equal(t, "store", lock.Generation.Package)
			require.Equal(t, "internal/store", lock.Generation.Output)
			require.Equal(t, "compact", lock.Generation.Emitter)
			require.True(t, lock.Generation.Prune)
			require.Equal(t, []compilerlock.QueryNameRecord{
				{ID: "maybe_overdue_task", Function: "MaybeOverdueTask", Result: "maybe_overdue_taskResult", Projection: "maybe_overdue_taskProjection", Decoder: "maybe_overdue_taskDecoder", File: "maybe_overdue_task_gen.go"},
				{ID: "overdue_task", Function: "OverdueTask", Result: "overdue_taskResult", Projection: "overdue_taskProjection", Decoder: "overdue_taskDecoder", File: "overdue_task_gen.go"},
				{ID: "overdue_tasks", Function: "OverdueTasks", Result: "overdue_tasksResult", Projection: "overdue_tasksProjection", Decoder: "overdue_tasksDecoder", File: "overdue_tasks_gen.go"},
			}, lock.Generation.Queries)
			wantParameters, wantResults := expectedProvenanceValues(engine)
			want := []struct{ id, cardinality, file string }{{"maybe_overdue_task", "maybe", "maybe_overdue_task_gen.go"}, {"overdue_task", "one", "overdue_task_gen.go"}, {"overdue_tasks", "many", "overdue_tasks_gen.go"}}
			for index, expected := range want {
				query := lock.Queries[index]
				require.Equal(t, expected.id, string(query.ID))
				require.Equal(t, expected.cardinality, query.Cardinality)
				require.Equal(t, "select", query.Operation)
				require.Equal(t, "queries/"+strings.TrimSuffix(expected.file, "_gen.go")+".sql", query.SQL.Path)
				require.Equal(t, wantParameters, query.Parameters)
				require.Equal(t, wantResults, query.Results)
				require.Equal(t, wantParameters, query.Evidence.Parameters)
				require.Equal(t, wantResults, query.Evidence.Results)
				require.Equal(t, lock.Engine.Dialect, query.Evidence.Dialect)
				require.Equal(t, lock.Engine.Profile, query.Evidence.Profile)
				require.Equal(t, []string{"projectID", "open", "cutoff"}, []string{query.Parameters[0].Name, query.Parameters[1].Name, query.Parameters[2].Name})
				require.Equal(t, []string{"id", "project_id", "assignee_id", "title", "is_open", "due_on", "created_at"}, valueNames(query.Results))
				data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(query.SQL.Path)))
				require.NoError(t, err)
				digest := sha256.Sum256(data)
				require.Equal(t, hex.EncodeToString(digest[:]), query.SQL.SHA256)
			}
			require.Equal(t, lock.Digests.Queries, manifestDigest(t, engine, "typed_query_digest"))
			require.Equal(t, lock.Digests.Queries, manifestDigest(t, engine, "rendered_sql_digest"))
		})
	}
}

func expectedProvenanceValues(engine string) ([]compilerlock.ValueRecord, []compilerlock.ValueRecord) {
	parameters := []compilerlock.ValueRecord{
		{Name: "projectID", Scalar: "integer", Nullable: false, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "integer"},
		{Name: "open", Scalar: "boolean", Nullable: false, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "boolean"},
		{Name: "cutoff", Scalar: "time", Nullable: false, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "time"},
	}
	results := []compilerlock.ValueRecord{
		{Name: "id", Scalar: "integer", Nullable: false, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "integer"},
		{Name: "project_id", Scalar: "integer", Nullable: false, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "integer"},
		{Name: "assignee_id", Scalar: "integer", Nullable: true, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "integer"},
		{Name: "title", Scalar: "text", Nullable: false, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "text"},
		{Name: "is_open", Scalar: "boolean", Nullable: false, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "boolean"},
		{Name: "due_on", Scalar: "time", Nullable: true, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "time"},
		{Name: "created_at", Scalar: "time", Nullable: false, TypeCertainty: "declared", NullabilityCertainty: "declared", LogicalKind: "time"},
	}
	if engine != "postgresql" {
		return parameters, results
	}
	native := func(name string) *compilerlock.NativeTypeRecord {
		return &compilerlock.NativeTypeRecord{Dialect: "postgresql", Name: name, Kind: "builtin"}
	}
	integer := func() *compilerlock.IntegerTypeFactsRecord {
		return &compilerlock.IntegerTypeFactsRecord{DisplayWidth: compilerlock.OptionalIntRecord{}}
	}
	parameters[0].TypeCertainty = "known"
	parameters[0].Native = native("int4")
	parameters[0].Integer = integer()
	parameters[1].TypeCertainty = "known"
	parameters[1].Native = native("bool")
	parameters[2].TypeCertainty = "known"
	parameters[2].Native = native("date")
	for index := range results {
		value := &results[index]
		switch value.Scalar {
		case "integer":
			value.TypeCertainty = "known"
			value.Native = native("int4")
			value.Integer = integer()
		case "boolean":
			value.TypeCertainty = "known"
			value.Native = native("bool")
		default:
			value.TypeCertainty = "known"
			value.Native = native(map[string]string{"text": "varchar", "time": "date"}[value.Scalar])
		}
	}
	results[6].Native = native("timestamp")
	return parameters, results
}

func valueNames(values []compilerlock.ValueRecord) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.Name
	}
	return result
}

func manifestDigest(t *testing.T, engine, key string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", engine, "d4-manifest.json"))
	require.NoError(t, err)
	var manifest map[string]any
	require.NoError(t, json.Unmarshal(data, &manifest))
	value, ok := manifest[key].(string)
	require.True(t, ok)
	return value
}

func TestGeneratedQueryProvenanceMutations(t *testing.T) {
	cli := buildRasqlCLI(t)
	for _, engine := range []string{"sqlite", "postgresql", "mysql"} {
		for _, queryID := range []string{"overdue_task", "maybe_overdue_task", "overdue_tasks"} {
			for _, mutation := range []struct{ name, diagnostic string }{
				{"cardinality", "generate: generated package is stale: queries\n"}, {"parameter type", "generate: generated package is stale: queries\n"},
				{"parameter order", "generate: generated package is stale: queries\n"}, {"result order", "generate: generated package is stale: queries\n"},
				{"output path", "generate: generated package is stale: generation\n"},
			} {
				t.Run(engine+"/"+queryID+"/"+mutation.name, func(t *testing.T) {
					root := filepath.Join(t.TempDir(), "fixture")
					require.NoError(t, copyTree(filepath.Join("testdata", engine), root))
					mutateFixtureConfig(t, filepath.Join(root, "rasql.json"), queryID, mutation.name)
					runProvenanceMutationCheck(t, root, cli, mutation.diagnostic)
				})
			}
			t.Run(engine+"/"+queryID+"/sql byte", func(t *testing.T) {
				root := filepath.Join(t.TempDir(), "fixture")
				require.NoError(t, copyTree(filepath.Join("testdata", engine), root))
				path := filepath.Join(root, "queries", queryID+".sql")
				data := mustReadFile(t, path)
				require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
				runProvenanceMutationCheck(t, root, cli, "generate: generated package is stale: queries\n")
			})
		}
	}
}

func mutateFixtureConfig(t *testing.T, path, queryID, mutation string) {
	t.Helper()
	var config map[string]any
	require.NoError(t, json.Unmarshal(mustReadFile(t, path), &config))
	queries, ok := config["queries"].([]any)
	require.True(t, ok)
	for _, raw := range queries {
		query, ok := raw.(map[string]any)
		require.True(t, ok)
		if query["id"] != queryID {
			continue
		}
		switch mutation {
		case "cardinality":
			query["cardinality"] = map[string]string{"one": "maybe", "maybe": "many", "many": "one"}[query["cardinality"].(string)]
		case "output path":
			query["output"] = "changed_" + queryID + "_gen.go"
		case "parameter type":
			query["parameters"].([]any)[0].(map[string]any)["scalar"] = "text"
		case "parameter order":
			values := query["parameters"].([]any)
			values[0], values[1] = values[1], values[0]
		case "result order":
			values := query["results"].([]any)
			values[0], values[1] = values[1], values[0]
		}
	}
	updated, err := json.MarshalIndent(config, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, append(updated, '\n'), 0o600))
}

func buildRasqlCLI(t *testing.T) string {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	binary := filepath.Join(t.TempDir(), "rasql")
	cache := filepath.Join(repoRoot, ".tmp", "a6-cli-build-cache")
	require.NoError(t, os.MkdirAll(cache, 0o755))
	command := exec.Command("go", "build", "-o", binary, "./cmd/rasql")
	command.Dir = repoRoot
	command.Env = offlineBuildEnv(cache)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
	return binary
}

func runProvenanceMutationCheck(t *testing.T, root, cli, diagnostic string) {
	t.Helper()
	command := exec.Command(cli, "generate", "-check", "-config", filepath.Join(root, "rasql.json"))
	command.Dir = root
	command.Env = offlineBuildEnv(filepath.Join(filepath.Dir(root), "a6-mutation-cache"))
	output, err := command.CombinedOutput()
	exitErr, ok := err.(*exec.ExitError)
	require.True(t, ok, string(output))
	require.Equal(t, 1, exitErr.ExitCode(), string(output))
	require.Equal(t, diagnostic, string(output))
}

func runGeneratedProfile(t *testing.T, tc profileCase, records string) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	moduleRoot := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, copyGeneratedStore(filepath.Join("testdata", tc.engine, "internal", "store"), filepath.Join(moduleRoot, "internal", "store")))
	driverSource, err := os.ReadFile("recording_driver_test.go")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, "recording_driver_test.go"), driverSource, 0o600))
	modulePath := "github.com/lestrrat-go/rasql/internal/conformance/fixture/" + tc.engine
	goMod := fmt.Sprintf("module %s\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nrequire %s\n\nreplace github.com/lestrrat-go/rasql => %s\n", modulePath, pinnedRequire(t, repoRoot, "github.com/stretchr/testify"), filepath.ToSlash(repoRoot))
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, "go.mod"), []byte(goMod), 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, "profile_consumer_test.go"), []byte(generatedProfileTest(tc, modulePath)), 0o600))
	command := exec.Command("go", "test", "-run", "^TestGeneratedProfile$")
	command.Dir = moduleRoot
	command.Env = append(offlineBuildEnv(t.TempDir()), "RASQL_PROFILE_RECORDS="+records)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func copyGeneratedStore(source, destination string) error {
	return filepath.WalkDir(source, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(source, path)
		if err != nil {
			return err
		}
		target := filepath.Join(destination, relative)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if filepath.Ext(path) != ".go" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o600)
	})
}

func generatedProfileTest(tc profileCase, modulePath string) string {
	dialectName := "SQLite"
	if tc.engine == "postgresql" {
		dialectName = "PostgreSQL"
	}
	if tc.engine == "mysql" {
		dialectName = "MySQL"
	}
	return fmt.Sprintf(`package conformance

import (
	"crypto/sha256"
	"database/sql/driver"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"%s/internal/store"
	"github.com/stretchr/testify/require"
)

type profileGraph struct { ID int64; Name string; Tasks rasql.LoadedMany[profileTask] }
type profileTask struct { ID int64; Title string; Assignee rasql.LoadedOne[store.MembersRow] }
type profileArgument struct { Type string `+"`json:\"type\"`"+`; Value string `+"`json:\"value\"`"+` }
type profileStatement struct { Operation string `+"`json:\"operation\"`"+`; Kind string `+"`json:\"kind\"`"+`; SQL string `+"`json:\"sql\"`"+`; Args []profileArgument `+"`json:\"args\"`"+` }
type profileOutput struct { Digest string `+"`json:\"digest\"`"+`; Statements []profileStatement `+"`json:\"statements\"`"+` }

func profileArg(value any) profileArgument {
	if typed, ok := value.(time.Time); ok {
		return profileArgument{Type: "time.Time", Value: typed.UTC().Format(time.RFC3339Nano)}
	}
	return profileArgument{Type: fmt.Sprintf("%%T", value), Value: fmt.Sprintf("%%v", value)}
}

func TestGeneratedProfile(t *testing.T) {
	cutoff := time.Date(2024, 1, 4, 0, 0, 0, 0, time.UTC)
	due := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	created := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	responses := []recordingResponse{
		{Kind: "query", Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "project-001"}}},
		{Kind: "query", Columns: []string{"id", "project_id", "assignee_id", "title", "is_open", "due_on", "created_at"}, Rows: [][]driver.Value{{int64(10), int64(1), int64(20), "overdue", true, due, created}}},
		{Kind: "query", Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(1), "project-001"}}},
		{Kind: "query", Columns: []string{"id", "project_id", "assignee_id", "title", "is_open", "due_on", "created_at"}, Rows: [][]driver.Value{{int64(10), int64(1), int64(20), "graph-task", true, due, created}}},
		{Kind: "query", Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(20), "member-020"}}},
	}
	if %t {
		responses = append(responses, recordingResponse{Kind: "query", Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(4001), "created"}}}, recordingResponse{Kind: "query", Columns: []string{"id", "name"}, Rows: [][]driver.Value{{int64(4001), "patched"}}})
	} else {
		responses = append(responses, recordingResponse{Kind: "exec", Affected: 1, AffectedSet: true}, recordingResponse{Kind: "exec", Affected: 1, AffectedSet: true})
	}
	state := &recordingDriverState{strict: true, responses: responses}
	database := openRecordingDB(state)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	raw, err := rasql.New(database, dialect.%s())
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion(%q, %d, %d, 0)
	require.NoError(t, err)
	executor, err := rasql.AsExecutor(raw, profile)
	require.NoError(t, err)
	projects, err := store.Projects().Source("p")
	require.NoError(t, err)
	projectColumns, err := (store.ProjectsColumns{}).Bind(projects)
	require.NoError(t, err)
	projectProjection, err := store.ProjectsProjection(projectColumns)
	require.NoError(t, err)
	projectQuery := rasql.Select(projects.Source(), projectProjection).Where(rasql.EqualValue(projectColumns.ID.Expr(), int64(1))).OrderBy(rasql.AscExpr(projectColumns.ID.Expr()))
	project, err := rasql.One(t.Context(), executor, projectQuery)
	require.NoError(t, err)
	require.Equal(t, int64(1), project.ID)
	require.Equal(t, "project-001", project.Name)
	overdue, err := store.OverdueTasks(int64(1), true, cutoff)
	require.NoError(t, err)
	overdueRows, err := rasql.All(t.Context(), executor, overdue)
	require.NoError(t, err)
	require.Len(t, overdueRows, 1)
	require.Equal(t, int64(10), overdueRows[0].ID)
	require.True(t, overdueRows[0].DueOn.Valid)
	tasks, err := store.Tasks().Source("t")
	require.NoError(t, err)
	members, err := store.Members().Source("m")
	require.NoError(t, err)
	taskColumns, err := (store.TasksColumns{}).Bind(tasks)
	require.NoError(t, err)
	memberColumns, err := (store.MembersColumns{}).Bind(members)
	require.NoError(t, err)
	taskProjection, err := store.TasksProjection(taskColumns)
	require.NoError(t, err)
	memberProjection, err := store.MembersProjection(memberColumns)
	require.NoError(t, err)
	taskQuery := rasql.Select(tasks.Source(), taskProjection).OrderBy(rasql.AscExpr(taskColumns.ID.Expr()))
	memberQuery := rasql.Select(members.Source(), memberProjection).OrderBy(rasql.AscExpr(memberColumns.ID.Expr()))
	memberPlan, err := rasql.NewGraphPlan(memberQuery, func(row store.MembersRow) store.MembersRow { return row })
	require.NoError(t, err)
	assignee, err := store.TasksAssigneeEdge(tasks, members, memberPlan, rasql.EdgeOptions{}, func(parent *profileTask, loaded rasql.LoadedOne[store.MembersRow]) { parent.Assignee = loaded })
	require.NoError(t, err)
	tasksPlan, err := rasql.NewGraphPlan(taskQuery, func(row store.TasksRow) profileTask { return profileTask{ID: row.ID, Title: row.Title} }, assignee)
	require.NoError(t, err)
	projectTasks, err := store.ProjectsTasksEdge(projects, tasks, tasksPlan, rasql.EdgeOptions{PerParentLimit: 5, Order: []rasql.OrderTerm{rasql.AscExpr(taskColumns.ID.Expr())}}, func(parent *profileGraph, loaded rasql.LoadedMany[profileTask]) { parent.Tasks = loaded })
	require.NoError(t, err)
	graphPlan, err := rasql.NewGraphPlan(projectQuery, func(row store.ProjectsRow) profileGraph { return profileGraph{ID: row.ID, Name: row.Name} }, projectTasks)
	require.NoError(t, err)
	graphs, err := rasql.LoadGraph(t.Context(), executor, graphPlan)
	require.NoError(t, err)
	require.Len(t, graphs, 1)
	require.True(t, graphs[0].Tasks.Loaded)
	require.Len(t, graphs[0].Tasks.Values, 1)
	require.True(t, graphs[0].Tasks.Values[0].Assignee.Loaded)
	require.True(t, graphs[0].Tasks.Values[0].Assignee.Present)
	require.Equal(t, int64(20), graphs[0].Tasks.Values[0].Assignee.Value.ID)
	createPlan, err := store.NewMembersCreate().ID(4001).Name("created").Plan()
	require.NoError(t, err)
	if %t {
		createSource, sourceErr := store.Members().Source("")
		require.NoError(t, sourceErr)
		createColumns, bindErr := (store.MembersColumns{}).Bind(createSource)
		require.NoError(t, bindErr)
		createProjection, projectionErr := store.MembersProjection(createColumns)
		require.NoError(t, projectionErr)
		createQuery, returningErr := rasql.Returning(createPlan, createProjection)
		require.NoError(t, returningErr)
		createdRow, queryErr := rasql.One(t.Context(), executor, createQuery)
		require.NoError(t, queryErr)
		require.Equal(t, int64(4001), createdRow.ID)
	} else {
		outcome, execErr := rasql.ExecMutation(t.Context(), executor, createPlan)
		require.NoError(t, execErr)
		require.Equal(t, int64(1), outcome.Affected)
	}
	patchSource, err := store.Members().Source("")
	require.NoError(t, err)
	patchColumns, err := (store.MembersColumns{}).Bind(patchSource)
	require.NoError(t, err)
	patchPlan, err := store.NewMembersPatch().Name("patched").Where(rasql.EqualValue(patchColumns.ID.Expr(), int64(4001)))
	require.NoError(t, err)
	if %t {
		patchProjection, projectionErr := store.MembersProjection(patchColumns)
		require.NoError(t, projectionErr)
		patchQuery, returningErr := rasql.Returning(patchPlan, patchProjection)
		require.NoError(t, returningErr)
		patchedRow, queryErr := rasql.One(t.Context(), executor, patchQuery)
		require.NoError(t, queryErr)
		require.Equal(t, "patched", patchedRow.Name)
	} else {
		outcome, execErr := rasql.ExecMutation(t.Context(), executor, patchPlan)
		require.NoError(t, execErr)
		require.Equal(t, int64(1), outcome.Affected)
	}
	calls := state.Calls()
	state.AssertDrained(t)
	require.Len(t, calls, 7)
	operations := []string{"single_row_read", "overdue_read", "graph_root", "graph_tasks", "graph_assignees", "create", "patch"}
	statements := make([]profileStatement, len(calls))
	for index, call := range calls {
		args := make([]profileArgument, len(call.args))
		for argIndex, value := range call.args {
			args[argIndex] = profileArg(value)
		}
		statements[index] = profileStatement{Operation: operations[index], Kind: call.kind, SQL: call.sql, Args: args}
	}
	result := profileOutput{Statements: statements}
	parts := make([]string, 0, len(statements)*4)
	for _, statement := range statements {
		parts = append(parts, statement.Operation, statement.Kind, statement.SQL, fmt.Sprint(len(statement.Args)))
		for _, arg := range statement.Args { parts = append(parts, arg.Type, arg.Value) }
	}
	hash := sha256.New()
	for _, part := range parts {
		var length [8]byte
		binary.BigEndian.PutUint64(length[:], uint64(len(part)))
		_, _ = hash.Write(length[:])
		_, _ = hash.Write([]byte(part))
	}
	result.Digest = hex.EncodeToString(hash.Sum(nil))
	data, err := json.MarshalIndent(result, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("RASQL_PROFILE_RECORDS"), append(data, '\n'), 0o600))
}
`, modulePath, tc.returning, dialectName, tc.id, tc.major, tc.minor, tc.returning, tc.returning)
}

func TestUnsupportedVersionBeforeSQL(t *testing.T) {
	for _, id := range []string{"postgresql-16", "postgresql-17", "mysql-8.4", "sqlite-3.35"} {
		_, err := rasql.EngineProfileFromVersion(id, 99, 0, 0)
		require.Error(t, err, id)
	}
}
