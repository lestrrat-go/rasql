package rasql

import (
	"context"
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type safeQueryRow struct {
	ID       int64   `rasql:"id"`
	Nickname *string `rasql:"nickname"`
}

func TestSafeSelectBuilderUsesTypedPredicates(t *testing.T) {
	table, err := TableOf[safeQueryRow](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "email", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	statement, err := TypedSelectFrom(table).
		Where(query.EqualValue(query.TypedColumnOf[safeQueryRow, int64](table.Column("id")), int64(3))).
		Build(dialect.SQLite())
	require.NoError(t, err)
	require.Contains(t, statement.SQL(), "WHERE")
}

func TestTypedFacadeZeroValuesReturnErrors(t *testing.T) {
	var zeroTable Table[safeQueryRow]
	_, err := TypedSelectFrom(zeroTable).Select()
	require.Error(t, err)
	var zeroColumn query.TypedColumn[safeQueryRow, int64]
	_, err = TypedSelectFrom(TableFrom[safeQueryRow](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})).
		Where(query.EqualValue(zeroColumn, int64(1))).Select()
	require.Error(t, err)
	_, err = TypedSelectFrom(TableFrom[safeQueryRow](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})).
		Where(query.Predicate{}).Select()
	require.Error(t, err)
}

func TestSafeSelectBuilderSQLiteTerminalsAndImmutability(t *testing.T) {
	ctx := context.Background()
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer func() { _ = sqlDB.Close() }()
	sqlDB.SetMaxOpenConns(1)
	db, err := New(sqlDB, dialect.SQLite())
	require.NoError(t, err)
	table, err := TableOf[safeQueryRow](schema.TableDef{Name: "users", PrimaryKey: []string{"id"}, Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "nickname", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	require.NoError(t, CreateTable(ctx, db, table))
	nickname := "Ada"
	_, err = Insert(ctx, db, table, safeQueryRow{ID: 1, Nickname: &nickname})
	require.NoError(t, err)
	_, err = Insert(ctx, db, table, safeQueryRow{ID: 2})
	require.NoError(t, err)
	id := query.TypedColumnOf[safeQueryRow, int64](table.Column("id"))
	nick := query.NullableColumnOf[safeQueryRow, *string](table.Column("nickname"))
	base := TypedSelectFrom(table)
	derived := base.Where(query.EqualValue(id, int64(1))).Order(query.Asc(id.Ref())).Limit(1).Offset(0).Distinct()
	aliased, err := As(table, "other")
	require.NoError(t, err)
	otherID := query.TypedColumnOf[safeQueryRow, int64](aliased.Column("id"))
	branch := base.Project(id.Ref()).Join(query.TypedInnerJoin(aliased.Ref(), query.EqualColumns(id, otherID))).
		GroupBy(id.Ref()).Having(query.EqualValue(id, int64(1))).Order(query.Asc(id.Ref()))
	_, err = branch.Build(dialect.SQLite())
	require.NoError(t, err)
	baseStatement, err := base.Build(dialect.SQLite())
	require.NoError(t, err)
	derivedStatement, err := derived.Build(dialect.SQLite())
	require.NoError(t, err)
	dynamicStatement, err := SelectFrom(table).WhereEqual(id.Ref(), int64(1)).OrderAsc(id.Ref()).Limit(1).Offset(0).Distinct().Build(dialect.SQLite())
	require.NoError(t, err)
	require.NotEqual(t, baseStatement.SQL(), derivedStatement.SQL())
	require.Equal(t, dynamicStatement.SQL(), derivedStatement.SQL())
	require.Equal(t, dynamicStatement.Args(), derivedStatement.Args())
	require.Empty(t, baseStatement.Args())
	require.Equal(t, int64(1), derivedStatement.Args()[0])
	require.Len(t, derivedStatement.Args(), 3)
	rows, err := derived.All(ctx, db)
	require.NoError(t, err)
	require.Len(t, rows, 1)
	require.Equal(t, int64(1), rows[0].ID)
	nullRows, err := TypedSelectFrom(table).Where(query.TypedIsNull(nick)).All(ctx, db)
	require.NoError(t, err)
	require.Len(t, nullRows, 1)
	require.Nil(t, nullRows[0].Nickname)
	one, err := derived.One(ctx, db)
	require.NoError(t, err)
	require.Equal(t, int64(1), one.ID)
	count, err := derived.Count(ctx, db)
	require.NoError(t, err)
	require.Equal(t, int64(1), count)
	pageCount, err := derived.CountPage(ctx, db)
	require.NoError(t, err)
	require.Equal(t, int64(1), pageCount)
	sequence, err := derived.Query(ctx, db)
	require.NoError(t, err)
	seen := 0
	for row, rowErr := range sequence {
		require.NoError(t, rowErr)
		require.Equal(t, int64(1), row.ID)
		seen++
	}
	require.Equal(t, 1, seen)
	_, err = base.Result()
	require.NoError(t, err)
}
