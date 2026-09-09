package conformance

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/scratchmod"
	"github.com/stretchr/testify/require"
)

func TestGeneratedOverdueCardinality(t *testing.T) {
	seed := SeedRows()
	for _, expected := range []struct {
		id  int64
		due string
	}{{1, "2024-01-01"}, {3, "2024-01-03"}} {
		var found SeedRow
		for _, row := range seed {
			if row.ID == expected.id {
				found = row
				break
			}
		}
		require.Equal(t, expected.id, found.ID)
		require.Equal(t, int64(1), found.ProjectID)
		require.NotNil(t, found.AssigneeID)
		require.Equal(t, expected.id, *found.AssigneeID)
		require.NotNil(t, found.DueOn)
		require.Equal(t, expected.due, *found.DueOn)
		require.True(t, found.Open)
		require.Equal(t, fmt.Sprintf("task-%04d", expected.id), found.Title)
		require.Equal(t, "2024-01-01T00:00:00Z", found.CreatedAt)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	require.NoError(t, err)
	root := filepath.Join(t.TempDir(), "fixture")
	require.NoError(t, copyGeneratedStore(filepath.Join("testdata", "sqlite", "internal", "store"), filepath.Join(root, "internal", "store")))
	// The fixture's own go.mod starts from the repository's, rather than
	// naming modernc.org/sqlite's version by hand: a hand-picked version
	// drifts from whatever go.mod actually pins and, under CI's
	// GOPROXY=off, fails to resolve once it does.
	require.NoError(t, scratchmod.Write(root, repoRoot, "example.test/generated"))
	require.NoError(t, os.WriteFile(filepath.Join(root, "cardinality_test.go"), []byte(generatedCardinalityTest()), 0o600))
	command := exec.Command("go", "test", "-run", "^TestGeneratedCardinalityRuntime$")
	command.Dir = root
	// GOCACHE is deliberately shared, not rooted under t.TempDir(): see
	// sharedOfflineGOCACHE's comment in generation_test.go.
	command.Env = offlineBuildEnv(sharedOfflineGOCACHE)
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}

func generatedCardinalityTest() string {
	return `package generated_test

import (
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"example.test/generated/internal/store"
	_ "modernc.org/sqlite"
)

func TestGeneratedCardinalityRuntime(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil { t.Fatal(err) }
	defer db.Close()
	db.SetMaxOpenConns(1)
	_, err = db.Exec("CREATE TABLE tasks (id INTEGER PRIMARY KEY, project_id INTEGER NOT NULL, assignee_id INTEGER, title TEXT NOT NULL, is_open BOOLEAN NOT NULL, due_on DATE, created_at TIMESTAMP NOT NULL)")
	if err != nil { t.Fatal(err) }
	for _, row := range []struct{id int64; due time.Time; assignee any}{
		{1, time.Date(2024,1,1,0,0,0,0,time.UTC), int64(1)}, {3, time.Date(2024,1,3,0,0,0,0,time.UTC), int64(3)},
	} {
		_, err = db.Exec("INSERT INTO tasks(id, project_id, assignee_id, title, is_open, due_on, created_at) VALUES (?, 1, ?, ?, 1, ?, ?)", row.id, row.assignee, fmt.Sprintf("task-%04d", row.id), row.due, "2024-01-01T00:00:00Z")
		if err != nil { t.Fatal(err) }
	}
	raw, err := rasql.New(db, dialect.SQLite())
	if err != nil { t.Fatal(err) }
	profile, err := rasql.DiscoverEngineProfile(t.Context(), raw, "sqlite-3.35")
	if err != nil { t.Fatal(err) }
	executor, err := rasql.AsExecutor(raw, profile)
	if err != nil { t.Fatal(err) }
	cutoffs := map[string]time.Time{"zero": time.Date(2024,1,1,0,0,0,0,time.UTC), "one": time.Date(2024,1,2,0,0,0,0,time.UTC), "two": time.Date(2024,1,4,0,0,0,0,time.UTC)}
	for name, cutoff := range cutoffs {
		one, err := store.OverdueTask(1, true, cutoff)
		if err != nil { t.Fatal(err) }
		row, err := rasql.One(t.Context(), executor, one)
		if name == "zero" { if !errors.Is(err, rasql.ErrNoRows) { t.Fatalf("one zero: %v", err) } }
		if name == "one" { if err != nil { t.Fatal(err) }; assertRow(t, row, 1) }
		if name == "two" { if !errors.Is(err, rasql.ErrMultipleRows) { t.Fatalf("one two: %v", err) } }
		if name == "zero" || name == "two" {
			allQuery, err := store.OverdueTask(1, true, cutoff); if err != nil { t.Fatal(err) }
			_, allErr := rasql.All(t.Context(), executor, allQuery); if name == "zero" && !errors.Is(allErr, rasql.ErrNoRows) { t.Fatalf("one all zero: %v", allErr) }; if name == "two" && !errors.Is(allErr, rasql.ErrMultipleRows) { t.Fatalf("one all two: %v", allErr) }
		}

		maybe, err := store.MaybeOverdueTask(1, true, cutoff)
		if err != nil { t.Fatal(err) }
		maybeRow, found, err := rasql.Maybe(t.Context(), executor, maybe)
		if name == "zero" { if err != nil || found || !reflect.ValueOf(maybeRow).IsZero() { t.Fatalf("maybe zero: %v %t", err, found) } }
		if name == "one" { if err != nil || !found { t.Fatalf("maybe one: %v %t", err, found) }; assertRow(t, maybeRow, 1) }
		if name == "two" { if !errors.Is(err, rasql.ErrMultipleRows) { t.Fatalf("maybe two: %v", err) } }
		if name == "zero" || name == "two" {
			allQuery, err := store.MaybeOverdueTask(1, true, cutoff); if err != nil { t.Fatal(err) }
			allRows, allErr := rasql.All(t.Context(), executor, allQuery); if name == "zero" && (allErr != nil || len(allRows) != 0) { t.Fatalf("maybe all zero: %v %d", allErr, len(allRows)) }; if name == "two" && !errors.Is(allErr, rasql.ErrMultipleRows) { t.Fatalf("maybe all two: %v", allErr) }
		}

		many, err := store.OverdueTasks(1, true, cutoff)
		if err != nil { t.Fatal(err) }
		rows, err := rasql.All(t.Context(), executor, many)
		if err != nil { t.Fatal(err) }
		want := map[string][]int64{"zero": nil, "one": []int64{1}, "two": []int64{1, 3}}[name]
		if len(rows) != len(want) { t.Fatalf("many %s: got %d rows, want %d", name, len(rows), len(want)) }
		for index, id := range want { assertRow(t, rows[index], id) }
	}
}

func assertRow(t *testing.T, row any, id int64) {
	t.Helper(); value := reflect.ValueOf(row)
	if value.FieldByName("ID").Interface() != id || value.FieldByName("ProjectID").Interface() != int64(1) { t.Fatalf("unexpected key fields: %#v", row) }
	if value.FieldByName("Title").Interface() != fmt.Sprintf("task-%04d", id) || value.FieldByName("IsOpen").Interface() != true { t.Fatalf("unexpected text fields: %#v", row) }
	if !reflect.DeepEqual(value.FieldByName("AssigneeID").Interface(), rasql.Nullable[int64]{Value:id, Valid:true}) { t.Fatalf("unexpected assignee: %#v", row) }
	if !reflect.DeepEqual(value.FieldByName("DueOn").Interface(), rasql.Nullable[time.Time]{Value:time.Date(2024,1,int(id),0,0,0,0,time.UTC), Valid:true}) { t.Fatalf("unexpected due: %#v", row) }
	if value.FieldByName("CreatedAt").Interface() != time.Date(2024,1,1,0,0,0,0,time.UTC) { t.Fatalf("unexpected created: %#v", row) }
}
`
}
