//go:build linux || darwin

package conformance

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/internal/compilerlock"
	"github.com/stretchr/testify/require"
)

type buildSample struct {
	Sample       int    `json:"sample"`
	Program      string `json:"program"`
	WallNS       int64  `json:"wall_ns"`
	UserNS       int64  `json:"user_ns"`
	SystemNS     int64  `json:"system_ns"`
	CPUNS        int64  `json:"cpu_ns"`
	PeakRSSBytes int64  `json:"peak_rss_bytes"`
	BinaryBytes  int64  `json:"binary_bytes"`
}

type footprintManifest struct {
	Engine         string `json:"engine"`
	Profile        string `json:"profile"`
	GeneratedFiles int    `json:"generated_files"`
	GeneratedBytes int64  `json:"generated_bytes"`
}

type buildArtifact struct {
	Format             string        `json:"format"`
	Engine             string        `json:"engine"`
	Profile            string        `json:"profile"`
	GoVersion          string        `json:"go_version"`
	GOOS               string        `json:"goos"`
	GOARCH             string        `json:"goarch"`
	Commit             string        `json:"commit"`
	Workload           string        `json:"workload"`
	CachePolicy        string        `json:"cache_policy"`
	GeneratedFiles     int           `json:"generated_files"`
	GeneratedBytes     int64         `json:"generated_bytes"`
	GenerationDuration int64         `json:"generation_duration_ns"`
	RasqlMedian        buildSample   `json:"rasql_median"`
	SQLMedian          buildSample   `json:"sql_median"`
	RasqlSamples       []buildSample `json:"rasql_samples"`
	SQLSamples         []buildSample `json:"sql_samples"`
}

func TestGeneratedFootprintBuild(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	cli := buildRasqlCLI(t)
	for _, engine := range []string{"sqlite", "postgresql", "mysql"} {
		t.Run(engine, func(t *testing.T) {
			copyRoot := filepath.Join(t.TempDir(), "fixture")
			require.NoError(t, copyTree(filepath.Join("testdata", engine), copyRoot))
			manifest := readFootprintManifest(t, filepath.Join(copyRoot, "d4-manifest.json"))
			expected := expectedGeneratedFiles(t, copyRoot)
			original := snapshotGeneratedFiles(t, copyRoot, expected)
			removeGeneratedOutput(t, copyRoot)
			require.NoError(t, writeBuildModule(copyRoot, repoRoot))
			config := filepath.Join(copyRoot, "rasql.json")
			generate := exec.Command(cli, "generate", "-config", config)
			generate.Dir = copyRoot
			generate.Env = offlineBuildEnv(filepath.Join(t.TempDir(), "generate-cache"))
			generationStarted := time.Now()
			output, err := generate.CombinedOutput()
			generationDurationNS := time.Since(generationStarted).Nanoseconds()
			require.NoError(t, err, string(output))
			files, bytes := generatedFootprint(t, copyRoot, expected)
			assertGeneratedFiles(t, copyRoot, original)
			require.Equal(t, manifest.GeneratedFiles, files)
			require.Equal(t, manifest.GeneratedBytes, bytes)
			check := exec.Command(cli, "check", "-config", config)
			check.Dir = copyRoot
			check.Env = offlineBuildEnv(filepath.Join(t.TempDir(), "check-cache"))
			checkOutput, checkErr := check.CombinedOutput()
			require.NoError(t, checkErr, string(checkOutput))
			require.NoError(t, writeConsumer(copyRoot, engine, true))
			require.NoError(t, writeConsumer(copyRoot, engine, false))

			warmup := []struct {
				program string
				path    string
			}{
				{program: "rasql", path: "./cmd/rasql-evidence"},
				{program: "sql", path: "./cmd/sql-evidence"},
			}
			for _, item := range warmup {
				runBuild(t, copyRoot, item.path, "warm-"+item.program)
				runProgram(t, filepath.Join(copyRoot, ".tmp", "warm-"+item.program))
			}
			const pairs = 5
			rasqlSamples := make([]buildSample, 0, pairs)
			sqlSamples := make([]buildSample, 0, pairs)
			for index := 0; index < pairs; index++ {
				if index%2 == 0 {
					rasqlSamples = append(rasqlSamples, identifiedBuildSample(t, copyRoot, "./cmd/rasql-evidence", "rasql-"+fmt.Sprint(index), index+1, "rasql"))
					sqlSamples = append(sqlSamples, identifiedBuildSample(t, copyRoot, "./cmd/sql-evidence", "sql-"+fmt.Sprint(index), index+1, "sql"))
				} else {
					sqlSamples = append(sqlSamples, identifiedBuildSample(t, copyRoot, "./cmd/sql-evidence", "sql-"+fmt.Sprint(index), index+1, "sql"))
					rasqlSamples = append(rasqlSamples, identifiedBuildSample(t, copyRoot, "./cmd/rasql-evidence", "rasql-"+fmt.Sprint(index), index+1, "rasql"))
				}
			}
			for _, sample := range append(append([]buildSample(nil), rasqlSamples...), sqlSamples...) {
				require.Positive(t, sample.WallNS)
				require.GreaterOrEqual(t, sample.UserNS, int64(0))
				require.GreaterOrEqual(t, sample.SystemNS, int64(0))
				require.GreaterOrEqual(t, sample.CPUNS, int64(0))
				require.Positive(t, sample.PeakRSSBytes)
				require.Positive(t, sample.BinaryBytes)
			}
			writeBuildArtifact(t, repoRoot, manifest, files, bytes, generationDurationNS, rasqlSamples, sqlSamples)
		})
	}
}

func snapshotGeneratedFiles(t *testing.T, root string, expected map[string]struct{}) map[string][]byte {
	t.Helper()
	snapshot := make(map[string][]byte, len(expected))
	for path := range expected {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		require.NoError(t, err, path)
		snapshot[path] = data
	}
	return snapshot
}

func assertGeneratedFiles(t *testing.T, root string, expected map[string][]byte) {
	t.Helper()
	for path, want := range expected {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		require.NoError(t, err, path)
		require.Equal(t, want, data, path)
	}
}

func readFootprintManifest(t *testing.T, path string) footprintManifest {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	var manifest footprintManifest
	require.NoError(t, json.Unmarshal(data, &manifest))
	return manifest
}

func removeGeneratedOutput(t *testing.T, root string) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "rasql.lock.json"))
	require.NoError(t, err)
	lock, err := compilerlock.Decode(data)
	require.NoError(t, err)
	require.NoError(t, os.RemoveAll(filepath.Join(root, lock.Generation.Output)))
}

func expectedGeneratedFiles(t *testing.T, root string) map[string]struct{} {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "rasql.lock.json"))
	require.NoError(t, err)
	lock, err := compilerlock.Decode(data)
	require.NoError(t, err)
	files := make(map[string]struct{}, len(lock.Generation.Objects)+2)
	for _, object := range lock.Generation.Objects {
		files[filepath.ToSlash(filepath.Join(lock.Generation.Output, object.File))] = struct{}{}
	}
	for _, query := range lock.Generation.Queries {
		files[filepath.ToSlash(filepath.Join(lock.Generation.Output, query.File))] = struct{}{}
	}
	files[filepath.ToSlash(filepath.Join(lock.Generation.Output, "schema_gen.go"))] = struct{}{}
	files[filepath.ToSlash(filepath.Join(lock.Generation.Output, "schema_gen_test.go"))] = struct{}{}
	return files
}

func generatedFootprint(t *testing.T, root string, expected map[string]struct{}) (int, int64) {
	t.Helper()
	seen := make(map[string]struct{}, len(expected))
	var bytes int64
	for path := range expected {
		full := filepath.Join(root, filepath.FromSlash(path))
		info, err := os.Stat(full)
		require.NoError(t, err, path)
		require.False(t, info.IsDir(), path)
		seen[path] = struct{}{}
		bytes += info.Size()
	}
	output := filepath.Dir(filepath.Join(root, filepath.FromSlash(filepath.Join("placeholder", "placeholder"))))
	for path := range expected {
		output = filepath.Dir(filepath.Join(root, filepath.FromSlash(path)))
		break
	}
	var actual []string
	require.NoError(t, filepath.Walk(output, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		actual = append(actual, filepath.ToSlash(relative))
		return nil
	}))
	sort.Strings(actual)
	for _, path := range actual {
		if _, ok := seen[path]; !ok {
			t.Fatalf("unexpected generated file %q", path)
		}
	}
	require.Len(t, actual, len(expected))
	return len(expected), bytes
}

func offlineBuildEnv(cache string) []string {
	blocked := map[string]struct{}{
		"RASQL_TEST_POSTGRES_DSN": {}, "RASQL_TEST_MYSQL_DSN": {},
		"TASKBOARD_SCHEMA_DSN": {}, "TASKBOARD_DSN": {}, "TASKBOARD_TEST_DSN": {},
		"GOPROXY": {}, "GOSUMDB": {}, "GOTOOLCHAIN": {}, "GOFLAGS": {}, "GOCACHE": {}, "GOWORK": {},
	}
	env := make([]string, 0, len(os.Environ())+5)
	for _, value := range os.Environ() {
		name, _, ok := strings.Cut(value, "=")
		if ok {
			if _, blocked := blocked[name]; blocked {
				continue
			}
		}
		env = append(env, value)
	}
	return append(env, "GOPROXY=off", "GOSUMDB=off", "GOTOOLCHAIN=local", "GOFLAGS=-mod=mod", "GOWORK=off", "GOCACHE="+cache)
}

func TestOfflineBuildEnvRemovesLiveDSNs(t *testing.T) {
	t.Setenv("RASQL_TEST_POSTGRES_DSN", "postgres://secret")
	t.Setenv("RASQL_TEST_MYSQL_DSN", "mysql://secret")
	t.Setenv("GOPROXY", "https://proxy.invalid")
	t.Setenv("GOSUMDB", "sumdb.invalid")
	t.Setenv("GOTOOLCHAIN", "go1.99")
	t.Setenv("GOFLAGS", "-mod=vendor")
	t.Setenv("GOCACHE", "stale-cache")
	t.Setenv("GOWORK", "stale.work")
	cache := filepath.Join(t.TempDir(), "cache")
	command := exec.Command(os.Args[0], "-test.run", "^TestOfflineBuildEnvChild$")
	command.Env = append(offlineBuildEnv(cache), "RASQL_D4_ENV_CHILD=1", "RASQL_D4_EXPECT_CACHE="+cache)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestOfflineBuildEnvChild(t *testing.T) {
	if os.Getenv("RASQL_D4_ENV_CHILD") != "1" {
		return
	}
	require.Empty(t, os.Getenv("RASQL_TEST_POSTGRES_DSN"))
	require.Empty(t, os.Getenv("RASQL_TEST_MYSQL_DSN"))
	require.Equal(t, "off", os.Getenv("GOPROXY"))
	require.Equal(t, "off", os.Getenv("GOSUMDB"))
	require.Equal(t, "local", os.Getenv("GOTOOLCHAIN"))
	require.Equal(t, "-mod=mod", os.Getenv("GOFLAGS"))
	require.Equal(t, "off", os.Getenv("GOWORK"))
	require.Equal(t, os.Getenv("RASQL_D4_EXPECT_CACHE"), os.Getenv("GOCACHE"))
	for _, name := range []string{"GOPROXY", "GOSUMDB", "GOTOOLCHAIN", "GOFLAGS", "GOWORK", "GOCACHE"} {
		count := 0
		for _, value := range os.Environ() {
			if strings.HasPrefix(value, name+"=") {
				count++
			}
		}
		require.Equal(t, 1, count, name)
	}
}

func writeBuildModule(root, repoRoot string) error {
	module := fmt.Sprintf("module example.test/generated\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nrequire modernc.org/sqlite v1.55.0\n\nreplace github.com/lestrrat-go/rasql => %s\n", filepath.ToSlash(repoRoot))
	return os.WriteFile(filepath.Join(root, "go.mod"), []byte(module), 0o600)
}

func writeConsumer(root, engine string, rasqlProgram bool) error {
	dir := "cmd/sql-evidence"
	text := handwrittenConsumer()
	if rasqlProgram {
		dir = "cmd/rasql-evidence"
		text = generatedConsumer(engine)
	}
	path := filepath.Join(root, dir)
	if err := os.MkdirAll(path, 0o755); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(path, "main.go"), []byte(text), 0o600)
}

func generatedConsumer(engine string) string {
	table := generatedTable()
	return fmt.Sprintf(`package main

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	generated "example.test/generated/internal/store"
	_ "modernc.org/sqlite"
)

var sink string

func main() {
	db, err := sql.Open("sqlite", "file:generated?mode=memory&cache=shared")
	if err != nil { panic(err) }
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("CREATE TABLE %s (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"); err != nil { panic(err) }
	if _, err = db.Exec("INSERT INTO %s (id, name) VALUES (1, 'fixture')"); err != nil { panic(err) }
	rdb, err := rasql.New(db, dialect.SQLite())
	if err != nil { panic(err) }
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	if err != nil { panic(err) }
	executor, err := rasql.AsExecutor(rdb, profile)
	if err != nil { panic(err) }
	source, err := generated.%s().Source("")
	if err != nil { panic(err) }
	expressions, err := (generated.%sColumns{}).Bind(source)
	if err != nil { panic(err) }
	projection, err := generated.%sProjection(expressions)
	if err != nil { panic(err) }
	rows, err := rasql.Rows(context.Background(), executor, rasql.Select(source.Source(), projection))
	if err != nil { panic(err) }
	count := 0
	for row, rowErr := range rows {
		if rowErr != nil { panic(rowErr) }
		if row.ID != 1 || row.Name != "fixture" { panic(fmt.Sprintf("unexpected row %%+v", row)) }
		count++
	}
	if count != 1 { panic(fmt.Sprintf("rows = %%d", count)) }
	sink = fmt.Sprintf("%%d:%%s", count, "fixture")
}

`, table, table, table, table, table)
}

func handwrittenConsumer() string {
	return `package main

import (
	"database/sql"
	"fmt"
	_ "modernc.org/sqlite"
)

var sink string

func main() {
	db, err := sql.Open("sqlite", "file:generated?mode=memory&cache=shared")
	if err != nil { panic(err) }
	defer db.Close()
	db.SetMaxOpenConns(1)
	if _, err = db.Exec("CREATE TABLE projects (id INTEGER PRIMARY KEY, name TEXT NOT NULL)"); err != nil { panic(err) }
	if _, err = db.Exec("INSERT INTO projects (id, name) VALUES (1, 'fixture')"); err != nil { panic(err) }
	rows, err := db.Query("SELECT id, name FROM projects ORDER BY id")
	if err != nil { panic(err) }
	count := 0
	for rows.Next() {
		var id int64
		var name string
		if err = rows.Scan(&id, &name); err != nil { panic(err) }
		if id != 1 || name != "fixture" { panic(fmt.Sprintf("unexpected row %d/%s", id, name)) }
		count++
	}
	if err = rows.Close(); err != nil { panic(err) }
	if err = rows.Err(); err != nil { panic(err) }
	if count != 1 { panic(fmt.Sprintf("rows = %d", count)) }
	sink = fmt.Sprintf("%d:%s", count, "fixture")
}
`
}

func generatedTable() string {
	return "Projects"
}

func runProgram(t *testing.T, path string) {
	t.Helper()
	output, err := exec.Command(path).CombinedOutput()
	require.NoError(t, err, "%s", output)
}

func identifiedBuildSample(t *testing.T, root, packagePath, name string, sample int, program string) buildSample {
	t.Helper()
	value := runBuild(t, root, packagePath, name)
	value.Sample = sample
	value.Program = program
	return value
}

func runBuild(t *testing.T, root, packagePath, name string) buildSample {
	t.Helper()
	binaryPath := filepath.Join(root, ".tmp", name)
	require.NoError(t, os.MkdirAll(filepath.Dir(binaryPath), 0o755))
	_ = os.Remove(binaryPath)
	command := exec.Command("go", "build", "-buildvcs=false", "-trimpath", "-o", binaryPath, packagePath)
	command.Dir = root
	command.Env = offlineBuildEnv(filepath.Join(root, ".gocache"))
	start := time.Now()
	commandOutput, err := command.CombinedOutput()
	require.NoError(t, err, "%s", commandOutput)
	state := command.ProcessState
	usage, ok := state.SysUsage().(*syscall.Rusage)
	require.True(t, ok)
	info, err := os.Stat(binaryPath)
	require.NoError(t, err)
	peakRSSBytes := usage.Maxrss
	if runtime.GOOS == "linux" {
		peakRSSBytes *= 1024
	}
	return buildSample{
		WallNS:       time.Since(start).Nanoseconds(),
		UserNS:       state.UserTime().Nanoseconds(),
		SystemNS:     state.SystemTime().Nanoseconds(),
		CPUNS:        state.UserTime().Nanoseconds() + state.SystemTime().Nanoseconds(),
		PeakRSSBytes: peakRSSBytes,
		BinaryBytes:  info.Size(),
	}
}

func writeBuildArtifact(t *testing.T, repoRoot string, manifest footprintManifest, files int, bytes, generationDurationNS int64, rasqlSamples, sqlSamples []buildSample) {
	t.Helper()
	commit := CommitFromEnvironment()
	require.NotEmpty(t, commit)
	data, err := json.MarshalIndent(buildArtifact{
		Format: "rasql.d4.generated-footprint.v1", Engine: manifest.Engine, Profile: manifest.Profile,
		GoVersion: runtime.Version(), GOOS: runtime.GOOS, GOARCH: runtime.GOARCH,
		Commit: commit, Workload: "generated compact SQLite read",
		CachePolicy:    "private per-fixture GOCACHE; GOPROXY=off; alternating five build pairs",
		GeneratedFiles: files, GeneratedBytes: bytes, GenerationDuration: generationDurationNS,
		RasqlMedian: medianBuildSample(rasqlSamples), SQLMedian: medianBuildSample(sqlSamples),
		RasqlSamples: rasqlSamples, SQLSamples: sqlSamples,
	}, "", "  ")
	require.NoError(t, err)
	require.Equal(t, manifest.GeneratedFiles, files)
	require.Equal(t, manifest.GeneratedBytes, bytes)
	dir := filepath.Join(repoRoot, ".tmp", "d4-generated-footprint")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	path := filepath.Join(dir, manifest.Engine+".json")
	require.NoError(t, os.WriteFile(path, append(data, '\n'), 0o600))
	stored, err := os.ReadFile(path)
	require.NoError(t, err)
	var decoded buildArtifact
	require.NoError(t, json.Unmarshal(stored, &decoded))
	require.Equal(t, "rasql.d4.generated-footprint.v1", decoded.Format)
	require.Equal(t, manifest.Engine, decoded.Engine)
	require.Equal(t, manifest.Profile, decoded.Profile)
	require.Equal(t, runtime.Version(), decoded.GoVersion)
	require.Equal(t, runtime.GOOS, decoded.GOOS)
	require.Equal(t, runtime.GOARCH, decoded.GOARCH)
	require.Equal(t, commit, decoded.Commit)
	require.NotEmpty(t, decoded.Workload)
	require.NotEmpty(t, decoded.CachePolicy)
	require.Equal(t, files, decoded.GeneratedFiles)
	require.Equal(t, bytes, decoded.GeneratedBytes)
	require.Positive(t, decoded.GenerationDuration)
	require.Len(t, decoded.RasqlSamples, 5)
	require.Len(t, decoded.SQLSamples, 5)
	require.Equal(t, medianBuildSample(decoded.RasqlSamples), decoded.RasqlMedian)
	require.Equal(t, medianBuildSample(decoded.SQLSamples), decoded.SQLMedian)
}

func medianBuildSample(values []buildSample) buildSample {
	return buildSample{
		Sample: 0, Program: "median", WallNS: medianInt64(values, func(value buildSample) int64 { return value.WallNS }),
		UserNS:       medianInt64(values, func(value buildSample) int64 { return value.UserNS }),
		SystemNS:     medianInt64(values, func(value buildSample) int64 { return value.SystemNS }),
		CPUNS:        medianInt64(values, func(value buildSample) int64 { return value.CPUNS }),
		PeakRSSBytes: medianInt64(values, func(value buildSample) int64 { return value.PeakRSSBytes }),
		BinaryBytes:  medianInt64(values, func(value buildSample) int64 { return value.BinaryBytes }),
	}
}

func medianInt64(values []buildSample, metric func(buildSample) int64) int64 {
	ordered := make([]int64, len(values))
	for index, value := range values {
		ordered[index] = metric(value)
	}
	sort.Slice(ordered, func(left, right int) bool { return ordered[left] < ordered[right] })
	return ordered[len(ordered)/2]
}
