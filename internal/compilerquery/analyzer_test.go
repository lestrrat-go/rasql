package compilerquery_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"

	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// TestAnalyzeSnapshotsQueryAndNamesItsPath proves one analyzed query comes back with the
// module-relative path it was read from, both on the query itself and on the snapshot the caller
// revalidates before it writes anything.
func TestAnalyzeSnapshotsQueryAndNamesItsPath(t *testing.T) {
	db := sqliteUsersDB(t)
	result, err := analyzeSQLiteQuery(t, db, `SELECT id FROM users WHERE id = {{bind "id" users.id}}`)
	require.NoError(t, err)
	require.Len(t, result.Queries, 1)
	require.Len(t, result.Snapshots, 1)
	require.Equal(t, "q.sql", result.Queries[0].SQLPath)
	require.Equal(t, "q.sql", result.Snapshots[0].Path())
}

// TestAnalyzeTypesEveryValueFromItsDeclaration proves the declaration is the only source of a
// value's Go-facing type: the scalar is carried through, its logical kind comes from the scalar
// alone, and no engine observation is attached to either value.
func TestAnalyzeTypesEveryValueFromItsDeclaration(t *testing.T) {
	db := sqliteUsersDB(t)
	result, err := analyzeSQLiteQuery(t, db, `SELECT id FROM users WHERE id = {{bind "id" users.id}} OR id = {{bind "id" users.id}}`)
	require.NoError(t, err)
	require.Len(t, result.Queries, 1)
	parameter, value := result.Queries[0].Parameters[0], result.Queries[0].Results[0]
	require.Equal(t, compilerir.CertaintyDeclared, parameter.TypeCertainty)
	require.Equal(t, compilerir.CertaintyDeclared, value.TypeCertainty)
	require.Equal(t, compilerir.CertaintyDeclared, value.NullabilityCertainty)
	require.Equal(t, "integer", value.LogicalKind)
	require.Nil(t, parameter.Native)
	require.Nil(t, value.Integer)
}

// TestAnalyzeRefusesABlankScalarOnEveryEngine pins the rule every engine now shares: a value with
// no scalar has no Go type to generate, and no engine is asked for one.
func TestAnalyzeRefusesABlankScalarOnEveryEngine(t *testing.T) {
	for _, engine := range []struct {
		name    string
		profile string
		version engineprofile.Version
	}{
		{"sqlite", "sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35}},
		{"postgresql", "postgresql-17", engineprofile.Version{Known: true, Major: 17}},
		{"mysql", "mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4}},
	} {
		t.Run(engine.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(root, "q.sql"), []byte("SELECT id FROM users"), 0o600))
			nullable := false
			config := compilerquery.Config{ModuleRoot: root, Queries: []compilerquery.QueryConfig{{
				ID: "q", Input: "q.sql", Engine: engine.name, Function: "Q", Operation: "select", Cardinality: "many",
				Results: []compilerquery.ValueDeclaration{{Name: "id", Nullable: &nullable}},
			}}}
			analyzer, err := compilerquery.NewAnalyzer(config)
			require.NoError(t, err)
			profile, err := engineprofile.Builtin(engine.profile, engine.version)
			require.NoError(t, err)
			_, err = analyzer.Analyze(t.Context(), schemasource.AnalysisRequest{Profile: profile})
			require.ErrorContains(t, err, `query "q" value "id" requires a scalar declaration`)
		})
	}
}

// TestAnalyzeRefusesAScalarWithNoLogicalKind proves a scalar the mappings do not define is
// refused rather than passed through to the emitter.
func TestAnalyzeRefusesAScalarWithNoLogicalKind(t *testing.T) {
	db := sqliteUsersDB(t)
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "q.sql"), []byte("SELECT id FROM users"), 0o600))
	nullable := false
	config := compilerquery.Config{ModuleRoot: root, Queries: []compilerquery.QueryConfig{{
		ID: "q", Input: "q.sql", Engine: "sqlite", Function: "Q", Operation: "select", Cardinality: "many",
		Results: []compilerquery.ValueDeclaration{{Name: "id", Scalar: "custom", Nullable: &nullable}},
	}}}
	analyzer, err := compilerquery.NewAnalyzer(config)
	require.NoError(t, err)
	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	_, err = analyzer.Analyze(t.Context(), schemasource.AnalysisRequest{Profile: profile, DB: db})
	require.ErrorContains(t, err, `scalar "custom" has no declared logical kind`)
}

// analyzeSQLiteQuery analyzes sqlText as one SQLite select taking an "id" parameter and returning
// an "id" result, against db. Both values declare their scalar, which every engine now requires.
func analyzeSQLiteQuery(t *testing.T, db *sql.DB, sqlText string) (schemasource.AnalysisResult, error) {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "q.sql"), []byte(sqlText), 0o600))
	nullable := false
	config := compilerquery.Config{ModuleRoot: root, Queries: []compilerquery.QueryConfig{{
		ID: "q", Input: "q.sql", Engine: "sqlite", Function: "Q", Operation: "select", Cardinality: "many",
		Parameters: []compilerquery.ValueDeclaration{{Name: "id", Scalar: "integer", Nullable: &nullable}},
		Results:    []compilerquery.ValueDeclaration{{Name: "id", Scalar: "integer", Nullable: &nullable}},
	}}}
	analyzer, err := compilerquery.NewAnalyzer(config)
	require.NoError(t, err)
	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	return analyzer.Analyze(t.Context(), schemasource.AnalysisRequest{Profile: profile, DB: db})
}

// sqliteUsersDB opens an in-memory SQLite database holding the one table the queries in this
// package's tests name, so the analyzer's prepare has a real schema to plan against.
func sqliteUsersDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")
	require.NoError(t, err)
	return db
}
