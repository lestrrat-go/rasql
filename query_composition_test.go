package rasql_test

import (
	"database/sql"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
	_ "modernc.org/sqlite"
)

type q2AcceptanceRow struct {
	Category rasql.Nullable[string]
	Amount   int64
}

type q2AcceptanceDecoder struct{ resultSchema rasql.ResultSchema }

func (d q2AcceptanceDecoder) ResultSchema() rasql.ResultSchema { return d.resultSchema }
func (q2AcceptanceDecoder) Presence() []rasql.Presence         { return nil }
func (q2AcceptanceDecoder) DecodeRow(source rasql.ScanSource, row *q2AcceptanceRow) error {
	var category sql.NullString
	if err := source.Scan(&category, &row.Amount); err != nil {
		return err
	}
	row.Category = rasql.Nullable[string]{Value: category.String, Valid: category.Valid}
	return nil
}

type q2AcceptanceGroup struct {
	Category rasql.Nullable[string]
	Count    int64
}

type q2AcceptanceGroupDecoder struct{ resultSchema rasql.ResultSchema }

func (d q2AcceptanceGroupDecoder) ResultSchema() rasql.ResultSchema { return d.resultSchema }
func (q2AcceptanceGroupDecoder) Presence() []rasql.Presence         { return nil }
func (q2AcceptanceGroupDecoder) DecodeRow(source rasql.ScanSource, row *q2AcceptanceGroup) error {
	var category sql.NullString
	if err := source.Scan(&category, &row.Count); err != nil {
		return err
	}
	row.Category = rasql.Nullable[string]{Value: category.String, Valid: category.Valid}
	return nil
}

func TestQueryComposition(t *testing.T) {
	t.Run("executes compound outer operations and counts", func(t *testing.T) {
		db := q2AcceptanceSQLite(t)
		base := q2AcceptanceQuery(t)
		compiler := q2AcceptanceCompiler(t)

		union, err := rasql.Combine(base, rasql.Union, base)
		require.NoError(t, err)
		unionRows := q2AcceptanceRows(t, db, compiler, union)
		require.Equal(t, []q2AcceptanceRow{{Category: rasql.Nullable[string]{Valid: false}, Amount: 0}, {Category: rasql.Nullable[string]{Value: "a", Valid: true}, Amount: 1}, {Category: rasql.Nullable[string]{Value: "b", Valid: true}, Amount: 2}}, unionRows)

		all, err := rasql.Combine(base, rasql.UnionAll, base)
		require.NoError(t, err)
		allRows := q2AcceptanceRows(t, db, compiler, all)
		require.Len(t, allRows, 8)
		require.Equal(t, allRows[:4], allRows[4:])

		filtered := all.Where(rasql.EqualExpr(rasql.Value(1), rasql.Value(2))).Where(rasql.EqualExpr(rasql.Value(3), rasql.Value(4)))
		filteredRows := q2AcceptanceRows(t, db, compiler, filtered)
		require.Empty(t, filteredRows)

		ordered := all.OrderBy(rasql.AscExpr(rasql.Value(int64(1))))
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

		projectedSchema, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "category", Type: schema.TextType{}, Codec: "category.codec"},
			rasql.ResultColumn{Name: "amount", Type: schema.IntegerType{}, Codec: "amount.codec"},
		)
		require.NoError(t, err)
		projected, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.Item("category", rasql.Value("projected"), schema.TextType{}, "category.codec"),
			rasql.Item("amount", rasql.Value(int64(99)), schema.IntegerType{}, "amount.codec"),
		}, q2AcceptanceDecoder{resultSchema: projectedSchema})
		require.NoError(t, err)
		projectedQuery := rasql.Project(all.Plan(), projected)
		projectedRows := q2AcceptanceRows(t, db, compiler, projectedQuery)
		require.Len(t, projectedRows, 8)
		for _, row := range projectedRows {
			require.Equal(t, q2AcceptanceRow{Category: rasql.Nullable[string]{Value: "projected", Valid: true}, Amount: 99}, row)
		}

		cte, err := rasql.CTEOf("categories", base)
		require.NoError(t, err)
		withSource, err := cte.Source("c")
		require.NoError(t, err)
		withCategory, err := rasql.BindNullResultColumn[q2AcceptanceRow, string](withSource, "category")
		require.NoError(t, err)
		withAmount, err := rasql.BindResultColumn[q2AcceptanceRow, int64](withSource, "amount")
		require.NoError(t, err)
		withProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.NullItem("category", withCategory.NullExpr(), schema.TextType{}, "category.codec"),
			rasql.Item("amount", withAmount.Expr(), schema.IntegerType{}, "amount.codec"),
		}, q2AcceptanceDecoder{resultSchema: base.Schema()})
		require.NoError(t, err)
		withQuery, err := rasql.With(rasql.Select(withSource.Source(), withProjection), cte)
		require.NoError(t, err)
		withRows := q2AcceptanceRows(t, db, compiler, withQuery)
		require.Equal(t, baseRowsExpected(), withRows)

		countAll := rasql.CountQuery(offset, true)
		require.Equal(t, int64(1), q2AcceptanceCountValue(t, db, compiler, countAll))
		countUnpaged := rasql.CountQuery(offset, false)
		require.Equal(t, int64(8), q2AcceptanceCountValue(t, db, compiler, countUnpaged))

		operand, err := base.Limit(2)
		require.NoError(t, err)
		pagedUnion, err := rasql.Combine(operand, rasql.UnionAll, operand)
		require.NoError(t, err)
		outerPaged, err := pagedUnion.Limit(1)
		require.NoError(t, err)
		require.Len(t, q2AcceptanceRows(t, db, compiler, outerPaged), 1)
		require.Equal(t, int64(1), q2AcceptanceCountValue(t, db, compiler, rasql.CountQuery(outerPaged, true)))
		require.Equal(t, int64(4), q2AcceptanceCountValue(t, db, compiler, rasql.CountQuery(outerPaged, false)))
	})

	t.Run("reuses a grouped DTO through a derived table and a CTE", func(t *testing.T) {
		db := q2AcceptanceSQLite(t)
		compiler := q2AcceptanceCompiler(t)
		table, err := rasql.ReadTableOf[q2AcceptanceRow](schema.TableDef{
			Name: "q2_items",
			Columns: []schema.ColumnDef{
				{Name: "category", Type: schema.TextType{}, Nullable: true},
				{Name: "amount", Type: schema.IntegerType{}},
			},
		})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "i")
		require.NoError(t, err)
		category, err := rasql.BindNullColumn[q2AcceptanceRow, string](relation, "category", "category.codec")
		require.NoError(t, err)
		amount, err := rasql.BindColumn[q2AcceptanceRow, int64](relation, "amount", "amount.codec")
		require.NoError(t, err)
		schemaValue, err := rasql.NewResultSchema(
			rasql.ResultColumn{Name: "category", Type: schema.TextType{}, Nullable: true, Codec: "category.codec"},
			rasql.ResultColumn{Name: "total", Type: schema.IntegerType{}, Codec: "count.codec"},
		)
		require.NoError(t, err)
		groupProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.NullItem("category", category.NullExpr(), schema.TextType{}, "category.codec"),
			rasql.Item("total", rasql.CountExpr(amount.Expr()), schema.IntegerType{}, "count.codec"),
		}, q2AcceptanceGroupDecoder{resultSchema: schemaValue})
		require.NoError(t, err)
		grouped := rasql.Select(relation.Source(), groupProjection).GroupBy(rasql.GroupNull(category.NullExpr())).OrderBy(rasql.AscNull(category.NullExpr(), rasql.NullsFirst))
		require.NoError(t, grouped.Validate())

		derived, err := rasql.Derive(grouped, "derived_groups")
		require.NoError(t, err)
		derivedCategory, err := rasql.BindNullResultColumn[q2AcceptanceGroup, string](derived, "category")
		require.NoError(t, err)
		derivedCount, err := rasql.BindResultColumn[q2AcceptanceGroup, int64](derived, "total")
		require.NoError(t, err)
		derivedProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.NullItem("category", derivedCategory.NullExpr(), schema.TextType{}, "category.codec"),
			rasql.Item("total", derivedCount.Expr(), schema.IntegerType{}, "count.codec"),
		}, q2AcceptanceGroupDecoder{resultSchema: schemaValue})
		require.NoError(t, err)
		derivedQuery := rasql.Select(derived.Source(), derivedProjection)
		derivedRows := q2AcceptanceGroupRows(t, db, compiler, derivedQuery)
		require.Equal(t, []q2AcceptanceGroup{{Count: 2}, {Category: rasql.Nullable[string]{Value: "a", Valid: true}, Count: 1}, {Category: rasql.Nullable[string]{Value: "b", Valid: true}, Count: 1}}, derivedRows)

		cte, err := rasql.CTEOf("grouped", grouped)
		require.NoError(t, err)
		cteSource, err := cte.Source("grouped_alias")
		require.NoError(t, err)
		cteCategory, err := rasql.BindNullResultColumn[q2AcceptanceGroup, string](cteSource, "category")
		require.NoError(t, err)
		cteCount, err := rasql.BindResultColumn[q2AcceptanceGroup, int64](cteSource, "total")
		require.NoError(t, err)
		cteProjection, err := rasql.NewProjection([]rasql.ProjectionItem{
			rasql.NullItem("category", cteCategory.NullExpr(), schema.TextType{}, "category.codec"),
			rasql.Item("total", cteCount.Expr(), schema.IntegerType{}, "count.codec"),
		}, q2AcceptanceGroupDecoder{resultSchema: schemaValue})
		require.NoError(t, err)
		cteQuery, err := rasql.With(rasql.Select(cteSource.Source(), cteProjection), cte)
		require.NoError(t, err)
		cteRows := q2AcceptanceGroupRows(t, db, compiler, cteQuery)
		require.Equal(t, derivedRows, cteRows)
		require.Equal(t, schemaValue.Columns(), derivedQuery.Schema().Columns())
		require.Equal(t, schemaValue.Columns(), cteQuery.Schema().Columns())
	})

	t.Run("rejects an invalid CTE plan without panicking", func(t *testing.T) {
		base := q2AcceptanceQuery(t)
		zero := rasql.TypedCTE[q2AcceptanceRow]{}
		_, err := rasql.With(base, zero)
		q2RequirePlanCode(t, err, "invalid_cte")

		valid, err := rasql.CTEOf("items", base)
		require.NoError(t, err)
		_, err = valid.Source("")
		q2RequirePlanCode(t, err, "invalid_source")

		first, err := rasql.With(base, valid)
		require.NoError(t, err)
		_, err = rasql.With(first, valid)
		q2RequirePlanCode(t, err, "duplicate_cte")
	})

	t.Run("a count preserves a bad bind cause", func(t *testing.T) {
		bad := q2BadBindQuery(t)
		count := rasql.CountQuery(bad, true)
		err := count.Validate()
		var planErr *rasql.PlanError
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

func q2AcceptanceQuery(t *testing.T) rasql.Query[q2AcceptanceRow] {
	t.Helper()
	q, _ := q2AcceptanceQueryRelation(t)
	return q
}

// q2AcceptanceQueryRelation returns the same query alongside the relation it
// selects from, so a caller that needs to bind another column of the same
// source does not have to dig one back out of the built query.
func q2AcceptanceQueryRelation(t *testing.T) (rasql.Query[q2AcceptanceRow], rasql.TypedRelation[q2AcceptanceRow]) {
	t.Helper()
	table, err := rasql.ReadTableOf[q2AcceptanceRow](schema.TableDef{
		Name: "q2_items",
		Columns: []schema.ColumnDef{
			{Name: "category", Type: schema.TextType{}, Nullable: true},
			{Name: "amount", Type: schema.IntegerType{}},
		},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "i")
	require.NoError(t, err)
	category, err := rasql.BindNullColumn[q2AcceptanceRow, string](relation, "category", "category.codec")
	require.NoError(t, err)
	amount, err := rasql.BindColumn[q2AcceptanceRow, int64](relation, "amount", "amount.codec")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "category", Type: schema.TextType{}, Nullable: true, Codec: "category.codec"},
		rasql.ResultColumn{Name: "amount", Type: schema.IntegerType{}, Codec: "amount.codec"},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.NullItem("category", category.NullExpr(), schema.TextType{}, "category.codec"),
		rasql.Item("amount", amount.Expr(), schema.IntegerType{}, "amount.codec"),
	}, q2AcceptanceDecoder{resultSchema: resultSchema})
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection), relation
}

func q2AcceptanceCompiler(t *testing.T) *querycompile.Compiler {
	t.Helper()
	profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
	require.NoError(t, err)
	compiler, err := querycompile.New(profile)
	require.NoError(t, err)
	return &compiler
}

func q2AcceptanceRows(t *testing.T, db *sql.DB, compiler *querycompile.Compiler, query rasql.Query[q2AcceptanceRow]) []q2AcceptanceRow {
	t.Helper()
	compiled, err := rasql.Q1CompileQuery(rasql.Q1CompilerFor(compiler), query)
	require.NoError(t, err)
	statement, err := compiled.Copy()
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

func q2AcceptanceGroupRows(t *testing.T, db *sql.DB, compiler *querycompile.Compiler, query rasql.Query[q2AcceptanceGroup]) []q2AcceptanceGroup {
	t.Helper()
	compiled, err := rasql.Q1CompileQuery(rasql.Q1CompilerFor(compiler), query)
	require.NoError(t, err)
	statement, err := compiled.Copy()
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

func q2AcceptanceCountRows(t *testing.T, db *sql.DB, compiler *querycompile.Compiler, query rasql.Query[int64]) []int64 {
	t.Helper()
	compiled, err := rasql.Q1CompileQuery(rasql.Q1CompilerFor(compiler), query)
	require.NoError(t, err)
	statement, err := compiled.Copy()
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

func q2AcceptanceCountValue(t *testing.T, db *sql.DB, compiler *querycompile.Compiler, query rasql.Query[int64]) int64 {
	values := q2AcceptanceCountRows(t, db, compiler, query)
	require.Len(t, values, 1)
	return values[0]
}

func baseRowsExpected() []q2AcceptanceRow {
	return []q2AcceptanceRow{{Category: rasql.Nullable[string]{Valid: false}, Amount: 0}, {Category: rasql.Nullable[string]{Valid: false}, Amount: 0}, {Category: rasql.Nullable[string]{Value: "a", Valid: true}, Amount: 1}, {Category: rasql.Nullable[string]{Value: "b", Valid: true}, Amount: 2}}
}

func q2RequirePlanCode(t *testing.T, err error, code string) {
	t.Helper()
	var planErr *rasql.PlanError
	require.Error(t, err)
	require.True(t, errors.As(err, &planErr), fmt.Sprintf("expected PlanError, got %T: %v", err, err))
	require.Equal(t, code, planErr.Code)
}

type nullOrderRow struct{ ID rasql.Nullable[int64] }

func TestCompositionCompiler(t *testing.T) {

	t.Run("a partition limit lowers to ROW_NUMBER", func(t *testing.T) {
		schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}})
		require.NoError(t, err)
		table, err := rasql.ReadTableOf[partitionRow](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		require.NoError(t, err)
		relation, err := rasql.SourceOf(table, "i")
		require.NoError(t, err)
		column, err := rasql.BindColumn[partitionRow, int64](relation, "id", "")
		require.NoError(t, err)
		projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("id", column.Expr(), schema.IntegerType{}, "")}, partitionDecoder{result: schemaValue})
		require.NoError(t, err)
		q, err := rasql.Q1WithPartitionLimit(rasql.Select(relation.Source(), projection), []rasql.GroupKey{rasql.Group(column.Expr())}, []rasql.OrderTerm{rasql.AscExpr(column.Expr())}, 2)
		require.NoError(t, err)
		profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
		require.NoError(t, err)
		compiler, err := querycompile.New(profile)
		require.NoError(t, err)
		compiled, err := rasql.Q1CompileQuery(rasql.Q1CompilerFor(&compiler), q)
		require.NoError(t, err)
		statement := compiled.Statement
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
		_, err = rasql.Q1WithPartitionLimit(q, nil, []rasql.OrderTerm{rasql.AscExpr(column.Expr())}, 2)
		var planErr *rasql.PlanError
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
		_, err = rasql.Q1CompileQuery(rasql.Q1CompilerFor(&compiler), partitionQuery(t))
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_feature", planErr.Code)
		require.True(t, errors.Is(err, engineprofile.ErrUnsupportedFeature))
	})

	t.Run("null order on SQLite and render profiles", func(t *testing.T) {
		for _, tc := range []struct {
			name       string
			placement  rasql.NullOrder
			descending bool
			want       []any
		}{
			{"asc first", rasql.NullsFirst, false, []any{nil, int64(1), int64(2)}},
			{"asc last", rasql.NullsLast, false, []any{int64(1), int64(2), nil}},
			{"desc first", rasql.NullsFirst, true, []any{nil, int64(2), int64(1)}},
			{"desc last", rasql.NullsLast, true, []any{int64(2), int64(1), nil}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				nullColumn, table := nullableColumn(t)
				term := rasql.AscNull(nullColumn.NullExpr(), tc.placement)
				if tc.descending {
					term = rasql.DescNull(nullColumn.NullExpr(), tc.placement)
				}
				q := nullableOrderQuery(t, table, nullColumn, term)
				profile, err := engineprofile.Builtin("sqlite-3.35", engineprofile.Version{Known: true, Major: 3, Minor: 35})
				require.NoError(t, err)
				compiler, err := querycompile.New(profile)
				require.NoError(t, err)
				compiled, err := rasql.Q1CompileQuery(rasql.Q1CompilerFor(&compiler), q)
				require.NoError(t, err)
				db, err := sql.Open("sqlite", ":memory:")
				require.NoError(t, err)
				defer func() { require.NoError(t, db.Close()) }()
				_, err = db.ExecContext(t.Context(), `CREATE TABLE nullable_items (id INTEGER)`)
				require.NoError(t, err)
				_, err = db.ExecContext(t.Context(), `INSERT INTO nullable_items (id) VALUES (NULL), (1), (2)`)
				require.NoError(t, err)
				rows, err := db.QueryContext(t.Context(), compiled.Statement.SQL(), compiled.Statement.Args()...)
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
		q := nullableOrderQuery(t, table, nullColumn, rasql.AscNull(nullColumn.NullExpr(), rasql.NullsLast))
		pg, err := engineprofile.Builtin("postgresql-17", engineprofile.Version{Known: true, Major: 17})
		require.NoError(t, err)
		pgCompiler, err := querycompile.New(pg)
		require.NoError(t, err)
		pgStatement, err := rasql.Q1CompileQuery(rasql.Q1CompilerFor(&pgCompiler), q)
		require.NoError(t, err)
		require.Contains(t, pgStatement.Statement.SQL(), "NULLS LAST")
		my, err := engineprofile.Builtin("mysql-8.4", engineprofile.Version{Known: true, Major: 8, Minor: 4})
		require.NoError(t, err)
		myCompiler, err := querycompile.New(my)
		require.NoError(t, err)
		_, err = rasql.Q1CompileQuery(rasql.Q1CompilerFor(&myCompiler), q)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsupported_feature", planErr.Code)
	})

	t.Run("a partition limit rejects zero nodes and invalid null placement", func(t *testing.T) {
		q := partitionQuery(t)
		_, err := rasql.Q1WithPartitionLimit(q, []rasql.GroupKey{{}}, []rasql.OrderTerm{rasql.AscExpr(rasql.Expr[int64]{})}, 2)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_partition_limit", planErr.Code)
		table := query.MustTableRef(schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
		selectBody, err := query.NewSelect(table, table.Column("id"))
		require.NoError(t, err)
		_, err = selectBody.WithOrder(query.AscNulls(table.Column("id"), query.NullPlacement(99)))
		require.Error(t, err)
	})
}

func nullableColumn(t *testing.T) (rasql.NullColumn[nullOrderRow, int64], rasql.ReadTable[nullOrderRow]) {
	t.Helper()
	table, err := rasql.ReadTableOf[nullOrderRow](schema.TableDef{Name: "nullable_items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}, Nullable: true}}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "n")
	require.NoError(t, err)
	column, err := rasql.BindNullColumn[nullOrderRow, int64](relation, "id", "")
	require.NoError(t, err)
	return column, table
}

func nullableOrderQuery(t *testing.T, table rasql.ReadTable[nullOrderRow], column rasql.NullColumn[nullOrderRow, int64], term rasql.OrderTerm) rasql.Query[rasql.Nullable[int64]] {
	t.Helper()
	schemaValue, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "id", Type: schema.IntegerType{}, Nullable: true})
	require.NoError(t, err)
	projection, err := rasql.NullableScalar("id", column.NullExpr(), schema.IntegerType{}, "")
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "n")
	require.NoError(t, err)
	_ = schemaValue
	return rasql.Select(relation.Source(), projection).OrderBy(term)
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

// q2BadBindQuery projects a value whose bind snapshot fails, so the query
// carries the bind error a later step has to report.
func q2BadBindQuery(t *testing.T) rasql.Query[q2AcceptanceRow] {
	t.Helper()
	_, relation := q2AcceptanceQueryRelation(t)
	category, err := rasql.BindNullColumn[q2AcceptanceRow, string](relation, "category", "category.codec")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "category", Type: schema.TextType{}, Nullable: true, Codec: "category.codec"},
		rasql.ResultColumn{Name: "amount", Type: schema.IntegerType{}, Codec: "amount.codec"},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.NullItem("category", category.NullExpr(), schema.TextType{}, "category.codec"),
		rasql.Item("amount", rasql.Value(q2BadBind(1)), schema.IntegerType{}, "amount.codec"),
	}, q2AcceptanceDecoder{resultSchema: resultSchema})
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection)
}

// q2BadBind refuses to snapshot, which is how a projection reaches the bind
// error path without one being written in by hand.
type q2BadBind int64

func (q2BadBind) SnapshotBind() (q2BadBind, error) { return 0, q2AcceptanceBindCause{} }
