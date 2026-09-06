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
	_, err = base.Select()
	require.NoError(t, err)
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

func TestTypedPredicatesMatchDynamicSQLAndArgs(t *testing.T) {
	table, err := TableOf[safeQueryRow](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "nickname", Type: schema.TextType{}, Nullable: true},
	}})
	require.NoError(t, err)
	id := query.TypedColumnOf[safeQueryRow, int64](table.Column("id"))
	nickname := query.NullableColumnOf[safeQueryRow, *string](table.Column("nickname"))
	ada := "Ada"
	cases := []struct {
		name    string
		typed   query.Predicate
		dynamic query.Expression
	}{
		{"equal", query.EqualValue(id, int64(3)), query.Equal(id.Ref(), int64(3))},
		{"nullable equal", query.EqualNullableValue(nickname, &ada), query.Equal(nickname.Ref(), &ada)},
		{"less", query.LessValue(id, int64(3)), query.LessThan(id.Ref(), int64(3))},
		{"less or equal", query.LessOrEqualValue(id, int64(3)), query.LessThanOrEqual(id.Ref(), int64(3))},
		{"greater", query.GreaterValue(id, int64(3)), query.GreaterThan(id.Ref(), int64(3))},
		{"greater or equal", query.GreaterOrEqualValue(id, int64(3)), query.GreaterThanOrEqual(id.Ref(), int64(3))},
		{"in", query.InValues(id, int64(1), int64(3)), query.In(id.Ref(), int64(1), int64(3))},
		{"is null", query.TypedIsNull(nickname), query.IsNull(nickname.Ref())},
		{"is not null", query.TypedIsNotNull(nickname), query.IsNotNull(nickname.Ref())},
		{"logical", query.AndPredicates(query.EqualValue(id, int64(3)), query.TypedIsNotNull(nickname)), query.And(query.Equal(id.Ref(), int64(3)), query.IsNotNull(nickname.Ref()))},
		{"not", query.NotPredicate(query.EqualValue(id, int64(3))), query.Negate(query.Equal(id.Ref(), int64(3)))},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			typed, err := TypedSelectFrom(table).Where(tc.typed).Build(dialect.SQLite())
			require.NoError(t, err)
			dynamic, err := SelectFrom(table).Where(tc.dynamic).Build(dialect.SQLite())
			require.NoError(t, err)
			require.Equal(t, dynamic.SQL(), typed.SQL())
			require.Equal(t, dynamic.Args(), typed.Args())
		})
	}

	aliased, err := As(table, "other")
	require.NoError(t, err)
	otherID := query.TypedColumnOf[safeQueryRow, int64](aliased.Column("id"))
	typedJoin := TypedSelectFrom(table).Join(query.TypedLeftJoin(aliased.Ref(), query.EqualColumns(id, otherID)))
	dynamicJoin := SelectFrom(table).Join(query.LeftJoin(aliased.Ref(), query.Equal(id.Ref(), otherID.Ref())))
	typedStatement, err := typedJoin.Build(dialect.SQLite())
	require.NoError(t, err)
	dynamicStatement, err := dynamicJoin.Build(dialect.SQLite())
	require.NoError(t, err)
	require.Equal(t, dynamicStatement.SQL(), typedStatement.SQL())
	require.Equal(t, dynamicStatement.Args(), typedStatement.Args())
}

func TestSafeSelectBuilderForwardsErrorsAndZeroValues(t *testing.T) {
	table, err := TableOf[safeQueryRow](schema.TableDef{Name: "users", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}, {Name: "nickname", Type: schema.TextType{}, Nullable: true}}})
	require.NoError(t, err)
	id := query.TypedColumnOf[safeQueryRow, int64](table.Column("id"))
	var zeroNullable query.NullableColumn[safeQueryRow, *string]
	_, err = TypedSelectFrom(table).Where(query.TypedIsNull(zeroNullable)).Build(dialect.SQLite())
	require.Error(t, err)
	var zeroJoin query.TypedJoin
	_, err = TypedSelectFrom(table).Join(zeroJoin).Build(dialect.SQLite())
	require.Error(t, err)
	var zeroBuilder SafeSelectBuilder[safeQueryRow]
	_, err = zeroBuilder.Select()
	require.Error(t, err)

	invalid := TypedSelectFrom(table).Where(query.Predicate{})
	sqlDB, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { _ = sqlDB.Close() })
	db, err := New(sqlDB, dialect.SQLite())
	require.NoError(t, err)
	terminals := []struct {
		name string
		call func() error
	}{
		{"select", func() error { _, err := invalid.Select(); return err }},
		{"result", func() error { _, err := invalid.Result(); return err }},
		{"build", func() error { _, err := invalid.Build(dialect.SQLite()); return err }},
		{"query", func() error { _, err := invalid.Query(context.Background(), db); return err }},
		{"all", func() error { _, err := invalid.All(context.Background(), db); return err }},
		{"one", func() error { _, err := invalid.One(context.Background(), db); return err }},
		{"count", func() error { _, err := invalid.Count(context.Background(), db); return err }},
		{"count page", func() error { _, err := invalid.CountPage(context.Background(), db); return err }},
	}
	for _, terminal := range terminals {
		t.Run(terminal.name, func(t *testing.T) { require.Error(t, terminal.call()) })
	}

	forwarders := []struct {
		name string
		call func(SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow]
	}{
		{"project", func(b SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow] { return b.Project(id.Ref()) }},
		{"join", func(b SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow] {
			aliased, err := As(table, "other")
			require.NoError(t, err)
			otherID := query.TypedColumnOf[safeQueryRow, int64](aliased.Column("id"))
			return b.Join(query.TypedInnerJoin(aliased.Ref(), query.EqualColumns(id, otherID)))
		}},
		{"where", func(b SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow] {
			return b.Where(query.EqualValue(id, int64(1)))
		}},
		{"group by", func(b SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow] { return b.GroupBy(id.Ref()) }},
		{"having", func(b SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow] {
			return b.Project(query.CountAll()).GroupBy(id.Ref()).Having(query.EqualValue(id, int64(1)))
		}},
		{"order", func(b SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow] {
			return b.Order(query.Asc(id.Ref()))
		}},
		{"distinct", func(b SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow] { return b.Distinct() }},
		{"limit", func(b SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow] { return b.Limit(1) }},
		{"offset", func(b SafeSelectBuilder[safeQueryRow]) SafeSelectBuilder[safeQueryRow] { return b.Offset(1) }},
	}
	base := TypedSelectFrom(table)
	baseStatement, err := base.Build(dialect.SQLite())
	require.NoError(t, err)
	for _, forwarder := range forwarders {
		t.Run(forwarder.name, func(t *testing.T) {
			branch := forwarder.call(base)
			statement, err := branch.Build(dialect.SQLite())
			require.NoError(t, err)
			require.NotEqual(t, baseStatement.SQL(), statement.SQL())
			unchanged, err := base.Build(dialect.SQLite())
			require.NoError(t, err)
			require.Equal(t, baseStatement.SQL(), unchanged.SQL())
		})
	}
}
