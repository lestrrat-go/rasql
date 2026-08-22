package generate_test

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/internal/genfile"
	"github.com/lestrrat-go/rasql/internal/querygen"
	"github.com/lestrrat-go/rasql/namedsql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

func TestQueryPackageWritesOnlyGeneratedQueries(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "user_by_email.sql", `SELECT id, email FROM users WHERE email = {{bind "email"}}`)
	out := filepath.Join(root, "internal", "store")
	require.NoError(t, os.MkdirAll(out, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(out, "handwritten.go"), []byte("package store\n"), 0o600))

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     filepath.Join("internal", "store"),
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{
			Input:    "user_by_email.sql",
			Function: "UserByEmail",
			Output:   "user_by_email_gen.go",
		}},
	}
	require.NoError(t, queries.Write())

	generatedPath := filepath.Join(out, "user_by_email_gen.go")
	generated, err := os.ReadFile(generatedPath)
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(string(generated), genfile.Marker+"\n"))
	require.Contains(t, string(generated), "func UserByEmail(email any)")
	require.NotContains(t, string(generated), "schema.TableDef")
	require.FileExists(t, filepath.Join(out, "handwritten.go"))
	require.NoError(t, queries.Check())
}

func TestQueryPackageOutputIsDeterministic(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "z.sql", `SELECT id FROM users WHERE id = {{bind "id"}}`)
	writeQueryFile(t, root, "a.sql", `SELECT id FROM users WHERE id = {{bind "id"}}`)

	first := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store-one",
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{
			{Input: "z.sql", Function: "Zed", Output: "z_gen.go"},
			{Input: "a.sql", Function: "Alpha", Output: "a_gen.go"},
		},
	}
	second := first
	second.Dir = "store-two"
	second.Queries = append([]generate.Query(nil), first.Queries[0], first.Queries[1])

	firstPlan, err := first.Plan()
	require.NoError(t, err)
	secondPlan, err := second.Plan()
	require.NoError(t, err)
	require.Len(t, firstPlan.Files(), 2)
	require.Equal(t, filepath.Base(firstPlan.Files()[0].Path), "a_gen.go")
	require.Equal(t, filepath.Base(firstPlan.Files()[1].Path), "z_gen.go")
	for index, file := range firstPlan.Files() {
		require.Equal(t, file.Source, secondPlan.Files()[index].Source)
	}
}

func TestQueryPackageMatchesTemplateOutput(t *testing.T) {
	root := t.TempDir()
	source := `SELECT id FROM users WHERE id = {{bind "id"}}`
	writeQueryFile(t, root, "user.sql", source)

	plan, err := (generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{Input: "user.sql", Function: "UserByID", Output: "user_gen.go"}},
	}).Plan()
	require.NoError(t, err)

	parsed, err := namedsql.Parse("UserByID", source)
	require.NoError(t, err)
	compiled, err := parsed.Compile(dialect.PostgreSQL())
	require.NoError(t, err)
	want, err := querygen.GoSource(compiled.QueryDef(), "store", "UserByID")
	require.NoError(t, err)
	require.Equal(t, want, plan.Files()[0].Source)
}

func TestQueryPackageCheckReportsStaleInput(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "user.sql")
	writeQueryFile(t, root, "user.sql", `SELECT id FROM users WHERE id = {{bind "id"}}`)
	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{Input: "user.sql", Function: "UserByID", Output: "user_gen.go"}},
	}
	require.NoError(t, queries.Write())
	require.NoError(t, os.WriteFile(input, []byte(`SELECT email FROM users WHERE id = {{bind "id"}}`), 0o600))

	err := queries.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale), err)
	require.Contains(t, err.Error(), "user_gen.go")
}

func TestQueryPlanCommitKeepsRelativeRootInputPathStable(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)

	t.Chdir(filepath.Dir(root))
	queries := generate.QueryPackage{
		Package: "store",
		Root:    filepath.Base(root),
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
	}
	plan, err := queries.Plan()
	require.NoError(t, err)

	t.Chdir(outside)
	require.NoError(t, plan.Commit())
	require.FileExists(t, filepath.Join(root, "store", "query_gen.go"))
}

func TestQueryPlanCheckReportsOversizedOutputWithoutReadingItAll(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "user.sql", `SELECT id FROM users WHERE id = {{bind "id"}}`)
	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{Input: "user.sql", Function: "UserByID", Output: "user_gen.go"}},
	}
	require.NoError(t, queries.Write())

	plan, err := queries.Plan()
	require.NoError(t, err)
	files := plan.Files()
	require.Len(t, files, 1)

	output, err := os.OpenFile(files[0].Resolved, os.O_WRONLY|os.O_TRUNC, 0)
	require.NoError(t, err)
	t.Cleanup(func() { _ = output.Close() })
	_, err = output.Write(files[0].Source)
	require.NoError(t, err)
	_, err = output.Seek(8<<30, io.SeekStart)
	require.NoError(t, err)
	_, err = output.Write([]byte("x"))
	require.NoError(t, err)
	require.NoError(t, output.Close())

	err = plan.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale), err)
	require.Contains(t, err.Error(), "user_gen.go: differs")
}

func TestQueryPackageCheckReportsMarkedGenGoAndGenTestOrphansAndWriteRefusesThem(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "first.sql", `SELECT id FROM users`)
	writeQueryFile(t, root, "second.sql", `SELECT email FROM users`)
	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "first.sql", Function: "First", Output: "first_gen.go"}},
	}
	require.NoError(t, queries.Write())
	testOrphan := filepath.Join(root, "store", "first_gen_test.go")
	require.NoError(t, os.WriteFile(testOrphan, []byte(genfile.Marker+"\n\npackage store\n"), 0o600))

	queries.Queries = []generate.Query{{Input: "second.sql", Function: "Second", Output: "second_gen.go"}}
	plan, err := queries.Plan()
	require.NoError(t, err)
	orphans := plan.Orphans()
	require.Equal(t, []string{filepath.Join(root, "store", "first_gen.go"), testOrphan}, orphans)
	orphans[0] = "mutated"
	require.Equal(t, filepath.Join(root, "store", "first_gen.go"), plan.Orphans()[0])

	err = queries.Check()
	require.Error(t, err)
	require.True(t, errors.Is(err, generate.ErrStale), err)
	require.Contains(t, err.Error(), "first_gen.go")
	require.Contains(t, err.Error(), "first_gen_test.go")

	err = queries.Write()
	require.Error(t, err)
	require.Contains(t, err.Error(), "first_gen.go")
	require.Contains(t, err.Error(), "first_gen_test.go")
	require.FileExists(t, filepath.Join(root, "store", "first_gen.go"))
	require.FileExists(t, testOrphan)
	require.NoFileExists(t, filepath.Join(root, "store", "second_gen.go"))
}

func TestQueryPackageRejectsForeignPackageBeforeWriting(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	writeQueryFile(t, root, "other.sql", `SELECT 2`)
	dir := filepath.Join(root, "store")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foreign.go"), []byte("package other\n"), 0o600))
	before := snapshotDir(t, dir)
	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{
			{Input: "query.sql", Function: "Query", Output: "query_gen.go"},
			{Input: "other.sql", Function: "Other", Output: "other_gen.go"},
		},
	}

	_, err := queries.Plan()
	require.Error(t, err)
	require.Contains(t, err.Error(), `already holds package "other"`)
	require.Error(t, queries.Write())
	require.Equal(t, before, snapshotDir(t, dir))
	require.NoFileExists(t, filepath.Join(dir, "query_gen.go"))
	require.NoFileExists(t, filepath.Join(dir, "other_gen.go"))
}

func TestQueryPackageRejectsMarkedForeignPackageBeforeWriting(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	dir := filepath.Join(root, "store")
	require.NoError(t, os.MkdirAll(dir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "foreign.go"), []byte(genfile.Marker+"\n\npackage other\n"), 0o600))

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
	}

	_, err := queries.Plan()
	require.ErrorContains(t, err, `already holds package "other"`)
	require.NoFileExists(t, filepath.Join(dir, "query_gen.go"))
}

func TestQueryPackageRejectsExistingSamePackageDeclarationBeforeWriting(t *testing.T) {
	tests := []struct {
		name   string
		source string
	}{
		{name: "function", source: "package store\n\nfunc Query() {}\n"},
		{name: "type", source: "package store\n\ntype Query struct{}\n"},
		{name: "const", source: "package store\n\nconst Query = 1\n"},
		{name: "var", source: "package store\n\nvar Query = 1\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeQueryFile(t, root, "query.sql", `SELECT 1`)
			dir := filepath.Join(root, "store")
			require.NoError(t, os.MkdirAll(dir, 0o700))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "handwritten.go"), []byte(test.source), 0o600))
			queries := generate.QueryPackage{
				Package: "store",
				Root:    root,
				Dir:     "store",
				Dialect: dialect.SQLite(),
				Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
			}

			_, err := queries.Plan()
			require.ErrorContains(t, err, `query function "Query" collides with package-level declaration "Query" in handwritten.go`)
			require.ErrorContains(t, queries.Write(), `query function "Query" collides with package-level declaration "Query" in handwritten.go`)
			require.NoFileExists(t, filepath.Join(dir, "query_gen.go"))
		})
	}
}

func TestQueryPackageAuthorizesAllDestinationsBeforeWriting(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "first.sql", `SELECT id FROM users`)
	writeQueryFile(t, root, "second.sql", `SELECT email FROM users`)
	out := filepath.Join(root, "store")
	require.NoError(t, os.MkdirAll(out, 0o700))

	initial := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "first.sql", Function: "First", Output: "first_gen.go"}},
	}
	require.NoError(t, initial.Write())
	before, err := os.ReadFile(filepath.Join(out, "first_gen.go"))
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(out, "second_gen.go"), []byte("package store\n"), 0o600))

	withHandWrittenDestination := initial
	withHandWrittenDestination.Queries = []generate.Query{
		{Input: "first.sql", Function: "First", Output: "first_gen.go"},
		{Input: "second.sql", Function: "Second", Output: "second_gen.go"},
	}
	err = withHandWrittenDestination.Write()
	require.Error(t, err)
	require.Contains(t, err.Error(), "second_gen.go")
	after, readErr := os.ReadFile(filepath.Join(out, "first_gen.go"))
	require.NoError(t, readErr)
	require.Equal(t, before, after)
}

func TestQueryPlanCheckAndCommitRefuseHandwrittenDestinationBeforePublishing(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "first.sql", `SELECT id FROM users`)
	writeQueryFile(t, root, "second.sql", `SELECT email FROM users`)
	out := filepath.Join(root, "store")
	require.NoError(t, os.MkdirAll(out, 0o700))
	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{
			{Input: "first.sql", Function: "First", Output: "first_gen.go"},
			{Input: "second.sql", Function: "Second", Output: "second_gen.go"},
		},
	}
	plan, err := queries.Plan()
	require.NoError(t, err)
	firstPath := filepath.Join(out, "first_gen.go")
	secondPath := filepath.Join(out, "second_gen.go")
	unpublished := []byte(genfile.Marker + "\n\npackage store\n\nfunc Unpublished() {}\n")
	require.NoError(t, os.WriteFile(firstPath, unpublished, 0o600))
	require.NoError(t, os.WriteFile(secondPath, []byte("package store\n"), 0o600))

	err = plan.Check()
	require.ErrorContains(t, err, "second_gen.go")
	require.ErrorContains(t, err, "was not generated by rasqlgen")
	require.False(t, errors.Is(err, generate.ErrStale), "an unauthorized destination is a refusal, not staleness")
	afterCheck, readErr := os.ReadFile(firstPath)
	require.NoError(t, readErr)
	require.Equal(t, unpublished, afterCheck)

	err = plan.Commit()
	require.ErrorContains(t, err, "second_gen.go")
	require.ErrorContains(t, err, "was not generated by rasqlgen")
	afterCommit, readErr := os.ReadFile(firstPath)
	require.NoError(t, readErr)
	require.Equal(t, unpublished, afterCommit)
}

func TestQueryPlanCommitRejectsPostPlanOwnershipChanges(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(t *testing.T, dir string)
		want   string
	}{
		{
			name: "foreign package",
			mutate: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "foreign.go"), []byte("package other\n"), 0o600))
			},
			want: "already holds package \"other\"",
		},
		{
			name: "same package declaration",
			mutate: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "handwritten.go"), []byte("package store\n\nfunc Query() {}\n"), 0o600))
			},
			want: `query function "Query" collides`,
		},
		{
			name: "generated orphan",
			mutate: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "old_gen.go"), []byte(genfile.Marker+"\n\npackage store\n"), 0o600))
			},
			want: "gained 1 file(s)",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeQueryFile(t, root, "query.sql", `SELECT 1`)
			queries := generate.QueryPackage{
				Package: "store",
				Root:    root,
				Dir:     "store",
				Dialect: dialect.SQLite(),
				Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
			}
			plan, err := queries.Plan()
			require.NoError(t, err)
			test.mutate(t, filepath.Join(root, "store"))

			err = plan.Commit()
			require.ErrorContains(t, err, test.want)
			require.ErrorContains(t, err, "rerun QueryPackage.Plan")
			require.NoFileExists(t, filepath.Join(root, "store", "query_gen.go"))
		})
	}
}

func TestQueryPlanCheckRejectsPostPlanOwnershipChanges(t *testing.T) {
	tests := []struct {
		name         string
		mutate       func(t *testing.T, dir string)
		want         string
		requiresPlan bool
	}{
		{
			name: "foreign package",
			mutate: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "foreign.go"), []byte("package other\n"), 0o600))
			},
			want:         "already holds package \"other\"",
			requiresPlan: true,
		},
		{
			name: "same package declaration",
			mutate: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "handwritten.go"), []byte("package store\n\nfunc Query() {}\n"), 0o600))
			},
			want:         `query function "Query" collides`,
			requiresPlan: true,
		},
		{
			name: "generated orphan",
			mutate: func(t *testing.T, dir string) {
				require.NoError(t, os.MkdirAll(dir, 0o700))
				require.NoError(t, os.WriteFile(filepath.Join(dir, "old_gen.go"), []byte(genfile.Marker+"\n\npackage store\n"), 0o600))
			},
			want: "is an orphaned generated file",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeQueryFile(t, root, "query.sql", `SELECT 1`)
			queries := generate.QueryPackage{
				Package: "store",
				Root:    root,
				Dir:     "store",
				Dialect: dialect.SQLite(),
				Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
			}
			plan, err := queries.Plan()
			require.NoError(t, err)
			test.mutate(t, filepath.Join(root, "store"))

			err = plan.Check()
			require.ErrorContains(t, err, test.want)
			if test.requiresPlan {
				require.ErrorContains(t, err, "rerun QueryPackage.Plan")
			}
		})
	}
}

func TestQueryPlanCommitRejectsChangedQueryInput(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
	}
	require.NoError(t, queries.Write())
	outputPath := filepath.Join(root, "store", "query_gen.go")
	plannedOutput, err := os.ReadFile(outputPath)
	require.NoError(t, err)
	plan, err := queries.Plan()
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(root, "query.sql"), []byte(`SELECT 2`), 0o600))

	err = plan.Commit()
	require.ErrorContains(t, err, "query input query.sql changed after QueryPackage.Plan")
	require.ErrorContains(t, err, "rerun QueryPackage.Plan")
	currentOutput, readErr := os.ReadFile(outputPath)
	require.NoError(t, readErr)
	require.Equal(t, plannedOutput, currentOutput)
}

func TestQueryPlanCheckAndCommitRejectChangedQueryInputBeforePublishing(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "first.sql", `SELECT 1`)
	writeQueryFile(t, root, "second.sql", `SELECT 2`)
	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{
			{Input: "first.sql", Function: "First", Output: "first_gen.go"},
			{Input: "second.sql", Function: "Second", Output: "second_gen.go"},
		},
	}
	require.NoError(t, queries.Write())
	plan, err := queries.Plan()
	require.NoError(t, err)
	outputPath := filepath.Join(root, "store", "first_gen.go")
	unpublished := []byte(genfile.Marker + "\n\npackage store\n\nfunc Unpublished() {}\n")
	require.NoError(t, os.WriteFile(outputPath, unpublished, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(root, "first.sql"), []byte(`SELECT 3`), 0o600))

	err = plan.Check()
	require.ErrorContains(t, err, "query input first.sql changed after QueryPackage.Plan")
	require.ErrorContains(t, err, "rerun QueryPackage.Plan")
	require.False(t, errors.Is(err, generate.ErrStale), "a changed input invalidates the plan before stale output comparison")
	afterCheck, readErr := os.ReadFile(outputPath)
	require.NoError(t, readErr)
	require.Equal(t, unpublished, afterCheck)

	err = plan.Commit()
	require.ErrorContains(t, err, "query input first.sql changed after QueryPackage.Plan")
	require.ErrorContains(t, err, "rerun QueryPackage.Plan")
	afterCommit, readErr := os.ReadFile(outputPath)
	require.NoError(t, readErr)
	require.Equal(t, unpublished, afterCommit)
}

func TestQueryPackageRejectsInvalidQueriesBeforeWriting(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "query.sql", `SELECT 1`)
	writeQueryFile(t, root, "invalid.sql", `SELECT {{broken}}`)
	base := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{Input: "query.sql", Function: "Query", Output: "query_gen.go"}},
	}
	tests := []struct {
		name    string
		mutate  func(*generate.QueryPackage)
		message string
	}{
		{name: "empty queries", mutate: func(p *generate.QueryPackage) { p.Queries = nil }, message: "at least one query"},
		{name: "missing template", mutate: func(p *generate.QueryPackage) { p.Queries[0].Input = "" }, message: "query input or sql is required"},
		{name: "both input and sql", mutate: func(p *generate.QueryPackage) { p.Queries[0].SQL = "SELECT 1" }, message: "query input and sql cannot both be set"},
		{name: "missing function", mutate: func(p *generate.QueryPackage) { p.Queries[0].Function = "" }, message: "query function is required"},
		{name: "unexported function", mutate: func(p *generate.QueryPackage) { p.Queries[0].Function = "userByID" }, message: "must be an exported Go identifier"},
		{name: "invalid function", mutate: func(p *generate.QueryPackage) { p.Queries[0].Function = "123User" }, message: "must be an exported Go identifier"},
		{name: "unsafe output path", mutate: func(p *generate.QueryPackage) { p.Queries[0].Output = "../query_gen.go" }, message: "must be a file name"},
		{name: "wrong output suffix", mutate: func(p *generate.QueryPackage) { p.Queries[0].Output = "query.go" }, message: "must end in _gen.go"},
		{name: "invalid source", mutate: func(p *generate.QueryPackage) { p.Queries[0].Input = "invalid.sql" }, message: "validate query"},
		{name: "missing dialect", mutate: func(p *generate.QueryPackage) { p.Dialect = nil }, message: "dialect must not be nil"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := base
			candidate.Queries = append([]generate.Query(nil), base.Queries...)
			test.mutate(&candidate)
			_, err := candidate.Plan()
			require.Error(t, err)
			require.Contains(t, err.Error(), test.message)
			err = candidate.Write()
			require.Error(t, err)
			require.Contains(t, err.Error(), test.message)
			require.NoFileExists(t, filepath.Join(root, "store", "query_gen.go"))
		})
	}
}

func TestQueryPackageRejectsCollisions(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "one.sql", `SELECT 1`)
	writeQueryFile(t, root, "two.sql", `SELECT 2`)
	base := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{
			{Input: "one.sql", Function: "One", Output: "one_gen.go"},
			{Input: "two.sql", Function: "Two", Output: "two_gen.go"},
		},
	}

	duplicateOutput := base
	duplicateOutput.Queries = append([]generate.Query(nil), base.Queries...)
	duplicateOutput.Queries[1].Output = "ONE_gen.go"
	_, err := duplicateOutput.Plan()
	require.ErrorContains(t, err, "collides")

	duplicateFunction := base
	duplicateFunction.Queries = append([]generate.Query(nil), base.Queries...)
	duplicateFunction.Queries[1].Function = "One"
	_, err = duplicateFunction.Plan()
	require.ErrorContains(t, err, "collides")
}

func writeQueryFile(t *testing.T, root, name, source string) {
	t.Helper()
	require.NoError(t, os.WriteFile(filepath.Join(root, name), []byte(source), 0o600))
}

// TestQueryPackageCompilesInlineQuery requires a query carrying its template
// in SQL to generate the same function a query naming a file generates, and
// requires Check to stay green with no template file anywhere on disk.
func TestQueryPackageCompilesInlineQuery(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "from_file.sql", `SELECT id FROM users WHERE id = {{bind "id"}}`)

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{
			{Input: "from_file.sql", Function: "FromFile", Output: "from_file_gen.go"},
			{SQL: `SELECT id FROM users WHERE id = {{bind "id"}}`, Function: "Inline", Output: "inline_gen.go"},
		},
	}
	require.NoError(t, queries.Write())

	fromFile, err := os.ReadFile(filepath.Join(root, "store", "from_file_gen.go"))
	require.NoError(t, err)
	inline, err := os.ReadFile(filepath.Join(root, "store", "inline_gen.go"))
	require.NoError(t, err)
	require.Equal(t,
		strings.Replace(string(fromFile), "func FromFile(", "func Inline(", 1),
		string(inline),
		"the same template must render the same function body whether it comes from a file or from SQL")
	require.NoError(t, queries.Check())
}

// TestQueryPackageTypesBindsFromTables requires a QueryPackage carrying
// Tables to generate a typed parameter for a bind that names a column,
// exactly as Store does.
func TestQueryPackageTypesBindsFromTables(t *testing.T) {
	root := t.TempDir()
	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.PostgreSQL(),
		Tables:  []schema.TableDef{usersTableDef()},
		Queries: []generate.Query{{
			SQL:      `SELECT id FROM users WHERE email = {{bind "email" users.email}}`,
			Function: "UserByEmail",
			Output:   "user_by_email_gen.go",
		}},
	}

	require.NoError(t, queries.Write())
	generated, err := os.ReadFile(filepath.Join(root, "store", "user_by_email_gen.go"))
	require.NoError(t, err)
	require.Contains(t, string(generated), "func UserByEmail(email string)")
}

// TestQueryPackageRejectsTypedBindWithoutTables pins the "error, never a
// silent any" decision on the standalone path: a QueryPackage with no
// Tables cannot resolve a bind that names a column, and Plan fails naming
// the unresolvable reference rather than falling back to any.
func TestQueryPackageRejectsTypedBindWithoutTables(t *testing.T) {
	root := t.TempDir()
	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.PostgreSQL(),
		Queries: []generate.Query{{
			SQL:      `SELECT id FROM users WHERE email = {{bind "email" users.email}}`,
			Function: "UserByEmail",
			Output:   "user_by_email_gen.go",
		}},
	}

	_, err := queries.Plan()
	require.ErrorContains(t, err, "users.email")
	require.ErrorContains(t, err, "no table descriptors were supplied")
}

// TestQueryPackageInlineQuerySurvivesADeletedQueryDirectory requires an inline
// template to be independent of the filesystem: a plan takes no snapshot of
// it, so removing every .sql file leaves the commit valid.
func TestQueryPackageInlineQuerySurvivesADeletedQueryDirectory(t *testing.T) {
	root := t.TempDir()
	writeQueryFile(t, root, "unused.sql", `SELECT 1`)

	queries := generate.QueryPackage{
		Package: "store",
		Root:    root,
		Dir:     "store",
		Dialect: dialect.SQLite(),
		Queries: []generate.Query{{SQL: `SELECT id FROM users WHERE id = {{bind "id"}}`, Function: "Inline", Output: "inline_gen.go"}},
	}
	plan, err := queries.Plan()
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, "unused.sql")))

	require.NoError(t, plan.Commit())
	require.FileExists(t, filepath.Join(root, "store", "inline_gen.go"))
}
