//go:build unix

package rasql

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/dbtest"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

// This proves CoalesceExpr, the typed COALESCE. It takes a nullable column
// and a never-NULL fallback and returns a plain Expr, which the test uses
// twice over: once to read the substituted value back per row, and once to
// feed that Expr into GreaterValue, an operator only a non-null Expr can
// reach — a NullExpr could not have been passed there at all, so the query
// below would fail to compile if CoalesceExpr still returned one.

type coalesceGapAccountRow struct {
	Name string
}

type coalesceGapDecoder struct{ result ResultSchema }

func (d coalesceGapDecoder) ResultSchema() ResultSchema { return d.result }
func (d coalesceGapDecoder) Presence() []Presence       { return nil }
func (d coalesceGapDecoder) DecodeRow(src ScanSource, row *coalesceGapAccountRow) error {
	return src.Scan(&row.Name)
}

// coalesceGapQuery builds:
//
//	SELECT name FROM accounts
//	WHERE COALESCE(credit_limit, 100) > 40
//	ORDER BY name
//
// credit_limit is nullable; 100 is the fallback CoalesceExpr substitutes for
// a NULL row. The threshold, 40, sits strictly between the fallback and one
// seeded row's real limit, so the result set only comes out right if NULL
// rows are actually replaced with 100 rather than passed through as NULL
// (which would drop that row from a > comparison entirely) or as some other
// placeholder such as 0 (which would also drop it, since 0 is not > 40).
func coalesceGapQuery(t *testing.T) Query[coalesceGapAccountRow] {
	t.Helper()

	accounts, err := ReadTableOf[coalesceGapAccountRow](schema.TableDef{Name: "accounts", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "name", Type: schema.TextType{}},
		{Name: "credit_limit", Type: schema.IntegerType{}, Nullable: true},
	}})
	require.NoError(t, err)
	a, err := SourceOf(accounts, "")
	require.NoError(t, err)

	name, err := BindColumn[coalesceGapAccountRow, string](a, "name", "")
	require.NoError(t, err)
	creditLimit, err := BindNullColumn[coalesceGapAccountRow, int64](a, "credit_limit", "")
	require.NoError(t, err)

	limit := CoalesceExpr(creditLimit.NullExpr(), Value(int64(100)))

	result, err := NewResultSchema(ResultColumn{Name: "name", Type: schema.TextType{}})
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{
		Item("name", name.Expr(), schema.TextType{}, ""),
	}, coalesceGapDecoder{result: result})
	require.NoError(t, err)

	return Select(a.Source(), projection).
		Where(GreaterValue(limit, int64(40))).
		OrderBy(AscExpr(name.Expr()))
}

// coalesceGapSeed seeds three accounts: alice has no credit_limit at all
// (NULL, falls back to 100, kept by > 40), bob's real limit is 30 (kept as
// 30, dropped by > 40), and carol's real limit is 5000 (kept as 5000, kept
// by > 40). Alice and carol surviving while bob does not is what a wrong
// fallback value or a fallback that only sometimes applies would get wrong.
func coalesceGapSeed(t *testing.T, database *sql.DB) {
	t.Helper()
	exec := func(statement string) {
		t.Helper()
		_, err := database.ExecContext(t.Context(), statement)
		require.NoError(t, err, statement)
	}
	exec("CREATE TABLE accounts (id INTEGER PRIMARY KEY, name VARCHAR(64) NOT NULL, credit_limit INTEGER)")
	exec(`INSERT INTO accounts (id, name, credit_limit) VALUES
		(1, 'alice', NULL), (2, 'bob', 30), (3, 'carol', 5000)`)
}

func runCoalesceGapAcceptance(t *testing.T, database *sql.DB, executor Executor) {
	t.Helper()
	coalesceGapSeed(t, database)
	rows, err := All(t.Context(), executor, coalesceGapQuery(t))
	require.NoError(t, err)
	require.Equal(t, []coalesceGapAccountRow{
		{Name: "alice"},
		{Name: "carol"},
	}, rows)
}

func TestTypedOperatorCoalesceExprAcceptanceSQLite(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = database.Close() })
	db, err := New(database, dialect.SQLite())
	require.NoError(t, err)
	profile, err := EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runCoalesceGapAcceptance(t, database, executor)
}

func TestTypedOperatorCoalesceExprAcceptancePostgreSQL(t *testing.T) {
	database := dbtest.PostgreSQLDB(t)
	db, err := New(database, dialect.PostgreSQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "postgresql-17")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runCoalesceGapAcceptance(t, database, executor)
}

func TestTypedOperatorCoalesceExprAcceptanceMySQL(t *testing.T) {
	database := dbtest.MySQLDB(t)
	db, err := New(database, dialect.MySQL())
	require.NoError(t, err)
	profile, err := DiscoverEngineProfile(t.Context(), db, "mysql-8.4")
	require.NoError(t, err)
	executor, err := AsExecutor(db, profile)
	require.NoError(t, err)
	runCoalesceGapAcceptance(t, database, executor)
}
