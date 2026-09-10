package rasql

import (
	"database/sql"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type partitionRow struct{ ID int64 }
type nullOrderRow struct{ ID Nullable[int64] }
type partitionDecoder struct{ schema ResultSchema }

func (d partitionDecoder) ResultSchema() ResultSchema              { return d.schema }
func (partitionDecoder) Presence() []Presence                      { return nil }
func (partitionDecoder) DecodeRow(ScanSource, *partitionRow) error { return nil }

func TestQ2MatchBaseOccurrences(t *testing.T) {
	base := compiledQuery{statement: stmt.New(sqltext.Text("base"), 1, 2), bindSlots: []bindSlot{{id: 11, codec: "a"}, {id: 12, codec: "b"}}}
	paged := compiledQuery{statement: stmt.New(sqltext.Text("paged"), 9, 1, 2, 10), bindSlots: []bindSlot{{id: 90}, {id: 11, codec: "a"}, {id: 12, codec: "b"}, {id: 91}}}
	indexes, err := matchBaseOccurrences(base, paged)
	require.NoError(t, err)
	require.Equal(t, []int{1, 2}, indexes)
	base.bindSlots[0].id = 0
	_, err = matchBaseOccurrences(base, paged)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsupported_keyset_bind", planErr.Code)
}

func TestQ2UnwrapBindTokensPreservesNamedValues(t *testing.T) {
	value, copier, err := adoptBind(42, true)
	require.NoError(t, err)
	statement := stmt.New(sqltext.Text("SELECT ?"), bindToken{id: 7, value: value, copy: copier, codec: "int"})
	compiled, err := unwrapBindTokens(statement)
	require.NoError(t, err)
	require.Equal(t, []any{42}, compiled.statement.Args())
	require.Equal(t, []bindSlot{{id: 7, codec: "int"}}, compiled.bindSlots)
}

func TestQ2PartitionLimitLowersToRowNumber(t *testing.T) {
	schemaValue, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	table, err := ReadTableOf[partitionRow](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "i")
	require.NoError(t, err)
	column, err := BindColumn[partitionRow, int64](relation, "id", "")
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("id", column.Expr(), schema.IntegerType{}, "")}, partitionDecoder{schema: schemaValue})
	require.NoError(t, err)
	q, err := Select(relation.Source(), projection).withPartitionLimit([]GroupKey{Group(column.Expr())}, []OrderTerm{AscExpr(column.Expr())}, 2)
	require.NoError(t, err)
	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	compiler, err := querycompile.New(profile)
	require.NoError(t, err)
	compiled, err := compileQuery(&compiler, q)
	require.NoError(t, err)
	statement := compiled.statement
	require.Contains(t, statement.SQL(), "row_number() OVER")
	require.Contains(t, statement.SQL(), "PARTITION BY")
	require.Contains(t, statement.SQL(), "<= ?")
	db, err := sql.Open("sqlite", ":memory:")
	require.NoError(t, err)
	defer func() { require.NoError(t, db.Close()) }()
	_, err = db.ExecContext(t.Context(), `CREATE TABLE items (id INTEGER)`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO items (id) VALUES (1), (1), (1), (1), (2), (2), (2), (2)`)
	require.NoError(t, err)
	rows, err := db.QueryContext(t.Context(), statement.SQL(), statement.Args()...)
	require.NoError(t, err)
	defer func() { require.NoError(t, rows.Close()) }()
	count := 0
	ids := make([]int64, 0, 4)
	for rows.Next() {
		var id int64
		require.NoError(t, rows.Scan(&id))
		ids = append(ids, id)
		count++
	}
	require.NoError(t, rows.Err())
	require.Equal(t, 4, count)
	require.Equal(t, []int64{1, 1, 2, 2}, ids)
	_, err = q.withPartitionLimit(nil, []OrderTerm{AscExpr(column.Expr())}, 2)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "invalid_partition_limit", planErr.Code)
}

func partitionQuery(t *testing.T) Query[partitionRow] {
	t.Helper()
	schemaValue, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}})
	require.NoError(t, err)
	table, err := ReadTableOf[partitionRow](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "i")
	require.NoError(t, err)
	column, err := BindColumn[partitionRow, int64](relation, "id", "")
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{Item("id", column.Expr(), schema.IntegerType{}, "")}, partitionDecoder{schema: schemaValue})
	require.NoError(t, err)
	q, err := Select(relation.Source(), projection).withPartitionLimit([]GroupKey{Group(column.Expr())}, []OrderTerm{AscExpr(column.Expr())}, 2)
	require.NoError(t, err)
	return q
}

func TestQ2PartitionLimitPreservesUnsupportedWindowCause(t *testing.T) {
	base, err := engineprofile.Builtin("postgresql-17", engineprofile.Version{Known: true, Major: 17})
	require.NoError(t, err)
	caps := base.Capabilities
	caps.WindowFunctions = false
	caps.PerParentLimit = engineprofile.PerParentLimitUnsupported
	profile, err := engineprofile.New("custom:q2", engineprofile.Custom, "postgresql", engineprofile.Version{}, caps, engineprofile.Limits{MaxBindParameters: 100})
	require.NoError(t, err)
	compiler, err := querycompile.NewWithDialect(profile, dialect.PostgreSQL())
	require.NoError(t, err)
	_, err = compileQuery(&compiler, partitionQuery(t))
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsupported_feature", planErr.Code)
	require.True(t, errors.Is(err, engineprofile.ErrUnsupportedFeature))
}

func TestQ2NullOrderSQLiteAndRenderProfiles(t *testing.T) {
	for _, tc := range []struct {
		name       string
		placement  NullOrder
		descending bool
		want       []any
	}{
		{"asc first", NullsFirst, false, []any{nil, int64(1), int64(2)}},
		{"asc last", NullsLast, false, []any{int64(1), int64(2), nil}},
		{"desc first", NullsFirst, true, []any{nil, int64(2), int64(1)}},
		{"desc last", NullsLast, true, []any{int64(2), int64(1), nil}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			nullColumn, table := nullableColumn(t)
			term := AscNull(nullColumn.NullExpr(), tc.placement)
			if tc.descending {
				term = DescNull(nullColumn.NullExpr(), tc.placement)
			}
			q := nullableOrderQuery(t, table, nullColumn, term)
			profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
			require.NoError(t, err)
			compiler, err := querycompile.New(profile)
			require.NoError(t, err)
			compiled, err := compileQuery(&compiler, q)
			require.NoError(t, err)
			db, err := sql.Open("sqlite", ":memory:")
			require.NoError(t, err)
			defer func() { require.NoError(t, db.Close()) }()
			_, err = db.ExecContext(t.Context(), `CREATE TABLE nullable_items (id INTEGER)`)
			require.NoError(t, err)
			_, err = db.ExecContext(t.Context(), `INSERT INTO nullable_items (id) VALUES (NULL), (1), (2)`)
			require.NoError(t, err)
			rows, err := db.QueryContext(t.Context(), compiled.statement.SQL(), compiled.statement.Args()...)
			require.NoError(t, err)
			defer func() { require.NoError(t, rows.Close()) }()
			got := make([]any, 0, 3)
			for rows.Next() {
				var value any
				require.NoError(t, rows.Scan(&value))
				got = append(got, value)
			}
			require.NoError(t, rows.Err())
			require.Equal(t, tc.want, got)
		})
	}
	nullColumn, table := nullableColumn(t)
	q := nullableOrderQuery(t, table, nullColumn, AscNull(nullColumn.NullExpr(), NullsLast))
	pg, err := engineprofile.Builtin("postgresql-17", engineprofile.Version{Known: true, Major: 17})
	require.NoError(t, err)
	pgCompiler, err := querycompile.New(pg)
	require.NoError(t, err)
	pgStatement, err := compileQuery(&pgCompiler, q)
	require.NoError(t, err)
	require.Contains(t, pgStatement.statement.SQL(), "NULLS LAST")
	my, err := engineprofile.Builtin("mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4})
	require.NoError(t, err)
	myCompiler, err := querycompile.New(my)
	require.NoError(t, err)
	_, err = compileQuery(&myCompiler, q)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsupported_feature", planErr.Code)
}

func TestQ2PartitionLimitRejectsZeroNodesAndInvalidNullPlacement(t *testing.T) {
	q := partitionQuery(t)
	_, err := q.withPartitionLimit([]GroupKey{{}}, []OrderTerm{AscExpr(Expr[int64]{})}, 2)
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "invalid_partition_limit", planErr.Code)
	table := query.MustTableRef(schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	selectBody, err := query.NewSelect(table, table.Column("id"))
	require.NoError(t, err)
	_, err = selectBody.WithOrder(query.AscNulls(table.Column("id"), query.NullPlacement(99)))
	require.Error(t, err)
}

func nullableColumn(t *testing.T) (NullColumn[nullOrderRow, int64], ReadTable[nullOrderRow]) {
	t.Helper()
	table, err := ReadTableOf[nullOrderRow](schema.TableDef{Name: "nullable_items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, Nullable: true}}})
	require.NoError(t, err)
	relation, err := SourceOf(table, "n")
	require.NoError(t, err)
	column, err := BindNullColumn[nullOrderRow, int64](relation, "id", "")
	require.NoError(t, err)
	return column, table
}

func nullableOrderQuery(t *testing.T, table ReadTable[nullOrderRow], column NullColumn[nullOrderRow, int64], term OrderTerm) Query[Nullable[int64]] {
	t.Helper()
	schemaValue, err := NewResultSchema(ResultColumn{Name: "id", Type: schema.IntegerType{}, Nullable: true})
	require.NoError(t, err)
	projection, err := NullableScalar("id", column.NullExpr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	relation, err := SourceOf(table, "n")
	require.NoError(t, err)
	_ = schemaValue
	return Select(relation.Source(), projection).OrderBy(term)
}
