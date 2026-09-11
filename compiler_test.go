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

// compilerSelect builds a query that reads one column of compiler_items, with
// a predicate so the statement carries a bound argument.
func compilerSelect(t *testing.T) rasql.Query[compilerCountRow] {
	t.Helper()
	table, err := rasql.ReadTableOf[compilerCountRow](schema.TableDef{
		Name:    "compiler_items",
		Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[compilerCountRow, int64](relation, "id", "")
	require.NoError(t, err)
	result, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection(
		[]rasql.ProjectionItem{rasql.Item("id", id.Expr(), schema.IntegerType{}, "")},
		compilerCountDecoder{result: result},
	)
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection).Where(rasql.EqualValue(id.Expr(), int64(7)))
}

type compilerCountRow struct{ ID int64 }

type compilerCountDecoder struct{ result rasql.ResultSchema }

func (d compilerCountDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (compilerCountDecoder) Presence() []rasql.Presence         { return nil }
func (compilerCountDecoder) DecodeRow(source rasql.ScanSource, row *compilerCountRow) error {
	return source.Scan(&row.ID)
}

func TestCompileQuery(t *testing.T) {
	t.Run("renders a read without a database", func(t *testing.T) {
		compiler := compilerFor(t, "postgresql-17", 17, 0, dialect.PostgreSQL())
		statement, err := rasql.CompileQuery(compiler, compilerSelect(t))
		require.NoError(t, err)
		require.Contains(t, statement.SQL(), "SELECT")
		require.Contains(t, statement.SQL(), "compiler_items")
		require.Equal(t, []any{int64(7)}, statement.Args())
	})

	// Render answers the same question for a dialect alone, so the two agree on
	// a query whose SQL the profile does not change.
	t.Run("agrees with Render where the profile does not matter", func(t *testing.T) {
		query := compilerSelect(t)
		compiler := compilerFor(t, "postgresql-17", 17, 0, dialect.PostgreSQL())
		compiled, err := rasql.CompileQuery(compiler, query)
		require.NoError(t, err)
		rendered, err := rasql.Render(query, dialect.PostgreSQL())
		require.NoError(t, err)
		require.Equal(t, rendered.SQL(), compiled.SQL())
		require.Equal(t, rendered.Args(), compiled.Args())
	})

	// A []byte is the value that would expose a shared buffer, and it cannot go
	// in a predicate because EqualValue needs a comparable type, so it is bound
	// as a projected value instead. Each compile hands back its own buffer, so
	// writing through one cannot change what a later compile of the same query
	// produces.
	t.Run("hands back bound values a caller cannot write through", func(t *testing.T) {
		result, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "payload", Type: schema.BytesType{}})
		require.NoError(t, err)
		projection, err := rasql.NewProjection(
			[]rasql.ProjectionItem{rasql.Item("payload", rasql.Value([]byte("original")), schema.BytesType{}, "")},
			compilerBytesDecoder{result: result},
		)
		require.NoError(t, err)
		table, err := rasql.ReadTableOf[compilerBytesRow](schema.TableDef{
			Name:    "compiler_items",
			Columns: []schema.ColumnDef{{Name: "payload", Type: schema.BytesType{}}},
		})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "")
		require.NoError(t, err)
		query := rasql.Select(relation.Source(), projection)

		compiler := compilerFor(t, "postgresql-17", 17, 0, dialect.PostgreSQL())
		first, err := rasql.CompileQuery(compiler, query)
		require.NoError(t, err)
		require.Equal(t, []byte("original"), first.BoundArgs()[0])
		first.BoundArgs()[0].([]byte)[0] = 'X'

		second, err := rasql.CompileQuery(compiler, query)
		require.NoError(t, err)
		require.Equal(t, []byte("original"), second.BoundArgs()[0])
	})

	t.Run("reports a compiler that carries no profile", func(t *testing.T) {
		var zero rasql.Compiler
		_, err := rasql.CompileQuery(zero, compilerSelect(t))
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "engine_profile_unavailable", planErr.Code)
	})

	t.Run("reports a query the profile refuses", func(t *testing.T) {
		compiler := compilerFor(t, "postgresql-17", 17, 0, dialect.PostgreSQL())
		_, err := rasql.CompileQuery(compiler, rasql.Query[compilerCountRow]{})
		require.Error(t, err)
	})
}

type compilerBytesRow struct{ Payload []byte }

type compilerBytesDecoder struct{ result rasql.ResultSchema }

func (d compilerBytesDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (compilerBytesDecoder) Presence() []rasql.Presence         { return nil }
func (compilerBytesDecoder) DecodeRow(source rasql.ScanSource, row *compilerBytesRow) error {
	return source.Scan(&row.Payload)
}

func mustRelation(t *testing.T, table rasql.Table[compilerRow]) rasql.TypedRelation[compilerRow] {
	t.Helper()
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	return relation
}
