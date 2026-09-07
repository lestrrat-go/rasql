package rasql

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type g5MutationRow struct{ ID, Name, Count int64 }

var errG5Snapshot = errors.New("g5 snapshot failed")

func g5MutationTable(t *testing.T) (Table[g5MutationRow], Column[g5MutationRow, int64], Column[g5MutationRow, int64]) {
	t.Helper()
	table, err := TableOf[g5MutationRow](schema.TableDef{Name: "g5_items", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}}, {Name: "name", Type: schema.IntegerType{}, Default: "1"}, {Name: "count", Type: schema.IntegerType{}, Default: "2"},
	}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "")
	require.NoError(t, err)
	id, err := BindColumn[g5MutationRow, int64](relation, "id", "")
	require.NoError(t, err)
	name, err := BindColumn[g5MutationRow, int64](relation, "name", "")
	require.NoError(t, err)
	return table, id, name
}

func TestG5SQLiteUpdateDefaultPreflightDoesNotExecute(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	table, id, name := g5MutationTable(t)
	plan, err := NewPatchPlan(table, EqualValue(id.Expr(), int64(1)), DefaultField(name))
	require.NoError(t, err)
	_, err = ExecMutation(t.Context(), executor, plan)
	require.ErrorIs(t, err, ErrUnsupportedEngineFeature)
}

func TestG5PatchPredicateBridgeAcceptsBothFamilies(t *testing.T) {
	table, id, name := g5MutationTable(t)
	rootPredicate := EqualValue(id.Expr(), int64(1))
	legacyPredicate := query.EqualValue(query.TypedColumnOf[g5MutationRow, int64](table.Column("id")), int64(1))
	rootPlan, err := NewPatchPlan(table, rootPredicate, SetField(name, int64(3)))
	require.NoError(t, err)
	legacyPlan, err := NewPatchPlan(table, legacyPredicate, SetField(name, int64(4)))
	require.NoError(t, err)
	_, err = rootPlan.lower()
	require.NoError(t, err)
	_, err = legacyPlan.lower()
	require.NoError(t, err)
}

func TestG5PatchPredicateBridgePreservesBindSnapshotError(t *testing.T) {
	table, id, name := g5MutationTable(t)
	predicate := Predicate{node: query.Equal(id.Expr(), query.Bind(int64(1))), bindErr: errG5Snapshot}
	_, err := NewPatchPlan(table, predicate, SetField(name, int64(3)))
	require.ErrorIs(t, err, errG5Snapshot)
}

func TestG5UpdateDefaultCapabilityAndRendering(t *testing.T) {
	table, id, name := g5MutationTable(t)
	plan, err := NewPatchPlan(table, EqualValue(id.Expr(), int64(1)), DefaultField(name), SetField(id, int64(2)))
	require.NoError(t, err)
	statement, err := plan.lower()
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
			profile, err := EngineProfileFromVersion(tc.profile, tc.major, tc.minor, 0)
			require.NoError(t, err)
			compiler, err := profile.queryCompiler(tc.dialect)
			require.NoError(t, err)
			rendered, err := compiler.Write(statement)
			if tc.fails {
				require.ErrorIs(t, err, ErrUnsupportedEngineFeature)
				return
			}
			require.NoError(t, err)
			require.Contains(t, rendered.SQL(), tc.want)
			require.Len(t, rendered.Args(), 2)
		})
	}
}
