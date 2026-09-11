package rasql

import (
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
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

type q2AcceptanceRow struct {
	Category Nullable[string]
	Amount   int64
}

type q2AcceptanceDecoder struct{ resultSchema ResultSchema }

func (d q2AcceptanceDecoder) ResultSchema() ResultSchema { return d.resultSchema }
func (q2AcceptanceDecoder) Presence() []Presence         { return nil }
func (q2AcceptanceDecoder) DecodeRow(source ScanSource, row *q2AcceptanceRow) error {
	var category sql.NullString
	if err := source.Scan(&category, &row.Amount); err != nil {
		return err
	}
	row.Category = Nullable[string]{Value: category.String, Valid: category.Valid}
	return nil
}

type q2AcceptanceGroup struct {
	Category Nullable[string]
	Count    int64
}

type q2AcceptanceGroupDecoder struct{ resultSchema ResultSchema }

func (d q2AcceptanceGroupDecoder) ResultSchema() ResultSchema { return d.resultSchema }
func (q2AcceptanceGroupDecoder) Presence() []Presence         { return nil }
func (q2AcceptanceGroupDecoder) DecodeRow(source ScanSource, row *q2AcceptanceGroup) error {
	var category sql.NullString
	if err := source.Scan(&category, &row.Count); err != nil {
		return err
	}
	row.Category = Nullable[string]{Value: category.String, Valid: category.Valid}
	return nil
}

func TestQueryComposition(t *testing.T) {
	t.Run("executes compound outer operations and counts", func(t *testing.T) {
		db := q2AcceptanceSQLite(t)
		base := q2AcceptanceQuery(t)
		compiler := q2AcceptanceCompiler(t)

		union, err := Combine(base, Union, base)
		require.NoError(t, err)
		unionRows := q2AcceptanceRows(t, db, compiler, union)
		require.Equal(t, []q2AcceptanceRow{{Category: Nullable[string]{Valid: false}, Amount: 0}, {Category: Nullable[string]{Value: "a", Valid: true}, Amount: 1}, {Category: Nullable[string]{Value: "b", Valid: true}, Amount: 2}}, unionRows)

		all, err := Combine(base, UnionAll, base)
		require.NoError(t, err)
		allRows := q2AcceptanceRows(t, db, compiler, all)
		require.Len(t, allRows, 8)
		require.Equal(t, allRows[:4], allRows[4:])

		filtered := all.Where(EqualExpr(Value(1), Value(2))).Where(EqualExpr(Value(3), Value(4)))
		filteredRows := q2AcceptanceRows(t, db, compiler, filtered)
		require.Empty(t, filteredRows)

		ordered := all.OrderBy(AscExpr(Value(int64(1))))
		orderedRows := q2AcceptanceRows(t, db, compiler, ordered)
		require.Len(t, orderedRows, 8)

		distinctRows := q2AcceptanceRows(t, db, compiler, all.Distinct())
		require.Equal(t, unionRows, distinctRows)

		limited, err := all.Limit(1)
		require.NoError(t, err)
		offset, err := limited.Offset(1)
		require.NoError(t, err)
		pagedRows := q2AcceptanceRows(t, db, compiler, offset)
		require.Len(t, pagedRows, 1)

		projectedSchema, err := NewResultSchema(
			ResultColumn{Name: "category", Type: schema.TextType{}, Codec: "category.codec"},
			ResultColumn{Name: "amount", Type: schema.IntegerType{}, Codec: "amount.codec"},
		)
		require.NoError(t, err)
		projected, err := NewProjection([]ProjectionItem{
			Item("category", Value("projected"), schema.TextType{}, "category.codec"),
			Item("amount", Value(int64(99)), schema.IntegerType{}, "amount.codec"),
		}, q2AcceptanceDecoder{resultSchema: projectedSchema})
		require.NoError(t, err)
		projectedQuery := Project(all.Plan(), projected)
		projectedRows := q2AcceptanceRows(t, db, compiler, projectedQuery)
		require.Len(t, projectedRows, 8)
		for _, row := range projectedRows {
			require.Equal(t, q2AcceptanceRow{Category: Nullable[string]{Value: "projected", Valid: true}, Amount: 99}, row)
		}

		cte, err := CTEOf("categories", base)
		require.NoError(t, err)
		withSource, err := cte.Source("c")
		require.NoError(t, err)
		withCategory, err := BindNullResultColumn[q2AcceptanceRow, string](withSource, "category")
		require.NoError(t, err)
		withAmount, err := BindResultColumn[q2AcceptanceRow, int64](withSource, "amount")
		require.NoError(t, err)
		withProjection, err := NewProjection([]ProjectionItem{
			NullItem("category", withCategory.NullExpr(), schema.TextType{}, "category.codec"),
			Item("amount", withAmount.Expr(), schema.IntegerType{}, "amount.codec"),
		}, q2AcceptanceDecoder{resultSchema: base.Schema()})
		require.NoError(t, err)
		withQuery, err := With(Select(withSource.Source(), withProjection), cte)
		require.NoError(t, err)
		withRows := q2AcceptanceRows(t, db, compiler, withQuery)
		require.Equal(t, baseRowsExpected(), withRows)

		countAll := CountQuery(offset, true)
		require.Equal(t, int64(1), q2AcceptanceCountValue(t, db, compiler, countAll))
		countUnpaged := CountQuery(offset, false)
		require.Equal(t, int64(8), q2AcceptanceCountValue(t, db, compiler, countUnpaged))

		operand, err := base.Limit(2)
		require.NoError(t, err)
		pagedUnion, err := Combine(operand, UnionAll, operand)
		require.NoError(t, err)
		outerPaged, err := pagedUnion.Limit(1)
		require.NoError(t, err)
		require.Len(t, q2AcceptanceRows(t, db, compiler, outerPaged), 1)
		require.Equal(t, int64(1), q2AcceptanceCountValue(t, db, compiler, CountQuery(outerPaged, true)))
		require.Equal(t, int64(4), q2AcceptanceCountValue(t, db, compiler, CountQuery(outerPaged, false)))
	})

	t.Run("reuses a grouped DTO through a derived table and a CTE", func(t *testing.T) {
		db := q2AcceptanceSQLite(t)
		compiler := q2AcceptanceCompiler(t)
		table, err := ReadTableOf[q2AcceptanceRow](schema.TableDef{
			Name: "q2_items",
			Columns: []schema.ColumnDef{
				{Name: "category", Type: schema.TextType{}, Nullable: true},
				{Name: "amount", Type: schema.IntegerType{}},
			},
		})
		require.NoError(t, err)
		relation, err := SourceOf(table, "i")
		require.NoError(t, err)
		category, err := BindNullColumn[q2AcceptanceRow, string](relation, "category", "category.codec")
		require.NoError(t, err)
		amount, err := BindColumn[q2AcceptanceRow, int64](relation, "amount", "amount.codec")
		require.NoError(t, err)
		schemaValue, err := NewResultSchema(
			ResultColumn{Name: "category", Type: schema.TextType{}, Nullable: true, Codec: "category.codec"},
			ResultColumn{Name: "total", Type: schema.IntegerType{}, Codec: "count.codec"},
		)
		require.NoError(t, err)
		groupProjection, err := NewProjection([]ProjectionItem{
			NullItem("category", category.NullExpr(), schema.TextType{}, "category.codec"),
			Item("total", CountExpr(amount.Expr()), schema.IntegerType{}, "count.codec"),
		}, q2AcceptanceGroupDecoder{resultSchema: schemaValue})
		require.NoError(t, err)
		grouped := Select(relation.Source(), groupProjection).GroupBy(GroupNull(category.NullExpr())).OrderBy(AscNull(category.NullExpr(), NullsFirst))
		require.NoError(t, grouped.Validate())

		derived, err := Derive(grouped, "derived_groups")
		require.NoError(t, err)
		derivedCategory, err := BindNullResultColumn[q2AcceptanceGroup, string](derived, "category")
		require.NoError(t, err)
		derivedCount, err := BindResultColumn[q2AcceptanceGroup, int64](derived, "total")
		require.NoError(t, err)
		derivedProjection, err := NewProjection([]ProjectionItem{
			NullItem("category", derivedCategory.NullExpr(), schema.TextType{}, "category.codec"),
			Item("total", derivedCount.Expr(), schema.IntegerType{}, "count.codec"),
		}, q2AcceptanceGroupDecoder{resultSchema: schemaValue})
		require.NoError(t, err)
		derivedQuery := Select(derived.Source(), derivedProjection)
		derivedRows := q2AcceptanceGroupRows(t, db, compiler, derivedQuery)
		require.Equal(t, []q2AcceptanceGroup{{Count: 2}, {Category: Nullable[string]{Value: "a", Valid: true}, Count: 1}, {Category: Nullable[string]{Value: "b", Valid: true}, Count: 1}}, derivedRows)

		cte, err := CTEOf("grouped", grouped)
		require.NoError(t, err)
		cteSource, err := cte.Source("grouped_alias")
		require.NoError(t, err)
		cteCategory, err := BindNullResultColumn[q2AcceptanceGroup, string](cteSource, "category")
		require.NoError(t, err)
		cteCount, err := BindResultColumn[q2AcceptanceGroup, int64](cteSource, "total")
		require.NoError(t, err)
		cteProjection, err := NewProjection([]ProjectionItem{
			NullItem("category", cteCategory.NullExpr(), schema.TextType{}, "category.codec"),
			Item("total", cteCount.Expr(), schema.IntegerType{}, "count.codec"),
		}, q2AcceptanceGroupDecoder{resultSchema: schemaValue})
		require.NoError(t, err)
		cteQuery, err := With(Select(cteSource.Source(), cteProjection), cte)
		require.NoError(t, err)
		cteRows := q2AcceptanceGroupRows(t, db, compiler, cteQuery)
		require.Equal(t, derivedRows, cteRows)
		require.Equal(t, schemaValue.Columns(), derivedQuery.Schema().Columns())
		require.Equal(t, schemaValue.Columns(), cteQuery.Schema().Columns())
	})

	t.Run("rejects an invalid CTE plan without panicking", func(t *testing.T) {
		base := q2AcceptanceQuery(t)
		zero := TypedCTE[q2AcceptanceRow]{}
		_, err := With(base, zero)
		q2RequirePlanCode(t, err, "invalid_cte")

		var typedNil *TypedCTE[q2AcceptanceRow]
		require.NotPanics(t, func() {
			_, err = With(base, typedNil)
		})
		q2RequirePlanCode(t, err, "invalid_cte")

		valid, err := CTEOf("items", base)
		require.NoError(t, err)
		_, err = valid.Source("")
		q2RequirePlanCode(t, err, "invalid_source")

		first, err := With(base, valid)
		require.NoError(t, err)
		_, err = With(first, valid)
		q2RequirePlanCode(t, err, "duplicate_cte")
	})

	t.Run("a count preserves a bad bind cause", func(t *testing.T) {
		base := q2AcceptanceQuery(t)
		bad := base
		bad.plan = clonePlan(base.plan)
		bad.plan.projection[0].bindErr = &PlanError{Code: "unsnapshotable_bind", Path: "bind", Detail: "bad bind", cause: q2AcceptanceBindCause{}}
		count := CountQuery(bad, true)
		err := count.Validate()
		var planErr *PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsnapshotable_bind", planErr.Code)
		var cause q2AcceptanceBindCause
		require.ErrorAs(t, err, &cause)
	})
}

type q2AcceptanceBindCause struct{}

func (q2AcceptanceBindCause) Error() string { return "q2 bind cause" }

func q2AcceptanceSQLite(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:q2_composition_acceptance?mode=memory&cache=shared")
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, db.Close()) })
	_, err = db.ExecContext(t.Context(), `CREATE TABLE q2_items (category TEXT, amount INTEGER)`)
	require.NoError(t, err)
	_, err = db.ExecContext(t.Context(), `INSERT INTO q2_items (category, amount) VALUES (NULL, 0), (NULL, 0), ('a', 1), ('b', 2)`)
	require.NoError(t, err)
	return db
}

func q2AcceptanceQuery(t *testing.T) Query[q2AcceptanceRow] {
	t.Helper()
	q, _ := q2AcceptanceQueryRelation(t)
	return q
}

// q2AcceptanceQueryRelation returns the same query alongside the relation it
// selects from, so a caller that needs to bind another column of the same
// source does not have to dig one back out of the built query.
func q2AcceptanceQueryRelation(t *testing.T) (Query[q2AcceptanceRow], TypedRelation[q2AcceptanceRow]) {
	t.Helper()
	table, err := ReadTableOf[q2AcceptanceRow](schema.TableDef{
		Name: "q2_items",
		Columns: []schema.ColumnDef{
			{Name: "category", Type: schema.TextType{}, Nullable: true},
			{Name: "amount", Type: schema.IntegerType{}},
		},
	})
	require.NoError(t, err)
	relation, err := SourceOf(table, "i")
	require.NoError(t, err)
	category, err := BindNullColumn[q2AcceptanceRow, string](relation, "category", "category.codec")
	require.NoError(t, err)
	amount, err := BindColumn[q2AcceptanceRow, int64](relation, "amount", "amount.codec")
	require.NoError(t, err)
	resultSchema, err := NewResultSchema(
		ResultColumn{Name: "category", Type: schema.TextType{}, Nullable: true, Codec: "category.codec"},
		ResultColumn{Name: "amount", Type: schema.IntegerType{}, Codec: "amount.codec"},
	)
	require.NoError(t, err)
	projection, err := NewProjection([]ProjectionItem{
		NullItem("category", category.NullExpr(), schema.TextType{}, "category.codec"),
		Item("amount", amount.Expr(), schema.IntegerType{}, "amount.codec"),
	}, q2AcceptanceDecoder{resultSchema: resultSchema})
	require.NoError(t, err)
	return Select(relation.Source(), projection), relation
}

func q2AcceptanceCompiler(t *testing.T) *querycompile.Compiler {
	t.Helper()
	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	compiler, err := querycompile.New(profile)
	require.NoError(t, err)
	return &compiler
}

func q2AcceptanceRows(t *testing.T, db *sql.DB, compiler *querycompile.Compiler, query Query[q2AcceptanceRow]) []q2AcceptanceRow {
	t.Helper()
	compiled, err := compileQuery(compiler, query)
	require.NoError(t, err)
	statement, err := compiled.Statement()
	require.NoError(t, err)
	rows, err := db.QueryContext(t.Context(), statement.SQL(), statement.Args()...)
	require.NoError(t, err)
	result := make([]q2AcceptanceRow, 0)
	for rows.Next() {
		var row q2AcceptanceRow
		require.NoError(t, query.Projection().Decoder().DecodeRow(rows, &row))
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return result
}

func q2AcceptanceGroupRows(t *testing.T, db *sql.DB, compiler *querycompile.Compiler, query Query[q2AcceptanceGroup]) []q2AcceptanceGroup {
	t.Helper()
	compiled, err := compileQuery(compiler, query)
	require.NoError(t, err)
	statement, err := compiled.Statement()
	require.NoError(t, err)
	rows, err := db.QueryContext(t.Context(), statement.SQL(), statement.Args()...)
	require.NoError(t, err)
	result := make([]q2AcceptanceGroup, 0)
	for rows.Next() {
		var row q2AcceptanceGroup
		require.NoError(t, query.Projection().Decoder().DecodeRow(rows, &row))
		result = append(result, row)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return result
}

func q2AcceptanceCountRows(t *testing.T, db *sql.DB, compiler *querycompile.Compiler, query Query[int64]) []int64 {
	t.Helper()
	compiled, err := compileQuery(compiler, query)
	require.NoError(t, err)
	statement, err := compiled.Statement()
	require.NoError(t, err)
	rows, err := db.QueryContext(t.Context(), statement.SQL(), statement.Args()...)
	require.NoError(t, err)
	result := make([]int64, 0)
	for rows.Next() {
		var value int64
		require.NoError(t, query.Projection().Decoder().DecodeRow(rows, &value))
		result = append(result, value)
	}
	require.NoError(t, rows.Err())
	require.NoError(t, rows.Close())
	return result
}

func q2AcceptanceCountValue(t *testing.T, db *sql.DB, compiler *querycompile.Compiler, query Query[int64]) int64 {
	values := q2AcceptanceCountRows(t, db, compiler, query)
	require.Len(t, values, 1)
	return values[0]
}

func baseRowsExpected() []q2AcceptanceRow {
	return []q2AcceptanceRow{{Category: Nullable[string]{Valid: false}, Amount: 0}, {Category: Nullable[string]{Valid: false}, Amount: 0}, {Category: Nullable[string]{Value: "a", Valid: true}, Amount: 1}, {Category: Nullable[string]{Value: "b", Valid: true}, Amount: 2}}
}

func q2RequirePlanCode(t *testing.T, err error, code string) {
	t.Helper()
	var planErr *PlanError
	require.Error(t, err)
	require.True(t, errors.As(err, &planErr), fmt.Sprintf("expected PlanError, got %T: %v", err, err))
	require.Equal(t, code, planErr.Code)
}

type partitionRow struct{ ID int64 }
type nullOrderRow struct{ ID Nullable[int64] }
type partitionDecoder struct{ schema ResultSchema }

func (d partitionDecoder) ResultSchema() ResultSchema              { return d.schema }
func (partitionDecoder) Presence() []Presence                      { return nil }
func (partitionDecoder) DecodeRow(ScanSource, *partitionRow) error { return nil }

func TestCompositionCompiler(t *testing.T) {
	t.Run("matches base occurrences", func(t *testing.T) {
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
	})

	t.Run("unwrapping bind tokens preserves named values", func(t *testing.T) {
		value, copier, err := adoptBind(42, true)
		require.NoError(t, err)
		statement := stmt.New(sqltext.Text("SELECT ?"), bindToken{id: 7, value: value, copy: copier, codec: "int"})
		compiled, err := unwrapBindTokens(statement)
		require.NoError(t, err)
		require.Equal(t, []any{42}, compiled.statement.Args())
		require.Equal(t, []bindSlot{{id: 7, codec: "int"}}, compiled.bindSlots)
	})

	t.Run("a partition limit lowers to ROW_NUMBER", func(t *testing.T) {
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
	})

	t.Run("a partition limit preserves an unsupported window cause", func(t *testing.T) {
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
	})

	t.Run("null order on SQLite and render profiles", func(t *testing.T) {
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
	})

	t.Run("a partition limit rejects zero nodes and invalid null placement", func(t *testing.T) {
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
	})
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

func TestQueryAPICompiles(t *testing.T) {
	directory, err := filepath.Abs(filepath.Join("testdata", "compile", "query_api", "positive"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "test", "./...")
	command.Dir = directory
	// GOCACHE is deliberately not overridden here: the ambient build cache
	// already holds rasql and its dependencies from the surrounding `go test
	// ./...` run, and a fresh per-call GOCACHE bought no isolation this
	// correctness check needs -- it only forced std-lib-adjacent
	// dependencies to compile from scratch on every call.
	command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compile fixture failed: %v\n%s", err, output)
	}
}

func TestQueryAPIRejectsInvalidCompileFixtures(t *testing.T) {
	for _, fixture := range []struct{ name, diagnostic string }{{"wrong_value", "cannot use"}, {"is_null_nonnull", "does not match"}, {"scalar_where", "cannot use"}, {"mismatched_projection", "cannot use"}, {"optional_required", "does not match"}} {
		directory, err := filepath.Abs(filepath.Join("testdata", "compile", "query_api", fixture.name))
		if err != nil {
			t.Fatal(err)
		}
		command := exec.Command("go", "test", "./...")
		command.Dir = directory
		command.Env = append(command.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod")
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), fixture.diagnostic) {
			t.Fatalf("fixture %s diagnostic = %s", fixture.name, output)
		}
	}
}
