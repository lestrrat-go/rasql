package rasql

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type graphRenderExecutor struct {
	dialect  dialect.Dialect
	compiler *querycompile.Compiler
}

func (e graphRenderExecutor) Dialect() dialect.Dialect { return e.dialect }
func (graphRenderExecutor) Query(context.Context, stmt.Statement) (ResultRows, error) {
	return nil, nil
}
func (graphRenderExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) { return nil, nil }
func (e graphRenderExecutor) queryCompiler() *querycompile.Compiler                  { return e.compiler }

func TestGraphPartitionRendering(t *testing.T) {
	_, _, childSource, _, _, childQuery := graphAcceptanceFixture(t, 1)
	children := TypedRelation[graphChildRow]{source: childSource}
	childParent, err := BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	childKey, err := NewGraphKey(KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	graphChildQuery := graphQuery[graphChildRow, graphChild]{value: childQuery, mapFn: func(row graphChildRow) graphChild { return graphChild{ID: row.ID} }}
	membership, err := buildGraphMembership(childKey.key, []keyTuple{{components: []keyComponent{{value: int64(1)}, {value: int64(1)}}}})
	require.NoError(t, err)
	limited, err := graphChildQuery.withOptions(EdgeOptions{Where: EqualValue(childTenant.Expr(), int64(1)), PerParentLimit: 5}, childKey.key, 5)
	require.NoError(t, err)
	limited = limited.withMembership(membership)

	for _, tc := range []struct {
		name    string
		dialect dialect.Dialect
		profile string
		major   int
		minor   int
	}{
		{name: "sqlite", dialect: dialect.SQLite(), profile: "sqlite-3.35", major: 3, minor: 35},
		{name: "postgresql", dialect: dialect.PostgreSQL(), profile: "postgresql-17", major: 17},
		{name: "mysql", dialect: dialect.MySQL(), profile: "mysql-8.4", major: 8, minor: 4},
	} {
		t.Run(tc.name, func(t *testing.T) {
			profile, err := EngineProfileFromVersion(tc.profile, tc.major, tc.minor, 0)
			require.NoError(t, err)
			compiler, err := profile.queryCompiler(tc.dialect)
			require.NoError(t, err)
			executor := graphRenderExecutor{dialect: tc.dialect, compiler: compiler}
			compiled, err := limited.compile(executor)
			require.NoError(t, err)
			sqlText := compiled.statement.SQL()
			upper := strings.ToUpper(sqlText)
			require.Contains(t, upper, "ROW_NUMBER() OVER")
			require.Contains(t, upper, "PARTITION BY")
			require.Contains(t, upper, "WHERE")
			require.Contains(t, upper, "<=")
			require.NotContains(t, upper, "__RASQL_ROW_NUMBER")
			require.GreaterOrEqual(t, len(compiled.statement.Args()), 2)
		})
	}
}

func TestGraphLiveProfile(t *testing.T) {
	t.Run("PostgreSQL", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)
		db, err := New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		profile, err := DiscoverEngineProfile(t.Context(), db, "postgresql-17")
		require.NoError(t, err)
		require.Equal(t, PostgreSQLEngine, profile.Engine())
		require.NotEqual(t, EnginePerParentLimitUnsupported, profile.Capabilities().PerParentLimit)
	})

	t.Run("MySQL", func(t *testing.T) {
		database := dbtest.MySQLDB(t)
		db, err := New(database, dialect.MySQL())
		require.NoError(t, err)
		profile, err := DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
		require.NoError(t, err)
		require.Equal(t, MySQLEngine, profile.Engine())
		require.NotEqual(t, EnginePerParentLimitUnsupported, profile.Capabilities().PerParentLimit)
	})
}
