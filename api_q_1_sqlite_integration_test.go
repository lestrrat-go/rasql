package rasql_test

import (
	"database/sql"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

func TestSQLiteReusableResultQueryConsumer(t *testing.T) {
	database, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, database.Close()) })
	database.SetMaxOpenConns(1)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE events (id INTEGER, category TEXT, value INTEGER)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `CREATE TABLE archive (category TEXT, count INTEGER)`)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), `INSERT INTO events VALUES (1, 'a', 1), (2, 'a', 1), (3, NULL, 1), (4, NULL, 1), (5, 'b', 0)`)
	require.NoError(t, err)

	events := query.MustTableRef(schema.TableDef{Name: "events", Columns: []schema.ColumnDef{
		{Name: "id", Type: schema.IntegerType{}},
		{Name: "category", Type: schema.TextType{}},
		{Name: "value", Type: schema.IntegerType{}},
	}})
	archive := query.MustTableRef(schema.TableDef{Name: "archive", Columns: []schema.ColumnDef{
		{Name: "category", Type: schema.TextType{}},
		{Name: "count", Type: schema.IntegerType{}},
	}})
	category, value := events.Column("category"), events.Column("value")
	filtered, err := query.NewGroupedSelect(events, []query.Expression{category}, category, query.CountAll().As("count"))
	require.NoError(t, err)
	filtered, err = filtered.WithWhere(query.GreaterThan(value, 0))
	require.NoError(t, err)
	result, err := query.ResultOf(filtered,
		query.ResultColumn{Name: "category", Type: schema.TextType{}, Nullable: true},
		query.ResultColumn{Name: "count", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	derived, err := query.Derived(result, "grouped")
	require.NoError(t, err)

	countStatement, err := render.SelectFromRelation(dialect.SQLite(), derived).Select("category").BuildCount()
	require.NoError(t, err)
	var groups int
	require.NoError(t, database.QueryRowContext(t.Context(), countStatement.SQL(), countStatement.Args()...).Scan(&groups))
	require.Equal(t, 2, groups)
	distinct, err := query.NewSelect(events, category)
	require.NoError(t, err)
	distinct, err = distinct.WithDistinct()
	require.NoError(t, err)
	distinct, err = distinct.WithOrder(query.Asc(category))
	require.NoError(t, err)
	distinctStatement, err := render.Select(dialect.SQLite(), distinct)
	require.NoError(t, err)
	distinctRows, err := database.QueryContext(t.Context(), distinctStatement.SQL(), distinctStatement.Args()...)
	require.NoError(t, err)
	var distinctValues []sql.NullString
	for distinctRows.Next() {
		var item sql.NullString
		require.NoError(t, distinctRows.Scan(&item))
		distinctValues = append(distinctValues, item)
	}
	require.NoError(t, distinctRows.Err())
	require.NoError(t, distinctRows.Close())
	require.Equal(t, []sql.NullString{{}, {Valid: true, String: "a"}, {Valid: true, String: "b"}}, distinctValues)

	paged, err := query.NewSelect(derived, derived.Column("category"), derived.Column("count"))
	require.NoError(t, err)
	paged, err = paged.WithOrder(query.Asc(derived.Column("count")), query.Asc(derived.Column("category")))
	require.NoError(t, err)
	paged, err = paged.WithLimit(1)
	require.NoError(t, err)
	pagedStatement, err := render.Select(dialect.SQLite(), paged)
	require.NoError(t, err)
	var categoryValue sql.NullString
	var countValue int
	require.NoError(t, database.QueryRowContext(t.Context(), pagedStatement.SQL(), pagedStatement.Args()...).Scan(&categoryValue, &countValue))
	require.False(t, categoryValue.Valid)
	require.Equal(t, 2, countValue)

	joined, err := query.NewJoinedSelect(events,
		[]query.Join{query.InnerJoin(derived, query.Equal(category, derived.Column("category")))}, nil,
		events.Column("id"), derived.Column("count"))
	require.NoError(t, err)
	joinedStatement, err := render.Select(dialect.SQLite(), joined)
	require.NoError(t, err)
	rows, err := database.QueryContext(t.Context(), joinedStatement.SQL(), joinedStatement.Args()...)
	require.NoError(t, err)
	joinedRows := 0
	for rows.Next() {
		var id, groupedCount int
		require.NoError(t, rows.Scan(&id, &groupedCount))
		joinedRows++
		require.Equal(t, 2, groupedCount)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, 2, joinedRows)

	compound, err := query.CompoundQuery(result, query.UnionAll, result)
	require.NoError(t, err)
	compoundResult, err := query.ResultOf(compound,
		query.ResultColumn{Name: "category", Type: schema.TextType{}, Nullable: true},
		query.ResultColumn{Name: "count", Type: schema.IntegerType{}},
	)
	require.NoError(t, err)
	unionRelation, err := query.Derived(compoundResult, "all_groups")
	require.NoError(t, err)
	unionSelect := mustSelect(t, unionRelation, unionRelation.Column("category"), unionRelation.Column("count"))
	unionSelect, err = unionSelect.WithOrder(query.Asc(unionRelation.Column("category")), query.Asc(unionRelation.Column("count")))
	require.NoError(t, err)
	unionStatement, err := render.Select(dialect.SQLite(), unionSelect)
	require.NoError(t, err)
	rows, err = database.QueryContext(t.Context(), unionStatement.SQL(), unionStatement.Args()...)
	require.NoError(t, err)
	unionRows := make([]struct {
		category sql.NullString
		count    int
	}, 0, 4)
	for rows.Next() {
		var item struct {
			category sql.NullString
			count    int
		}
		require.NoError(t, rows.Scan(&item.category, &item.count))
		unionRows = append(unionRows, item)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	require.Equal(t, 4, len(unionRows))
	require.Equal(t, []sql.NullString{{}, {}, {Valid: true, String: "a"}, {Valid: true, String: "a"}}, []sql.NullString{unionRows[0].category, unionRows[1].category, unionRows[2].category, unionRows[3].category})
	for _, item := range unionRows {
		require.Equal(t, 2, item.count)
	}

	cte, err := query.CommonTable("positive_groups", result)
	require.NoError(t, err)
	cteRef, err := cte.Ref("")
	require.NoError(t, err)
	cteSelect, err := query.NewSelect(cteRef, cteRef.Column("category"))
	require.NoError(t, err)
	cteSelect, err = cteSelect.WithCTEs(cte)
	require.NoError(t, err)
	update, err := query.NewUpdate(events, query.Set(value, 9))
	require.NoError(t, err)
	update, err = update.WithWhere(query.InSelect(category, cteSelect))
	require.NoError(t, err)
	updateStatement, err := render.Update(dialect.SQLite(), update)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), updateStatement.SQL(), updateStatement.Args()...)
	require.NoError(t, err)

	insert, err := query.NewInsertSelect(archive, []query.ColumnRef{archive.Column("category"), archive.Column("count")}, result)
	require.NoError(t, err)
	insertStatement, err := render.Insert(dialect.SQLite(), insert)
	require.NoError(t, err)
	_, err = database.ExecContext(t.Context(), insertStatement.SQL(), insertStatement.Args()...)
	require.NoError(t, err)
	archiveRows, err := database.QueryContext(t.Context(), `SELECT category, count FROM archive ORDER BY category`)
	require.NoError(t, err)
	type archiveRow struct {
		category sql.NullString
		count    int
	}
	var storedArchive []archiveRow
	for archiveRows.Next() {
		var item archiveRow
		require.NoError(t, archiveRows.Scan(&item.category, &item.count))
		storedArchive = append(storedArchive, item)
	}
	require.NoError(t, archiveRows.Err())
	require.NoError(t, archiveRows.Close())
	require.Len(t, storedArchive, 2)
	require.False(t, storedArchive[0].category.Valid)
	require.Equal(t, 2, storedArchive[0].count)
	require.Equal(t, sql.NullString{Valid: true, String: "a"}, storedArchive[1].category)
	require.Equal(t, 2, storedArchive[1].count)
	eventRows, err := database.QueryContext(t.Context(), `SELECT id, category, value FROM events ORDER BY id`)
	require.NoError(t, err)
	type eventRow struct {
		id       int
		category sql.NullString
		value    int
	}
	var storedEvents []eventRow
	for eventRows.Next() {
		var item eventRow
		require.NoError(t, eventRows.Scan(&item.id, &item.category, &item.value))
		storedEvents = append(storedEvents, item)
	}
	require.NoError(t, eventRows.Err())
	require.NoError(t, eventRows.Close())
	require.Equal(t, []eventRow{
		{1, sql.NullString{Valid: true, String: "a"}, 9},
		{2, sql.NullString{Valid: true, String: "a"}, 9},
		{3, sql.NullString{}, 1},
		{4, sql.NullString{}, 1},
		{5, sql.NullString{Valid: true, String: "b"}, 0},
	}, storedEvents)
}

func mustSelect(t *testing.T, source query.RelationRef, projections ...query.Projection) query.Select {
	t.Helper()
	statement, err := query.NewSelect(source, projections...)
	require.NoError(t, err)
	return statement
}
