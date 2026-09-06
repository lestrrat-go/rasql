package generate_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/generate"
	"github.com/lestrrat-go/rasql/querydescribe"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

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
