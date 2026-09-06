package generate_test

import (
	"context"
	"database/sql"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type fixedDescriber struct{ description querydescribe.Description }

func (d fixedDescriber) Describe(context.Context, querydescribe.Request) (querydescribe.Description, error) {
	return d.description, nil
}

type mutatingDescriber struct{ description querydescribe.Description }

func (d mutatingDescriber) Describe(_ context.Context, request querydescribe.Request) (querydescribe.Description, error) {
	if len(request.Parameters) > 0 {
		request.Parameters[0] = "mutated"
	}
	if len(request.Tables) > 0 && len(request.Tables[0].Columns) > 0 {
		request.Tables[0].Columns[0].Name = "mutated"
	}
	if request.Expected != nil && len(request.Expected.Columns) > 0 {
		request.Expected.Columns[0].Binding.Imports = append(request.Expected.Columns[0].Binding.Imports, schema.GoImport{Path: "mutated"})
	}
	return d.description, nil
}

func TestQueryPackagePlanContextRejectsRemovedResultColumn(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE users(id INTEGER PRIMARY KEY)")
	require.NoError(t, err)
	packagePlan := generate.QueryPackage{Package: "queries", Dir: t.TempDir(), Dialect: dialect.SQLite(), Queries: []generate.Query{{Function: "Users", Output: "users_gen.go", SQL: "SELECT deleted_column FROM users", Describer: querydescribe.NewSQLite(db)}}}
	_, err = packagePlan.PlanContext(t.Context())
	require.Error(t, err)
	require.Contains(t, err.Error(), "deleted_column")
}

func TestQueryPackageRegeneratedTypeCompilesAndRejectsStaleCaller(t *testing.T) {
	root, err := filepath.Abs("..")
	require.NoError(t, err)
	dir := t.TempDir()
	makeDescription := func(typ string) querydescribe.Description {
		return querydescribe.Description{Columns: []querydescribe.Column{{Name: "id", Binding: schema.GoBinding{Type: typ}}}}
	}
	build := func(typ string) []byte {
		plan, planErr := (generate.QueryPackage{Package: "queries", Root: root, Dir: dir, Dialect: dialect.SQLite(), Queries: []generate.Query{{Function: "Report", Output: "report_gen.go", SQL: "SELECT 1", Describer: fixedDescriber{description: makeDescription(typ)}}}}).PlanContext(t.Context())
		require.NoError(t, planErr)
		require.Len(t, plan.Files(), 1)
		return plan.Files()[0].Source
	}
	first := build("int64")
	second := build("string")
	require.NotEqual(t, first, second)
	module := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(module, "go.mod"), []byte("module example.com/consumer\n\ngo 1.26\n\nrequire github.com/lestrrat-go/rasql v0.0.0\nreplace github.com/lestrrat-go/rasql => "+root+"\n"), 0o644))
	require.NoError(t, os.Mkdir(filepath.Join(module, "queries"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(module, "queries", "report_gen.go"), second, 0o644))
	caller := []byte("package queries\n\nvar _ int64 = ReportRow{}.ID\n")
	require.NoError(t, os.WriteFile(filepath.Join(module, "queries", "caller_test.go"), caller, 0o644))
	cmd := exec.Command("go", "test", "-mod=mod", "./...")
	cmd.Dir = module
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".tmp", "gocache"))
	output, err := cmd.CombinedOutput()
	require.Error(t, err, string(output))
	require.NoError(t, os.WriteFile(filepath.Join(module, "queries", "caller_test.go"), []byte("package queries\n\nvar _ string = ReportRow{}.ID\n"), 0o644))
	cmd = exec.Command("go", "test", "-mod=mod", "./...")
	cmd.Dir = module
	cmd.Env = append(os.Environ(), "GOCACHE="+filepath.Join(root, ".tmp", "gocache"))
	output, err = cmd.CombinedOutput()
	require.NoError(t, err, string(output))
	_ = first
}

func TestQueryPackagePlanClonesDescriptionAndChecksInputFreshness(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/plan\n\ngo 1.26\n"), 0o644))
	input := filepath.Join(root, "report.sql")
	require.NoError(t, os.WriteFile(input, []byte("SELECT 1"), 0o644))
	description := querydescribe.Description{Columns: []querydescribe.Column{{Name: "id", Binding: schema.GoBinding{Type: "int64"}}}}
	query := generate.Query{Function: "Report", Output: "report_gen.go", Input: input, Describer: fixedDescriber{description: description}}
	plan, err := (generate.QueryPackage{Package: "plan", Root: root, Dir: filepath.Join(root, "queries"), Dialect: dialect.SQLite(), Queries: []generate.Query{query}}).PlanContext(t.Context())
	require.NoError(t, err)
	description.Columns[0].Binding.Imports = append(description.Columns[0].Binding.Imports, schema.GoImport{Path: "mutated"})
	files := plan.Files()
	require.NotContains(t, string(files[0].Source), "mutated")
	require.NoError(t, os.WriteFile(input, []byte("SELECT 2"), 0o644))
	require.Error(t, plan.Commit())
}

func TestTypedResultNamesCannotCollideInStoreOrQueryPackage(t *testing.T) {
	description := querydescribe.Description{Columns: []querydescribe.Column{{Name: "id", Binding: schema.GoBinding{Type: "int64"}}}}
	queries := []generate.Query{{Function: "First", Output: "first_gen.go", SQL: "SELECT 1", ResultType: "SharedRow", Describer: fixedDescriber{description: description}}, {Function: "Second", Output: "second_gen.go", SQL: "SELECT 1", ResultType: "SharedRow", Describer: fixedDescriber{description}}}
	_, err := (generate.QueryPackage{Package: "queries", Dir: t.TempDir(), Dialect: dialect.SQLite(), Queries: queries}).PlanContext(t.Context())
	require.Error(t, err)
	_, err = (generate.Store{Package: "queries", Dir: t.TempDir(), Dialect: dialect.SQLite(), Tables: []schema.TableDef{{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}}, Queries: queries}).PlanContext(t.Context())
	require.Error(t, err)
}

func TestResultDescriptionSnapshotsRequestAndReturnedSlices(t *testing.T) {
	table := schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}
	expected := &querydescribe.Description{Columns: []querydescribe.Column{{Name: "id", Binding: schema.GoBinding{Type: "int64"}}}}
	description := querydescribe.Description{Columns: []querydescribe.Column{{Name: "id", Binding: schema.GoBinding{Type: "int64"}}}}
	query := generate.Query{Function: "Report", Output: "report_gen.go", SQL: "SELECT {{bind \"id\" users.id}}", Bindings: map[string]namedsql.ParameterBinding{"id": {Go: schema.GoBinding{Type: "int64"}}}, Describer: mutatingDescriber{description: description}, Expected: expected}
	plan, err := (generate.Store{Package: "queries", Dir: t.TempDir(), Dialect: dialect.SQLite(), Tables: []schema.TableDef{table}, Queries: []generate.Query{query}}).PlanContext(t.Context())
	require.NoError(t, err)
	description.Columns[0].Binding.Imports = append(description.Columns[0].Binding.Imports, schema.GoImport{Path: "mutated-result"})
	require.Equal(t, "id", table.Columns[0].Name)
	require.Equal(t, "int64", expected.Columns[0].Binding.Type)
	require.NotContains(t, string(plan.Files()[0].Source), "mutated-result")
}

func TestStorePlanContextChecksQueryInputFreshnessBeforePublication(t *testing.T) {
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/store\n\ngo 1.26\n"), 0o644))
	input := filepath.Join(root, "report.sql")
	require.NoError(t, os.WriteFile(input, []byte("SELECT 1"), 0o644))
	store := generate.Store{Package: "store", Root: root, Dir: filepath.Join(root, "generated"), Dialect: dialect.SQLite(), Tables: []schema.TableDef{{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}}}, Queries: []generate.Query{{Function: "Report", Output: "report_gen.go", Input: input}}}
	plan, err := store.PlanContext(t.Context())
	require.NoError(t, err)
	before := plan.Files()
	require.NotEmpty(t, before)
	require.NoError(t, os.WriteFile(input, []byte("SELECT 2"), 0o644))
	require.Error(t, plan.Check())
	require.Error(t, plan.Commit())
	_, err = os.Stat(filepath.Join(root, "generated"))
	require.Error(t, err)
}
