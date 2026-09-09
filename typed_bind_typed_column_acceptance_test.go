//go:build unix

package rasql

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// This proves BindTypedColumn and BindNullTypedColumn, the bridge from a
// generated store accessor's query.TypedColumn into the typed Expr/Column
// layer. Today the only entry point is BindColumn, which names a column by
// string and so gives up the compile-time check that makes a renamed column
// a build failure; this is the entry point that keeps that check.

// bindTypedColumnGapRow stands in for a generated store row.
type bindTypedColumnGapRow struct {
	ID       int64
	Name     string
	Nickname Nullable[string]
}

// bindTypedColumnGapTable is shaped exactly the way rasqlgen shapes a
// generated table: an embedded Table[T] and one accessor method per column,
// returning query.TypedColumn or query.NullableColumn rather than a string.
// It is hand-written here only because this task may not touch examples/ or
// the generator while another agent is converting them onto this API;
// BindTypedColumn and BindNullTypedColumn are proved against this exact
// shape, the one a real generated store also produces.
type bindTypedColumnGapTable struct{ Table[bindTypedColumnGapRow] }

func (t bindTypedColumnGapTable) ID() query.TypedColumn[bindTypedColumnGapRow, int64] {
	return query.TypedColumnOf[bindTypedColumnGapRow, int64](ColumnOf(t.Table, "id"))
}
func (t bindTypedColumnGapTable) Name() query.TypedColumn[bindTypedColumnGapRow, string] {
	return query.TypedColumnOf[bindTypedColumnGapRow, string](ColumnOf(t.Table, "name"))
}
func (t bindTypedColumnGapTable) Nickname() query.NullableColumn[bindTypedColumnGapRow, string] {
	return query.NullableColumnOf[bindTypedColumnGapRow, string](ColumnOf(t.Table, "nickname"))
}

func bindTypedColumnGapUsers(t *testing.T) bindTypedColumnGapTable {
	t.Helper()
	table, err := TableOf[bindTypedColumnGapRow](schema.TableDef{Name: "gap_users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
		{Name: "nickname", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	return bindTypedColumnGapTable{Table: table}
}

type bindTypedColumnGapResultRow struct {
	Name     string
	Nickname Nullable[string]
}

type bindTypedColumnGapDecoder struct{ result ResultSchema }

func (d bindTypedColumnGapDecoder) ResultSchema() ResultSchema { return d.result }
func (d bindTypedColumnGapDecoder) Presence() []Presence       { return nil }
func (d bindTypedColumnGapDecoder) DecodeRow(src ScanSource, row *bindTypedColumnGapResultRow) error {
	return src.Scan(&row.Name, &row.Nickname)
}

// bindTypedColumnGapQuery builds
//
//	SELECT name, nickname FROM gap_users WHERE id > 0 ORDER BY name
//
// naming every column through users.ID(), users.Name() and users.Nickname()
// — the query.TypedColumn and query.NullableColumn a generated accessor
// returns — bridged by BindTypedColumn and BindNullTypedColumn, never by a
// string passed to BindColumn.
func bindTypedColumnGapQuery(t *testing.T) Query[bindTypedColumnGapResultRow] {
	t.Helper()

	users := bindTypedColumnGapUsers(t)
	u, err := SourceOf(users, "")
	require.NoError(t, err)

	id, err := BindTypedColumn(users.ID())
	require.NoError(t, err)
	name, err := BindTypedColumn(users.Name())
	require.NoError(t, err)
	nickname, err := BindNullTypedColumn(users.Nickname())
	require.NoError(t, err)

	result, err := NewResultSchema(
		ResultColumn{Name: "name", Type: schema.TextType{}},
		ResultColumn{Name: "nickname", Type: schema.TextType{}, Nullable: true},
	)
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{
		Item("name", name.Expr(), schema.TextType{}, ""),
		NullItem("nickname", nickname.NullExpr(), schema.TextType{}, ""),
	}, bindTypedColumnGapDecoder{result: result})
	require.NoError(t, err)

	return Select(u.Source(), projection).
		Where(GreaterValue(id.Expr(), int64(0))).
		OrderBy(AscExpr(name.Expr()))
}

func bindTypedColumnGapSeed(t *testing.T, database *sql.DB) {
	t.Helper()
	exec := func(statement string) {
		t.Helper()
		_, err := database.ExecContext(t.Context(), statement)
		require.NoError(t, err, statement)
	}
	exec("CREATE TABLE gap_users (id INTEGER PRIMARY KEY, name VARCHAR(64) NOT NULL, nickname VARCHAR(64))")
	exec(`INSERT INTO gap_users (id, name, nickname) VALUES (1, 'alice', 'ali'), (2, 'bob', NULL)`)
}

// runBindTypedColumnGapAcceptance runs the query and checks the decoded
// rows. BindTypedColumn or BindNullTypedColumn recovering the wrong column
// (say, swapping which accessor's ColumnRef backs which typed handle) would
// either fail Validate outright or decode the wrong values into name and
// nickname; getting alice's real nickname back and bob's real NULL back is
// what a passing assertion here requires.
func runBindTypedColumnGapAcceptance(t *testing.T, database *sql.DB, executor Executor) {
	t.Helper()
	bindTypedColumnGapSeed(t, database)
	rows, err := All(t.Context(), executor, bindTypedColumnGapQuery(t))
	require.NoError(t, err)
	require.Equal(t, []bindTypedColumnGapResultRow{
		{Name: "alice", Nickname: Nullable[string]{Value: "ali", Valid: true}},
		{Name: "bob", Nickname: Nullable[string]{}},
	}, rows)
}

func TestBindTypedColumnAcceptanceSQLite(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runBindTypedColumnGapAcceptance(t, database, executor)
}

func TestBindTypedColumnAcceptancePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	db, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runBindTypedColumnGapAcceptance(t, database, executor)
}

func TestBindTypedColumnAcceptanceMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	db, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runBindTypedColumnGapAcceptance(t, database, executor)
}
