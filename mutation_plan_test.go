package rasql_test

import (
	"context"
	"database/sql"
	"errors"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestMutationCompiles(t *testing.T) {
	positive, err := filepath.Abs(filepath.Join("testdata", "compile", "mutation_api", "positive"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = positive
	// GOCACHE is deliberately not overridden here: see the matching comment
	// in query_composition_internal_test.go's TestQueryAPICompiles.
	command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("positive mutation fixture failed: %v\n%s", err, output)
	}
	for _, fixture := range []struct{ name, diagnostic string }{
		{"wrong_value", "cannot use"},
		{"wrong_nullable", "does not satisfy"},
		{"clear_nonnull", "does not satisfy"},
		{"foreign_row", "does not match"},
		{"version_type", "cannot use"},
	} {
		negative, err := filepath.Abs(filepath.Join("testdata", "compile", "mutation_api", fixture.name))
		if err != nil {
			t.Fatal(err)
		}
		command = exec.Command("go", "test", "./...")
		command.Dir = negative
		command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), fixture.diagnostic) {
			t.Fatalf("negative mutation fixture %s diagnostic = %s", fixture.name, output)
		}
	}
}

type capabilityGuardRow struct {
	ID int64 `rasql:"id"`
}

// capabilityGuardTable is a Table[T] TableOf and MustTableOf would both
// refuse: it describes a read-only view. TableFrom is the one entry point
// that skips that check, which is exactly how a forged handle reaches
// NewCreatePlan or NewPatchPlan without ever passing through TableOf: it is
// the entry point rasqlgen uses for a descriptor it has already validated
// itself, so it validates nothing about the descriptor it is given here.
func capabilityGuardTable(t *testing.T) rasql.Table[capabilityGuardRow] {
	t.Helper()
	return rasql.TableFrom[capabilityGuardRow](schema.TableDef{
		Name:       "active_users",
		Kind:       schema.ObjectView,
		Operations: schema.OperationRead,
		Columns:    []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}},
	})
}

func TestMutationPlanValidation(t *testing.T) {
	// TestMutationPlanValidation/"a forged table handle rejects insert and update" reconstructs the case
	// TestTableCapabilities/"a forged writable handle rejects every mutation" (table_test.go,
	// removed from this branch because it also exercised rasql.Insert and
	// rasql.Update helpers this redesign does not carry forward) proved for
	// every mutation kind: a Table[T] whose schema does not support the
	// operation must be refused at the plan constructor, not just at TableOf.
	//
	// Confirmed by running both subtests against the unmodified constructors:
	// neither NewCreatePlan nor NewPatchPlan called requireTableOperation at
	// all, so both returned a nil error for a plan built against a read-only
	// view, where NewDeletePlan on the same handle already reported
	// `rasql: object "active_users" does not support operation 2`.
	t.Run("a forged table handle rejects insert and update", func(t *testing.T) {
		table := capabilityGuardTable(t)
		id := query.TypedColumnOf[capabilityGuardRow, int64](table.Column("id"))

		t.Run("insert", func(t *testing.T) {
			field := rasql.SetField[capabilityGuardRow, int64](id, 1)
			_, err := rasql.NewCreatePlan(table, field)
			require.ErrorContains(t, err, "does not support operation")
		})

		t.Run("update", func(t *testing.T) {
			field := rasql.SetField[capabilityGuardRow, int64](id, 1)
			where := query.EqualValue(id, int64(1))
			_, err := rasql.NewPatchPlan(table, where, field)
			require.ErrorContains(t, err, "does not support operation")
		})
	})
}

func TestExecMutation(t *testing.T) {
	// TestExecMutation/"rows affected survives a hook failure" pins the third
	// defect: ExecMutation discarded executor.Exec's sql.Result whenever it
	// returned a non-nil error at all, even though *ExtensionError reports that
	// the driver call underneath it succeeded. A hook failing after a write that
	// really happened should still let the caller learn how many rows it
	// affected, the same way exec.DB.ExecRendered already hands the result back
	// alongside a joined hook error.
	//
	// Confirmed by running this test against the unmodified ExecMutation: it
	// returned MutationOutcome{Durability: DurabilityUnknown} (Affected: 0)
	// alongside the hook error, instead of the affected row count below.
	t.Run("rows affected survives a hook failure", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		require.NoError(t, err)
		t.Cleanup(func() {
			mock.ExpectClose()
			require.NoError(t, database.Close())
			require.NoError(t, mock.ExpectationsWereMet())
		})

		hookErr := errors.New("export failed")
		hook := rasql.HookFunc{AfterFunc: func(context.Context, rasql.Operation, error) error { return hookErr }}
		db, err := rasql.New(database, dialect.SQLite(), hook)
		require.NoError(t, err)

		table, err := query.NewTableRef(schema.TableDef{
			Name:    "users",
			Columns: []schema.ColumnDef{{Name: "email", Type: schema.TextType{}}},
		})
		require.NoError(t, err)
		insert, err := query.NewInsert(table, query.Set(table.Column("email"), "ada@example.com"))
		require.NoError(t, err)
		mock.ExpectExec("INSERT INTO").WithArgs("ada@example.com").WillReturnResult(sqlmock.NewResult(1, 1))

		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		plan, err := rasql.NewStatementPlan(insert)
		require.NoError(t, err)

		outcome, err := rasql.ExecMutation(t.Context(), executor, plan)
		require.Error(t, err)
		var extensionErr *rasql.ExtensionError
		require.ErrorAs(t, err, &extensionErr)
		require.True(t, extensionErr.ExecutionSucceeded())
		require.Equal(t, int64(1), outcome.Affected)
		require.Equal(t, rasql.DurabilityCommitted, outcome.Durability)
	})
}

type g5MutationRow struct {
	ID, Name, Count int64
	Flag            g5FailingBind
}

var errG5Snapshot = errors.New("g5 snapshot failed")

// g5FailingBind refuses to snapshot, so binding one through any public
// comparison puts errG5Snapshot in the predicate's bind error. That is how a
// test reaches the bind-error path without building a Predicate by hand.
type g5FailingBind int64

func (g5FailingBind) SnapshotBind() (g5FailingBind, error) { return 0, errG5Snapshot }

func g5MutationTable(t *testing.T) (rasql.Table[g5MutationRow], rasql.Column[g5MutationRow, int64], rasql.Column[g5MutationRow, int64]) {
	t.Helper()
	table, _, id, name := g5MutationTableWithFlag(t)
	return table, id, name
}

func g5MutationTableWithFlag(t *testing.T) (rasql.Table[g5MutationRow], rasql.Column[g5MutationRow, g5FailingBind], rasql.Column[g5MutationRow, int64], rasql.Column[g5MutationRow, int64]) {
	t.Helper()
	table, err := rasql.TableOf[g5MutationRow](schema.TableDef{Name: "g5_items", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.IntegerType{}, Default: "1"},
		{Name: "count", Type: schema.IntegerType{}, Default: "2"}, {Name: "flag", Type: schema.IntegerType{}, Default: "0"},
	}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	id, err := rasql.BindColumn[g5MutationRow, int64](relation, "id", "")
	require.NoError(t, err)
	name, err := rasql.BindColumn[g5MutationRow, int64](relation, "name", "")
	require.NoError(t, err)
	flag, err := rasql.BindColumn[g5MutationRow, g5FailingBind](relation, "flag", "")
	require.NoError(t, err)
	return table, flag, id, name
}

func TestUpdateDefault(t *testing.T) {
	t.Run("capability and rendering", func(t *testing.T) {
		table, id, name := g5MutationTable(t)
		plan, err := rasql.NewPatchPlan(table, rasql.EqualValue(id.Expr(), int64(1)), rasql.DefaultField(name), rasql.SetField(id, int64(2)))
		require.NoError(t, err)
		for _, tc := range []struct {
			name    string
			dialect dialect.Dialect
			profile string
			major   int
			minor   int
			want    string
			fails   bool
		}{
			{name: "postgresql", dialect: dialect.PostgreSQL(), profile: "postgresql-17", major: 17, want: "DEFAULT"},
			{name: "mysql", dialect: dialect.MySQL(), profile: "mysql-8.4", major: 8, minor: 4, want: "DEFAULT"},
			{name: "sqlite", dialect: dialect.SQLite(), profile: "sqlite-3.35", major: 3, minor: 35, fails: true},
		} {
			t.Run(tc.name, func(t *testing.T) {
				profile, err := rasql.EngineProfileFromVersion(tc.profile, tc.major, tc.minor, 0)
				require.NoError(t, err)
				compiler, err := profile.Compiler(tc.dialect)
				require.NoError(t, err)
				rendered, err := compiler.Mutation(plan)
				if tc.fails {
					require.ErrorIs(t, err, rasql.ErrUnsupportedEngineFeature)
					return
				}
				require.NoError(t, err)
				require.Contains(t, rendered.SQL(), tc.want)
				require.Len(t, rendered.Args(), 2)
			})
		}
	})

	t.Run("the SQLite preflight executes nothing", func(t *testing.T) {
		database, err := sql.Open("sqlite", ":memory:")
		require.NoError(t, err)
		t.Cleanup(func() { _ = database.Close() })
		db, err := rasql.New(database, dialect.SQLite())
		require.NoError(t, err)
		profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
		require.NoError(t, err)
		executor, err := rasql.AsExecutor(db, profile)
		require.NoError(t, err)
		table, id, name := g5MutationTable(t)
		plan, err := rasql.NewPatchPlan(table, rasql.EqualValue(id.Expr(), int64(1)), rasql.DefaultField(name))
		require.NoError(t, err)
		_, err = rasql.ExecMutation(t.Context(), executor, plan)
		require.ErrorIs(t, err, rasql.ErrUnsupportedEngineFeature)
	})

	t.Run("a custom profile rejects DEFAULT before execution", func(t *testing.T) {
		table, id, name := g5MutationTable(t)
		_, err := rasql.NewPatchPlan(table, rasql.EqualValue(id.Expr(), int64(1)), rasql.DefaultField(name))
		require.NoError(t, err)
		profile, err := rasql.NewCustomEngineProfile("g5", rasql.EngineVersion{Known: true, Major: 1}, rasql.EngineCapabilities{}, rasql.EngineLimits{MaxBindParameters: 10})
		require.NoError(t, err)
		require.Equal(t, rasql.EngineUpdateDefaultUnsupported, profile.Capabilities().UpdateDefault)
	})

	t.Run("the patch predicate bridge accepts both families", func(t *testing.T) {
		table, id, name := g5MutationTable(t)
		rootPredicate := rasql.EqualValue(id.Expr(), int64(1))
		legacyPredicate := query.EqualValue(query.TypedColumnOf[g5MutationRow, int64](table.Column("id")), int64(1))
		rootPlan, err := rasql.NewPatchPlan(table, rootPredicate, rasql.SetField(name, int64(3)))
		require.NoError(t, err)
		legacyPlan, err := rasql.NewPatchPlan(table, legacyPredicate, rasql.SetField(name, int64(4)))
		require.NoError(t, err)
		compiler := mutationCompiler(t)
		_, err = compiler.Mutation(rootPlan)
		require.NoError(t, err)
		_, err = compiler.Mutation(legacyPlan)
		require.NoError(t, err)
	})

	t.Run("the patch predicate bridge preserves a bind snapshot error", func(t *testing.T) {
		table, flag, _, name := g5MutationTableWithFlag(t)
		predicate := rasql.EqualValue(flag.Expr(), g5FailingBind(1))
		_, err := rasql.NewPatchPlan(table, predicate, rasql.SetField(name, int64(3)))
		require.ErrorIs(t, err, errG5Snapshot)
	})

	t.Run("a patch predicate rejects the wrong table and stays in error", func(t *testing.T) {
		table, flag, _, name := g5MutationTableWithFlag(t)
		other, err := rasql.TableOf[g5MutationRow](schema.TableDef{Name: "g5_other", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		wrongPredicate := query.EqualValue(query.TypedColumnOf[g5MutationRow, int64](other.Column("id")), int64(1))
		wrongPlan, err := rasql.NewPatchPlan(table, wrongPredicate, rasql.SetField(name, int64(3)))
		require.NoError(t, err)
		compiler := mutationCompiler(t)
		_, err = compiler.Mutation(wrongPlan)
		require.Error(t, err)
		require.Contains(t, err.Error(), "outside the statement")
		bad := rasql.EqualValue(flag.Expr(), g5FailingBind(1))
		plan, err := rasql.NewPatchPlan(table, bad, rasql.SetField(name, int64(3)))
		require.ErrorIs(t, err, errG5Snapshot)
		_, err = compiler.Mutation(plan)
		require.ErrorIs(t, err, errG5Snapshot)
	})
}

// mutationCompiler renders a plan for one fixed profile, where a test cares
// only that lowering the plan succeeds or reports an error.
func mutationCompiler(t *testing.T) rasql.Compiler {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("postgresql-17", 17, 0, 0)
	require.NoError(t, err)
	compiler, err := profile.Compiler(dialect.PostgreSQL())
	require.NoError(t, err)
	return compiler
}
