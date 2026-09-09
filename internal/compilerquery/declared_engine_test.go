package compilerquery_test

import (
	"database/sql"
	"os"
	"path/filepath"
	"testing"
	"time"

	_ "github.com/go-sql-driver/mysql"
	"github.com/lestrrat-go/rasql/internal/compilerir"
	"github.com/lestrrat-go/rasql/internal/compilerquery"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/schemasource"
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestAnalyzeDeclaredOnlySQLitePreparesRealQuery(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	_, err = db.ExecContext(t.Context(), "CREATE TABLE users (id INTEGER PRIMARY KEY, name TEXT)")
	require.NoError(t, err)
	result := analyzeDeclaredEngine(t, db, "sqlite", "sqlite-3.35", querydescribe.NewSQLitePrepare(db), "SELECT id, name FROM users WHERE id = {{bind \"id\" users.id}}", "id", "integer", "id", "integer")
	require.Len(t, result.Queries, 1)
	require.Equal(t, compilerir.CertaintyDeclared, result.Queries[0].Parameters[0].TypeCertainty)
	require.Equal(t, compilerir.CertaintyDeclared, result.Queries[0].Results[0].TypeCertainty)
	require.Equal(t, compilerir.CertaintyDeclared, result.Queries[0].Results[1].NullabilityCertainty)
}

func TestAnalyzeDeclaredOnlyMySQLPreparesLiveQuery(t *testing.T) {
	dsn := os.Getenv("RASQL_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("RASQL_TEST_MYSQL_DSN is not set")
	}
	db, err := sql.Open("mysql", dsn)
	require.NoError(t, err)
	t.Cleanup(func() { _ = db.Close() })
	require.NoError(t, db.PingContext(t.Context()))
	table := "rasql_g4_declared_" + time.Now().Format("20060102150405")
	_, err = db.ExecContext(t.Context(), "CREATE TABLE `"+table+"` (id BIGINT PRIMARY KEY, name VARCHAR(32))")
	require.NoError(t, err)
	t.Cleanup(func() { _, _ = db.ExecContext(t.Context(), "DROP TABLE `"+table+"`") })
	result := analyzeDeclaredEngine(t, db, "mysql", "mysql-8.4", querydescribe.NewMySQL(db), "SELECT id, name FROM `"+table+"` WHERE id = {{bind \"id\" "+table+".id}}", "id", "integer", "id", "integer")
	require.Len(t, result.Queries, 1)
	require.Equal(t, compilerir.CertaintyDeclared, result.Queries[0].Parameters[0].TypeCertainty)
	require.Equal(t, compilerir.CertaintyDeclared, result.Queries[0].Results[1].TypeCertainty)
}

func analyzeDeclaredEngine(t *testing.T, db *sql.DB, engine, profileID string, describer compilerquery.Describer, sqlText, parameterName, parameterScalar, resultName, resultScalar string) schemasource.AnalysisResult {
	t.Helper()
	root := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(root, "q.sql"), []byte(sqlText), 0o600))
	nullable := false
	a, err := compilerquery.NewAnalyzer(compilerquery.Config{ModuleRoot: root, Queries: []compilerquery.QueryConfig{{ID: "q", Input: "q.sql", Engine: engine, Function: "Q", Operation: "select", Cardinality: "many", Parameters: []compilerquery.ValueDeclaration{{Name: parameterName, Scalar: parameterScalar, Nullable: &nullable}}, Results: []compilerquery.ValueDeclaration{{Name: resultName, Scalar: resultScalar, Nullable: &nullable}, {Name: "name", Scalar: "text", Nullable: &nullable}}}}}, compilerquery.Describers{SQLite: describer, MySQL: describer})
	require.NoError(t, err)
	version := engineprofile.Version{Known: true, Major: 8, Minor: 4}
	if engine == "sqlite" {
		version = engineprofile.Version{Known: true, Major: 3, Minor: 35}
	}
	profile, err := engineprofile.Builtin(profileID, version)
	require.NoError(t, err)
	returnResult, err := a.Analyze(t.Context(), schemasource.AnalysisRequest{Profile: profile, DB: db})
	require.NoError(t, err)
	return returnResult
}
