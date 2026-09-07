package rasql

import (
	"database/sql"
	"errors"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/internal/querycompile"
	"github.com/lestrrat-go/rasql/schema"
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

type q2AcceptanceCountDecoder struct{ resultSchema ResultSchema }

func (d q2AcceptanceCountDecoder) ResultSchema() ResultSchema { return d.resultSchema }
func (q2AcceptanceCountDecoder) Presence() []Presence         { return nil }
func (q2AcceptanceCountDecoder) DecodeRow(source ScanSource, value *int64) error {
	return source.Scan(value)
}

func TestQ2CompositionExecutesCompoundOuterOperationsAndCounts(t *testing.T) {
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
}

func TestQ2CompositionReusesGroupedDTOThroughDerivedAndCTE(t *testing.T) {
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
}

func TestQ2CompositionRejectsInvalidCTEPlansWithoutPanic(t *testing.T) {
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
}

type q2AcceptanceBindCause struct{}

func (q2AcceptanceBindCause) Error() string { return "q2 bind cause" }

type q2AcceptanceBadBind string

func (q2AcceptanceBadBind) SnapshotBind() (q2AcceptanceBadBind, error) {
	return q2AcceptanceBadBind("bad"), q2AcceptanceBindCause{}
}

func TestQ2CompositionCountPreservesBadBindCause(t *testing.T) {
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
}

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
	return Select(relation.Source(), projection)
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
