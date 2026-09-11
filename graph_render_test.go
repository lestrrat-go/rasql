package rasql_test

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

// graphRenderExecutor answers the root query with one row and every later
// query with none, and keeps what it was asked to run. Rendering is what this
// test reads, so nothing here has to execute.
type graphRenderExecutor struct {
	dialect    dialect.Dialect
	mu         sync.Mutex
	statements []stmt.Statement
}

func (e *graphRenderExecutor) Dialect() dialect.Dialect { return e.dialect }

func (e *graphRenderExecutor) Query(_ context.Context, statement stmt.Statement) (rasql.ResultRows, error) {
	e.mu.Lock()
	e.statements = append(e.statements, statement)
	e.mu.Unlock()
	if strings.Contains(statement.SQL(), "graph_parents") {
		return &runtimeFakeRows{columns: []string{"id", "tenant"}, values: [][]any{{int64(1), int64(1)}}}, nil
	}
	return &runtimeFakeRows{columns: []string{"id", "parent", "tenant", "rank"}}, nil
}

func (*graphRenderExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return nil, nil
}

func (e *graphRenderExecutor) childStatement(t *testing.T) stmt.Statement {
	t.Helper()
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, statement := range e.statements {
		if strings.Contains(statement.SQL(), "graph_children") {
			return statement
		}
	}
	t.Fatal("no child statement was rendered")
	return stmt.Statement{}
}

func TestGraphPartitionRendering(t *testing.T) {
	_, parentSource, childSource, _, parentQuery, childQuery := graphAcceptanceFixture(t, 1)
	parents := rasql.Q1TypedRelation[graphParentRow](parentSource)
	children := rasql.Q1TypedRelation[graphChildRow](childSource)
	parentID, err := rasql.BindColumn[graphParentRow, int64](parents, "id", "")
	require.NoError(t, err)
	parentTenant, err := rasql.BindNullColumn[graphParentRow, int64](parents, "tenant", "")
	require.NoError(t, err)
	childParent, err := rasql.BindColumn[graphChildRow, int64](children, "parent", "")
	require.NoError(t, err)
	childTenant, err := rasql.BindColumn[graphChildRow, int64](children, "tenant", "")
	require.NoError(t, err)
	parentKey, err := rasql.NewGraphKey(rasql.KeyPart(parentID, func(row graphParentRow) int64 { return row.ID }), rasql.NullKeyPart(parentTenant, func(row graphParentRow) rasql.Nullable[int64] { return row.Tenant }))
	require.NoError(t, err)
	childKey, err := rasql.NewGraphKey(rasql.KeyPart(childParent, func(row graphChildRow) int64 { return row.Parent }), rasql.KeyPart(childTenant, func(row graphChildRow) int64 { return row.Tenant }))
	require.NoError(t, err)
	childPlan, err := rasql.NewGraphPlan(childQuery, func(row graphChildRow) graphChild { return graphChild{ID: row.ID} })
	require.NoError(t, err)
	edge, err := rasql.HasMany("children", parentKey, childKey, childPlan, rasql.EdgeOptions{
		Where: rasql.EqualValue(childTenant.Expr(), int64(1)), PerParentLimit: 5,
	}, func(parent *graphParent, loaded rasql.LoadedMany[graphChild]) { parent.Children = loaded })
	require.NoError(t, err)
	rootQuery, err := parentQuery.Limit(1)
	require.NoError(t, err)
	plan, err := rasql.NewGraphPlan(rootQuery, func(row graphParentRow) graphParent { return graphParent{ID: row.ID} }, edge)
	require.NoError(t, err)

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
			profile, err := rasql.EngineProfileFromVersion(tc.profile, tc.major, tc.minor, 0)
			require.NoError(t, err)
			raw := &graphRenderExecutor{dialect: tc.dialect}
			executor, err := rasql.WithEngineProfile(raw, profile)
			require.NoError(t, err)
			_, err = rasql.LoadGraph(t.Context(), executor, plan)
			require.NoError(t, err)
			statement := raw.childStatement(t)
			upper := strings.ToUpper(statement.SQL())
			require.Contains(t, upper, "ROW_NUMBER() OVER")
			require.Contains(t, upper, "PARTITION BY")
			require.Contains(t, upper, "WHERE")
			require.Contains(t, upper, "<=")
			require.NotContains(t, upper, "__RASQL_ROW_NUMBER")
			require.GreaterOrEqual(t, len(statement.Args()), 2)
		})
	}
}

func TestGraphLiveProfile(t *testing.T) {
	t.Run("PostgreSQL", func(t *testing.T) {
		database := dbtest.PostgreSQLDB(t)
		db, err := rasql.New(database, dialect.PostgreSQL())
		require.NoError(t, err)
		profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "postgresql-17")
		require.NoError(t, err)
		require.Equal(t, rasql.PostgreSQLEngine, profile.Engine())
		require.NotEqual(t, rasql.EnginePerParentLimitUnsupported, profile.Capabilities().PerParentLimit)
	})

	t.Run("MySQL", func(t *testing.T) {
		database := dbtest.MySQLDB(t)
		db, err := rasql.New(database, dialect.MySQL())
		require.NoError(t, err)
		profile, err := rasql.DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
		require.NoError(t, err)
		require.Equal(t, rasql.MySQLEngine, profile.Engine())
		require.NotEqual(t, rasql.EnginePerParentLimitUnsupported, profile.Capabilities().PerParentLimit)
	})
}
