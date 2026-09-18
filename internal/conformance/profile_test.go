package conformance

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/cli/rasqlgen"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/gensum"
	"github.com/lestrrat-go/rasql/internal/scratchmod"
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

type profileRecords struct {
	Statements []profileStatement `json:"statements"`
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
			reviewed, ok := compileRenderStatements(tc.id)
			require.True(t, ok, tc.id)
			profile, err := builtinProfileByID(tc.id)
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
			var actual profileRecords
			require.NoError(t, json.Unmarshal(data, &actual))
			require.Equal(t, reviewed, actual.Statements)
		})
	}
}

// TestPostgreSQLProfilesRenderTheSameSQL pins that rasql renders one statement list for both
// supported PostgreSQL majors. compileRenderStatements hands both IDs the same slice, so this
// fails only once someone gives 16 and 17 lists of their own.
func TestPostgreSQLProfilesRenderTheSameSQL(t *testing.T) {
	sixteen, ok := compileRenderStatements("postgresql-16")
	require.True(t, ok)
	seventeen, ok := compileRenderStatements("postgresql-17")
	require.True(t, ok)
	require.Equal(t, sixteen, seventeen)
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
	// The fixture's own go.mod starts from the repository's, with its
	// go.sum copied alongside, rather than naming testify's version by
	// hand: a hand-picked require list has no go.sum entry for its own
	// module graph, and under CI's GOPROXY=off resolving that graph
	// (testify pulls in gopkg.in/yaml.v3, which requires gopkg.in/check.v1)
	// fails.
	require.NoError(t, scratchmod.Write(moduleRoot, repoRoot, modulePath))
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, "cardinality_profile_test.go"), []byte(generatedCardinalityProfileTest(tc, modulePath)), 0o600))
	command := exec.Command("go", "test", "-run", "^TestGeneratedCardinalityProfile$")
	command.Dir = moduleRoot
	// GOCACHE is deliberately shared, not rooted under t.TempDir(): see
	// sharedOfflineGOCACHE's comment in generation_test.go.
	command.Env = offlineBuildEnv(sharedOfflineGOCACHE)
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
	profileFuncName := map[string]string{
		"postgresql-16": "PostgreSQL16",
		"postgresql-17": "PostgreSQL17",
		"mysql-8.4":     "MySQL84",
		"sqlite-3.35":   "SQLite335",
	}[tc.id]
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
	executor, err := rasql.Open(t.Context(), db, dialect.%s(), rasql.WithProfile(rasql.%s()))
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
`, modulePath, querySQL, dialectName, profileFuncName, querySQL)
}

// fixtureConfig is the subset of a fixture's rasql.json TestGeneratedQueryProvenance reads
// straight from the config rather than from a decoded lock: the settings-digest inputs (package,
// output, emitter, prune) and each declared query's own fields.
type fixtureConfig struct {
	Package string         `json:"package"`
	Output  string         `json:"output"`
	Emitter string         `json:"emitter"`
	Prune   *bool          `json:"prune"`
	Queries []fixtureQuery `json:"queries"`
}

type fixtureQuery struct {
	ID          string         `json:"id"`
	Input       string         `json:"input"`
	Function    string         `json:"function"`
	Output      string         `json:"output"`
	Operation   string         `json:"operation"`
	Cardinality string         `json:"cardinality"`
	Parameters  []fixtureValue `json:"parameters"`
	Results     []fixtureValue `json:"results"`
}

type fixtureValue struct {
	Name string `json:"name"`
}

func readFixtureConfig(t *testing.T, root string) fixtureConfig {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "rasql.json"))
	require.NoError(t, err)
	var cfg fixtureConfig
	require.NoError(t, json.Unmarshal(data, &cfg))
	return cfg
}

func (cfg fixtureConfig) queryByID(id string) (fixtureQuery, bool) {
	for _, query := range cfg.Queries {
		if query.ID == id {
			return query, true
		}
	}
	return fixtureQuery{}, false
}

func fixtureValueNames(values []fixtureValue) []string {
	names := make([]string, len(values))
	for index, value := range values {
		names[index] = value.Name
	}
	return names
}

// TestGeneratedQueryProvenance asserts what the settings-digest inputs and the generated Go files
// themselves say about each fixture's three queries: the package, output directory, emitter, and
// prune setting rasql.json declares; the exact function, result, and decoder names the compact
// emitter produced; and that rasql.sum's recorded query checksum matches the query file's current
// bytes. It carries no assertion about PostgreSQL's live-catalog type promotion (declared
// certainty upgraded to known, with native type facts attached): that claim has no offline
// substitute and lives in TestGeneratedQueryProvenanceNativeTypesLive instead, guarded by
// internal/dbtest.
func TestGeneratedQueryProvenance(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			root := filepath.Join("testdata", engine)
			cfg := readFixtureConfig(t, root)
			require.Equal(t, "store", cfg.Package)
			require.Equal(t, "internal/store", cfg.Output)
			require.Equal(t, "compact", cfg.Emitter)
			require.Nil(t, cfg.Prune, "unset means the compact default, prune true, applies")
			require.Len(t, cfg.Queries, 3)

			sumData, err := os.ReadFile(filepath.Join(root, cfg.Output, "rasql.sum"))
			require.NoError(t, err)
			sum, err := gensum.Parse(sumData)
			require.NoError(t, err)

			want := []struct{ id, function, file, cardinality string }{
				{"maybe_overdue_task", "MaybeOverdueTask", "maybe_overdue_task_gen.go", "maybe"},
				{"overdue_task", "OverdueTask", "overdue_task_gen.go", "one"},
				{"overdue_tasks", "OverdueTasks", "overdue_tasks_gen.go", "many"},
			}
			for _, expected := range want {
				query, ok := cfg.queryByID(expected.id)
				require.True(t, ok, expected.id)
				require.Equal(t, expected.function, query.Function)
				require.Equal(t, expected.file, query.Output)
				require.Equal(t, expected.cardinality, query.Cardinality)
				require.Equal(t, "select", query.Operation)
				require.Equal(t, "queries/"+expected.id+".sql", query.Input)
				require.Equal(t, []string{"projectID", "open", "cutoff"}, fixtureValueNames(query.Parameters))
				require.Equal(t, []string{"id", "project_id", "assignee_id", "title", "is_open", "due_on", "created_at"}, fixtureValueNames(query.Results))

				source, err := os.ReadFile(filepath.Join(root, cfg.Output, expected.file))
				require.NoError(t, err)
				text := string(source)
				require.Contains(t, text, "func "+expected.function+"(")
				require.Contains(t, text, "type "+expected.id+"Result struct")
				require.Contains(t, text, "type "+expected.id+"Decoder struct")

				queryData, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(query.Input)))
				require.NoError(t, err)
				digest := sha256.Sum256(queryData)
				recordedPath := "internal/conformance/" + filepath.ToSlash(root) + "/" + query.Input
				value, ok := gensumEntryValue(sum.Queries, recordedPath)
				require.True(t, ok, recordedPath)
				require.Equal(t, "sha256:"+hex.EncodeToString(digest[:]), value)
			}
		})
	}
}

func gensumEntryValue(entries []gensum.Entry, name string) (string, bool) {
	for _, entry := range entries {
		if entry.Name == name {
			return entry.Value, true
		}
	}
	return "", false
}

// TestGeneratedQueryProvenanceMutations proves that codegen check -- run offline, against
// rasql.sum, with no database -- catches a fixture's rasql.json and its query files drifting out
// of step with the generated store. Every query-declaration mutation (cardinality, a parameter's
// type, either value list's order, the output file it renders to) changes the settings line, since
// settingsSnapshot.Queries carries the whole declared query list; only editing a query's own SQL
// file changes the queries line. This replaces the old lock-backed generate -check invocation
// (decision 11): T14 deletes -check from generate, so a mutation test still calling it that way
// would have nothing left to call.
func TestGeneratedQueryProvenanceMutations(t *testing.T) {
	for _, engine := range []string{"sqlite", "postgresql", "mysql"} {
		for _, queryID := range []string{"overdue_task", "maybe_overdue_task", "overdue_tasks"} {
			for _, mutation := range []string{"cardinality", "parameter type", "parameter order", "result order", "output path"} {
				t.Run(engine+"/"+queryID+"/"+mutation, func(t *testing.T) {
					_, root := copyFixtureAsModule(t, engine)
					mutateFixtureConfig(t, filepath.Join(root, "rasql.json"), queryID, mutation)
					runProvenanceMutationCheck(t, root, "check: generate: generated package is stale: settings (no database was consulted)")
				})
			}
			t.Run(engine+"/"+queryID+"/sql byte", func(t *testing.T) {
				_, root := copyFixtureAsModule(t, engine)
				path := filepath.Join(root, "queries", queryID+".sql")
				data := mustReadFile(t, path)
				require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
				recordedPath := "internal/conformance/testdata/" + engine + "/queries/" + queryID + ".sql"
				runProvenanceMutationCheck(t, root, "check: generate: generated package is stale: queries: "+recordedPath+" (no database was consulted)")
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

// runProvenanceMutationCheck runs codegen check in-process against root's rasql.json, exactly as
// rasqlgen.RunContext lets any caller in this module do, and requires the returned error's message
// to equal diagnostic and its mapped exit code to be 1 -- the same contract cmd/rasql applies to
// turn this error into a process exit.
func runProvenanceMutationCheck(t *testing.T, root, diagnostic string) {
	t.Helper()
	var output, diagnostics bytes.Buffer
	err := rasqlgen.RunContext(t.Context(), []string{"check", "-config", filepath.Join(root, "rasql.json")}, &output, &diagnostics)
	require.Error(t, err)
	require.Equal(t, 1, rasqlgen.ExitCode(err))
	require.Equal(t, diagnostic, err.Error())
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
	modulePath := "example.test/profile/" + tc.engine
	// The fixture's own go.mod starts from the repository's, with its
	// go.sum copied alongside: see runGeneratedCardinalityProfile's comment
	// on why a hand-picked require list breaks under CI's GOPROXY=off.
	require.NoError(t, scratchmod.Write(moduleRoot, repoRoot, modulePath))
	require.NoError(t, os.WriteFile(filepath.Join(moduleRoot, "profile_consumer_test.go"), []byte(generatedProfileTest(tc, modulePath)), 0o600))
	command := exec.Command("go", "test", "-run", "^TestGeneratedProfile$")
	command.Dir = moduleRoot
	// GOCACHE is deliberately shared, not rooted under t.TempDir(): see
	// sharedOfflineGOCACHE's comment in generation_test.go.
	command.Env = append(offlineBuildEnv(sharedOfflineGOCACHE), "RASQL_PROFILE_RECORDS="+records)
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
	profileFuncName := map[string]string{
		"postgresql-16": "PostgreSQL16",
		"postgresql-17": "PostgreSQL17",
		"mysql-8.4":     "MySQL84",
		"sqlite-3.35":   "SQLite335",
	}[tc.id]
	return fmt.Sprintf(`package conformance

import (
	"database/sql/driver"
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
type profileOutput struct { Statements []profileStatement `+"`json:\"statements\"`"+` }

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
	executor, err := rasql.Open(t.Context(), database, dialect.%s(), rasql.WithProfile(rasql.%s()))
	require.NoError(t, err)
	projects, err := store.Projects().As("p")
	require.NoError(t, err)
	projectColumns, err := (store.ProjectsColumns{}).Bind(projects.Table())
	require.NoError(t, err)
	projectProjection, err := store.ProjectsProjection(projectColumns)
	require.NoError(t, err)
	projectQuery := rasql.Select(projects, projectProjection).Where(rasql.EqualValue(projectColumns.ID.Expr(), int64(1))).OrderBy(rasql.AscExpr(projectColumns.ID.Expr()))
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
	tasks, err := store.Tasks().As("t")
	require.NoError(t, err)
	members, err := store.Members().As("m")
	require.NoError(t, err)
	taskColumns, err := (store.TasksColumns{}).Bind(tasks.Table())
	require.NoError(t, err)
	memberColumns, err := (store.MembersColumns{}).Bind(members.Table())
	require.NoError(t, err)
	taskProjection, err := store.TasksProjection(taskColumns)
	require.NoError(t, err)
	memberProjection, err := store.MembersProjection(memberColumns)
	require.NoError(t, err)
	taskQuery := rasql.Select(tasks, taskProjection).OrderBy(rasql.AscExpr(taskColumns.ID.Expr()))
	memberQuery := rasql.Select(members, memberProjection).OrderBy(rasql.AscExpr(memberColumns.ID.Expr()))
	memberPlan, err := rasql.NewGraphPlan(memberQuery, func(row store.MembersRow) store.MembersRow { return row })
	require.NoError(t, err)
	assignee, err := store.TasksAssigneeEdge(tasks.Table(), members.Table(), memberPlan, rasql.EdgeOptions{}, func(parent *profileTask, loaded rasql.LoadedOne[store.MembersRow]) { parent.Assignee = loaded })
	require.NoError(t, err)
	tasksPlan, err := rasql.NewGraphPlan(taskQuery, func(row store.TasksRow) profileTask { return profileTask{ID: row.ID, Title: row.Title} }, assignee)
	require.NoError(t, err)
	projectTasks, err := store.ProjectsTasksEdge(projects.Table(), tasks.Table(), tasksPlan, rasql.EdgeOptions{PerParentLimit: 5, Order: []rasql.OrderTerm{rasql.AscExpr(taskColumns.ID.Expr())}}, func(parent *profileGraph, loaded rasql.LoadedMany[profileTask]) { parent.Tasks = loaded })
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
	createPlan, err := store.Members().Create().ID(4001).Name("created").Plan()
	require.NoError(t, err)
	if %t {
		createSource := store.Members().Table()
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
	patchSource := store.Members().Table()
	patchColumns, err := (store.MembersColumns{}).Bind(patchSource)
	require.NoError(t, err)
	patchPlan, err := store.Members().Patch().Name("patched").Where(rasql.EqualValue(patchColumns.ID.Expr(), int64(4001)))
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
	data, err := json.MarshalIndent(result, "", "  ")
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(os.Getenv("RASQL_PROFILE_RECORDS"), append(data, '\n'), 0o600))
}
`, modulePath, tc.returning, dialectName, profileFuncName, tc.returning, tc.returning)
}

func TestUnsupportedVersionBeforeSQL(t *testing.T) {
	for _, id := range []string{"postgresql-16", "postgresql-17", "mysql-8.4", "sqlite-3.35"} {
		_, err := engineprofile.Builtin(id, engineprofile.Version{Known: true, Major: 99})
		require.Error(t, err, id)
	}
}

// compileRenderStatements gives back the SQL a fixture's generated store renders under profileID,
// with the arguments it binds, in the order TestGeneratedProfile executes the seven operations.
// A rendering change fails TestCompileRenderProfiles and shows up here as the statement that
// moved, so re-blessing this table means reading the diff and accepting the new SQL.
//
// PostgreSQL 16 and 17 share one slice, and TestPostgreSQLProfilesRenderTheSameSQL states that
// rather than leaving a reader to infer it from this function's shape.
func compileRenderStatements(profileID string) ([]profileStatement, bool) {
	postgresql := []profileStatement{
		{
			Operation: "single_row_read",
			Kind:      "query",
			SQL:       "SELECT \"p\".\"id\" AS \"id\", \"p\".\"name\" AS \"name\" FROM \"projects\" AS \"p\" WHERE (\"p\".\"id\" = $1) ORDER BY \"p\".\"id\"",
			Args: []profileArgument{
				{Type: "int64", Value: "1"},
			},
		},
		{
			Operation: "overdue_read",
			Kind:      "query",
			SQL:       "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at\nFROM tasks\nWHERE project_id = $1\n  AND is_open = $2\n  AND due_on IS NOT NULL\n  AND due_on < $3\nORDER BY id\n",
			Args: []profileArgument{
				{Type: "int64", Value: "1"},
				{Type: "bool", Value: "true"},
				{Type: "time.Time", Value: "2024-01-04T00:00:00Z"},
			},
		},
		{
			Operation: "graph_root",
			Kind:      "query",
			SQL:       "SELECT \"p\".\"id\" AS \"id\", \"p\".\"name\" AS \"name\" FROM \"projects\" AS \"p\" WHERE (\"p\".\"id\" = $1) ORDER BY \"p\".\"id\"",
			Args: []profileArgument{
				{Type: "int64", Value: "1"},
			},
		},
		{
			Operation: "graph_tasks",
			Kind:      "query",
			SQL:       "SELECT \"partition_source\".\"id\" AS \"id\", \"partition_source\".\"project_id\" AS \"project_id\", \"partition_source\".\"assignee_id\" AS \"assignee_id\", \"partition_source\".\"title\" AS \"title\", \"partition_source\".\"is_open\" AS \"is_open\", \"partition_source\".\"due_on\" AS \"due_on\", \"partition_source\".\"created_at\" AS \"created_at\" FROM (SELECT \"t\".\"id\" AS \"id\", \"t\".\"project_id\" AS \"project_id\", \"t\".\"assignee_id\" AS \"assignee_id\", \"t\".\"title\" AS \"title\", \"t\".\"is_open\" AS \"is_open\", \"t\".\"due_on\" AS \"due_on\", \"t\".\"created_at\" AS \"created_at\", row_number() OVER (PARTITION BY \"t\".\"project_id\" ORDER BY \"t\".\"id\") AS \"__rasql_partition_row\" FROM \"tasks\" AS \"t\" WHERE (\"t\".\"project_id\" = $1) ORDER BY \"t\".\"id\", \"t\".\"id\") AS \"partition_source\" WHERE (\"partition_source\".\"__rasql_partition_row\" <= $2)",
			Args: []profileArgument{
				{Type: "int64", Value: "1"},
				{Type: "int64", Value: "5"},
			},
		},
		{
			Operation: "graph_assignees",
			Kind:      "query",
			SQL:       "SELECT \"partition_source\".\"id\" AS \"id\", \"partition_source\".\"name\" AS \"name\" FROM (SELECT \"m\".\"id\" AS \"id\", \"m\".\"name\" AS \"name\", row_number() OVER (PARTITION BY \"m\".\"id\" ORDER BY \"m\".\"id\") AS \"__rasql_partition_row\" FROM \"members\" AS \"m\" WHERE (\"m\".\"id\" = $1) ORDER BY \"m\".\"id\", \"m\".\"id\") AS \"partition_source\" WHERE (\"partition_source\".\"__rasql_partition_row\" <= $2)",
			Args: []profileArgument{
				{Type: "int64", Value: "20"},
				{Type: "int64", Value: "2"},
			},
		},
		{
			Operation: "create",
			Kind:      "query",
			SQL:       "INSERT INTO \"members\" (\"id\", \"name\") VALUES ($1, $2) RETURNING \"id\" AS \"id\", \"name\" AS \"name\"",
			Args: []profileArgument{
				{Type: "int64", Value: "4001"},
				{Type: "string", Value: "created"},
			},
		},
		{
			Operation: "patch",
			Kind:      "query",
			SQL:       "UPDATE \"members\" SET \"name\" = $1 WHERE (\"members\".\"id\" = $2) RETURNING \"id\" AS \"id\", \"name\" AS \"name\"",
			Args: []profileArgument{
				{Type: "string", Value: "patched"},
				{Type: "int64", Value: "4001"},
			},
		},
	}
	switch profileID {
	case "postgresql-16", "postgresql-17":
		return postgresql, true
	case "mysql-8.4":
		return []profileStatement{
			{
				Operation: "single_row_read",
				Kind:      "query",
				SQL:       "SELECT `p`.`id` AS `id`, `p`.`name` AS `name` FROM `projects` AS `p` WHERE (`p`.`id` = ?) ORDER BY `p`.`id`",
				Args: []profileArgument{
					{Type: "int64", Value: "1"},
				},
			},
			{
				Operation: "overdue_read",
				Kind:      "query",
				SQL:       "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at\nFROM tasks\nWHERE project_id = ?\n  AND is_open = ?\n  AND due_on IS NOT NULL\n  AND due_on < ?\nORDER BY id\n",
				Args: []profileArgument{
					{Type: "int64", Value: "1"},
					{Type: "bool", Value: "true"},
					{Type: "time.Time", Value: "2024-01-04T00:00:00Z"},
				},
			},
			{
				Operation: "graph_root",
				Kind:      "query",
				SQL:       "SELECT `p`.`id` AS `id`, `p`.`name` AS `name` FROM `projects` AS `p` WHERE (`p`.`id` = ?) ORDER BY `p`.`id`",
				Args: []profileArgument{
					{Type: "int64", Value: "1"},
				},
			},
			{
				Operation: "graph_tasks",
				Kind:      "query",
				SQL:       "SELECT `partition_source`.`id` AS `id`, `partition_source`.`project_id` AS `project_id`, `partition_source`.`assignee_id` AS `assignee_id`, `partition_source`.`title` AS `title`, `partition_source`.`is_open` AS `is_open`, `partition_source`.`due_on` AS `due_on`, `partition_source`.`created_at` AS `created_at` FROM (SELECT `t`.`id` AS `id`, `t`.`project_id` AS `project_id`, `t`.`assignee_id` AS `assignee_id`, `t`.`title` AS `title`, `t`.`is_open` AS `is_open`, `t`.`due_on` AS `due_on`, `t`.`created_at` AS `created_at`, row_number() OVER (PARTITION BY `t`.`project_id` ORDER BY `t`.`id`) AS `__rasql_partition_row` FROM `tasks` AS `t` WHERE (`t`.`project_id` = ?) ORDER BY `t`.`id`, `t`.`id`) AS `partition_source` WHERE (`partition_source`.`__rasql_partition_row` <= ?)",
				Args: []profileArgument{
					{Type: "int64", Value: "1"},
					{Type: "int64", Value: "5"},
				},
			},
			{
				Operation: "graph_assignees",
				Kind:      "query",
				SQL:       "SELECT `partition_source`.`id` AS `id`, `partition_source`.`name` AS `name` FROM (SELECT `m`.`id` AS `id`, `m`.`name` AS `name`, row_number() OVER (PARTITION BY `m`.`id` ORDER BY `m`.`id`) AS `__rasql_partition_row` FROM `members` AS `m` WHERE (`m`.`id` = ?) ORDER BY `m`.`id`, `m`.`id`) AS `partition_source` WHERE (`partition_source`.`__rasql_partition_row` <= ?)",
				Args: []profileArgument{
					{Type: "int64", Value: "20"},
					{Type: "int64", Value: "2"},
				},
			},
			{
				Operation: "create",
				Kind:      "exec",
				SQL:       "INSERT INTO `members` (`id`, `name`) VALUES (?, ?)",
				Args: []profileArgument{
					{Type: "int64", Value: "4001"},
					{Type: "string", Value: "created"},
				},
			},
			{
				Operation: "patch",
				Kind:      "exec",
				SQL:       "UPDATE `members` SET `name` = ? WHERE (`members`.`id` = ?)",
				Args: []profileArgument{
					{Type: "string", Value: "patched"},
					{Type: "int64", Value: "4001"},
				},
			},
		}, true
	case "sqlite-3.35":
		return []profileStatement{
			{
				Operation: "single_row_read",
				Kind:      "query",
				SQL:       "SELECT \"p\".\"id\" AS \"id\", \"p\".\"name\" AS \"name\" FROM \"projects\" AS \"p\" WHERE (\"p\".\"id\" = ?) ORDER BY \"p\".\"id\"",
				Args: []profileArgument{
					{Type: "int64", Value: "1"},
				},
			},
			{
				Operation: "overdue_read",
				Kind:      "query",
				SQL:       "SELECT id, project_id, assignee_id, title, is_open, due_on, created_at\nFROM tasks\nWHERE project_id = ?\n  AND is_open = ?\n  AND due_on IS NOT NULL\n  AND due_on < ?\nORDER BY id\n",
				Args: []profileArgument{
					{Type: "int64", Value: "1"},
					{Type: "bool", Value: "true"},
					{Type: "time.Time", Value: "2024-01-04T00:00:00Z"},
				},
			},
			{
				Operation: "graph_root",
				Kind:      "query",
				SQL:       "SELECT \"p\".\"id\" AS \"id\", \"p\".\"name\" AS \"name\" FROM \"projects\" AS \"p\" WHERE (\"p\".\"id\" = ?) ORDER BY \"p\".\"id\"",
				Args: []profileArgument{
					{Type: "int64", Value: "1"},
				},
			},
			{
				Operation: "graph_tasks",
				Kind:      "query",
				SQL:       "SELECT \"partition_source\".\"id\" AS \"id\", \"partition_source\".\"project_id\" AS \"project_id\", \"partition_source\".\"assignee_id\" AS \"assignee_id\", \"partition_source\".\"title\" AS \"title\", \"partition_source\".\"is_open\" AS \"is_open\", \"partition_source\".\"due_on\" AS \"due_on\", \"partition_source\".\"created_at\" AS \"created_at\" FROM (SELECT \"t\".\"id\" AS \"id\", \"t\".\"project_id\" AS \"project_id\", \"t\".\"assignee_id\" AS \"assignee_id\", \"t\".\"title\" AS \"title\", \"t\".\"is_open\" AS \"is_open\", \"t\".\"due_on\" AS \"due_on\", \"t\".\"created_at\" AS \"created_at\", row_number() OVER (PARTITION BY \"t\".\"project_id\" ORDER BY \"t\".\"id\") AS \"__rasql_partition_row\" FROM \"tasks\" AS \"t\" WHERE (\"t\".\"project_id\" = ?) ORDER BY \"t\".\"id\", \"t\".\"id\") AS \"partition_source\" WHERE (\"partition_source\".\"__rasql_partition_row\" <= ?)",
				Args: []profileArgument{
					{Type: "int64", Value: "1"},
					{Type: "int64", Value: "5"},
				},
			},
			{
				Operation: "graph_assignees",
				Kind:      "query",
				SQL:       "SELECT \"partition_source\".\"id\" AS \"id\", \"partition_source\".\"name\" AS \"name\" FROM (SELECT \"m\".\"id\" AS \"id\", \"m\".\"name\" AS \"name\", row_number() OVER (PARTITION BY \"m\".\"id\" ORDER BY \"m\".\"id\") AS \"__rasql_partition_row\" FROM \"members\" AS \"m\" WHERE (\"m\".\"id\" = ?) ORDER BY \"m\".\"id\", \"m\".\"id\") AS \"partition_source\" WHERE (\"partition_source\".\"__rasql_partition_row\" <= ?)",
				Args: []profileArgument{
					{Type: "int64", Value: "20"},
					{Type: "int64", Value: "2"},
				},
			},
			{
				Operation: "create",
				Kind:      "query",
				SQL:       "INSERT INTO \"members\" (\"id\", \"name\") VALUES (?, ?) RETURNING \"id\" AS \"id\", \"name\" AS \"name\"",
				Args: []profileArgument{
					{Type: "int64", Value: "4001"},
					{Type: "string", Value: "created"},
				},
			},
			{
				Operation: "patch",
				Kind:      "query",
				SQL:       "UPDATE \"members\" SET \"name\" = ? WHERE (\"members\".\"id\" = ?) RETURNING \"id\" AS \"id\", \"name\" AS \"name\"",
				Args: []profileArgument{
					{Type: "string", Value: "patched"},
					{Type: "int64", Value: "4001"},
				},
			},
		}, true
	}
	return nil, false
}
