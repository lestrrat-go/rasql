package rasql_test

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type compilerRow struct{ ID, Name, Count int64 }

func compilerTable(t *testing.T) (rasql.Table[compilerRow], rasql.Column[compilerRow, int64], rasql.Column[compilerRow, int64]) {
	t.Helper()
	table, err := rasql.TableOf[compilerRow](schema.TableDef{
		Name: "compiler_items", PrimaryKey: []string{"id"},
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "name", Type: schema.IntegerType{}, Default: "1"},
			{Name: "count", Type: schema.IntegerType{}, Default: "2"},
		},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[compilerRow, int64](relation, "id", "")
	require.NoError(t, err)
	name, err := rasql.BindColumn[compilerRow, int64](relation, "name", "")
	require.NoError(t, err)
	return table, id, name
}

func compilerFor(t *testing.T, id string, major, minor int, d dialect.Dialect) rasql.Compiler {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion(id, major, minor, 0)
	require.NoError(t, err)
	compiler, err := profile.Compiler(d)
	require.NoError(t, err)
	return compiler
}

func TestCompilerMutation(t *testing.T) {
	t.Run("renders a patch without a database", func(t *testing.T) {
		table, id, name := compilerTable(t)
		plan, err := rasql.NewPatchPlan(table, rasql.EqualValue(id.Expr(), int64(1)), rasql.SetField(name, int64(3)))
		require.NoError(t, err)

		compiler := compilerFor(t, "postgresql-17", 17, 0, dialect.PostgreSQL())
		statement, err := compiler.Mutation(plan)
		require.NoError(t, err)
		require.Contains(t, statement.SQL(), "UPDATE")
		require.Contains(t, statement.SQL(), "compiler_items")
		require.Equal(t, []any{int64(3), int64(1)}, statement.Args())
	})

	// UPDATE ... SET col = DEFAULT is the case that proves the profile is
	// consulted rather than only the dialect: SQLite speaks SQL that looks the
	// same, and refuses this statement on its capabilities alone.
	t.Run("applies the engine profile capabilities, not just the dialect", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			dialect dialect.Dialect
			profile string
			major   int
			minor   int
			fails   bool
		}{
			{name: "postgresql", dialect: dialect.PostgreSQL(), profile: "postgresql-17", major: 17},
			{name: "mysql", dialect: dialect.MySQL(), profile: "mysql-8.4", major: 8, minor: 4},
			{name: "sqlite", dialect: dialect.SQLite(), profile: "sqlite-3.35", major: 3, minor: 35, fails: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				table, id, name := compilerTable(t)
				plan, err := rasql.NewPatchPlan(table, rasql.EqualValue(id.Expr(), int64(1)),
					rasql.DefaultField(name), rasql.SetField(id, int64(2)))
				require.NoError(t, err)

				compiler := compilerFor(t, tc.profile, tc.major, tc.minor, tc.dialect)
				statement, err := compiler.Mutation(plan)
				if tc.fails {
					require.ErrorIs(t, err, rasql.ErrUnsupportedEngineFeature)
					return
				}
				require.NoError(t, err)
				require.Contains(t, statement.SQL(), "DEFAULT")
				require.Len(t, statement.Args(), 2)
			})
		}
	})

	// The whole point of rendering without a database is that it shows what
	// execution would send. A hook reports the statement that actually reached
	// database/sql, and it has to match the rendered one exactly.
	t.Run("renders what execution sends", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		database.SetMaxOpenConns(1)
		t.Cleanup(func() { require.NoError(t, database.Close()) })
		_, err = database.ExecContext(t.Context(),
			`CREATE TABLE compiler_items (id INTEGER PRIMARY KEY, name INTEGER NOT NULL DEFAULT 1, count INTEGER NOT NULL DEFAULT 2)`)
		require.NoError(t, err)
		_, err = database.ExecContext(t.Context(), `INSERT INTO compiler_items (id, name) VALUES (1, 1)`)
		require.NoError(t, err)

		var sentSQL string
		var sentArgs []any
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		db, err = db.WithHooks(rasql.HookFunc{
			BeforeFunc: func(_ context.Context, operation rasql.Operation) error {
				sentSQL, sentArgs = operation.SQL(), operation.Args()
				return nil
			},
		})
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)

		table, id, name := compilerTable(t)
		plan, err := rasql.NewPatchPlan(table, rasql.EqualValue(id.Expr(), int64(1)), rasql.SetField(name, int64(7)))
		require.NoError(t, err)

		compiler, err := profile.Compiler(dialect.SQLite())
		require.NoError(t, err)
		rendered, err := compiler.Mutation(plan)
		require.NoError(t, err)

		outcome, err := rasql.ExecMutation(t.Context(), executor, plan)
		require.NoError(t, err)
		require.Equal(t, int64(1), outcome.Affected)

		require.Equal(t, rendered.SQL(), sentSQL)
		require.Equal(t, rendered.Args(), sentArgs)
	})

	t.Run("renders every plan family", func(t *testing.T) {
		table, id, name := compilerTable(t)
		compiler := compilerFor(t, "postgresql-17", 17, 0, dialect.PostgreSQL())

		create, err := rasql.NewCreatePlan(table, rasql.SetField(id, int64(1)), rasql.SetField(name, int64(2)))
		require.NoError(t, err)
		statement, err := compiler.Mutation(create)
		require.NoError(t, err)
		require.Contains(t, statement.SQL(), "INSERT")

		remove, err := rasql.NewDeletePlan(table,
			query.EqualValue(query.TypedColumnOf[compilerRow, int64](table.Column("id")), int64(1)))
		require.NoError(t, err)
		statement, err = compiler.Mutation(remove)
		require.NoError(t, err)
		require.Contains(t, statement.SQL(), "DELETE")
	})

	// BoundArgs exposes the statement's own slice, where Args hands back a
	// clone. Reading through BoundArgs is what shows each render detaches its
	// values, rather than showing only that Args copies.
	t.Run("hands back bound values a caller cannot write through", func(t *testing.T) {
		table, id, _ := compilerTable(t)
		payload, err := rasql.BindColumn[compilerRow, []byte](
			mustRelation(t, table), "name", "")
		require.NoError(t, err)
		plan, err := rasql.NewPatchPlan(table, rasql.EqualValue(id.Expr(), int64(1)),
			rasql.SetField(payload, []byte("original")))
		require.NoError(t, err)

		compiler := compilerFor(t, "postgresql-17", 17, 0, dialect.PostgreSQL())
		first, err := compiler.Mutation(plan)
		require.NoError(t, err)
		require.Equal(t, []byte("original"), first.BoundArgs()[0])
		first.BoundArgs()[0].([]byte)[0] = 'X'

		second, err := compiler.Mutation(plan)
		require.NoError(t, err)
		require.Equal(t, []byte("original"), second.BoundArgs()[0])
	})

	t.Run("reports a profile that does not match the dialect", func(t *testing.T) {
		profile, err := rasql.EngineProfileFromVersion("postgresql-17", 17, 0, 0)
		require.NoError(t, err)
		_, err = profile.Compiler(dialect.MySQL())
		require.ErrorIs(t, err, rasql.ErrEngineProfileMismatch)
		_, err = profile.Compiler(nil)
		require.ErrorIs(t, err, rasql.ErrInvalidEngineProfile)
	})

	t.Run("reports a compiler that carries no profile", func(t *testing.T) {
		table, id, name := compilerTable(t)
		plan, err := rasql.NewPatchPlan(table, rasql.EqualValue(id.Expr(), int64(1)), rasql.SetField(name, int64(3)))
		require.NoError(t, err)

		var zero rasql.Compiler
		_, err = zero.Mutation(plan)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "engine_profile_unavailable", planErr.Code)
		require.Equal(t, rasql.EngineProfile{}, zero.EngineProfile())
	})

	// A zero plan never came from a constructor and carries no table. Before
	// this was guarded, both Mutation and ExecMutation dereferenced it and
	// crashed, so each family is checked here rather than only the one that
	// happened to be noticed.
	t.Run("reports a plan that was never built", func(t *testing.T) {
		compiler := compilerFor(t, "postgresql-17", 17, 0, dialect.PostgreSQL())
		_, err := compiler.Mutation(nil)
		require.Error(t, err)

		plans := map[string]rasql.MutationPlan{
			"patch":  rasql.PatchPlan[compilerRow]{},
			"create": rasql.CreatePlan[compilerRow]{},
			"delete": rasql.DeletePlan[compilerRow]{},
			"upsert": rasql.UpsertPlan[compilerRow]{},
		}
		for name, plan := range plans {
			t.Run(name, func(t *testing.T) {
				_, err := compiler.Mutation(plan)
				require.Error(t, err)
			})
		}
	})

	// A native mutation already holds the SQL it will send, so there is no
	// write statement for a compiler to render and the plan says so itself.
	t.Run("reports a native mutation, which carries its own SQL", func(t *testing.T) {
		plan, err := rasql.NativeMutation(rasql.NativeStatement{
			Engine: "postgresql",
			SQL:    "DELETE FROM compiler_items WHERE id = $1",
			Args:   []rasql.NativeArgument{{Value: int64(1)}},
		})
		require.NoError(t, err)

		compiler := compilerFor(t, "postgresql-17", 17, 0, dialect.PostgreSQL())
		_, err = compiler.Mutation(plan)
		require.Error(t, err)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_feature", planErr.Code)
	})
}

func mustRelation(t *testing.T, table rasql.Table[compilerRow]) rasql.TypedRelation[compilerRow] {
	t.Helper()
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	return relation
}
