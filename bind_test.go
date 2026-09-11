package rasql_test

import (
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

type bindCopyShape struct {
	Array [2]int
	Ptr   *int
	Empty []byte
	Nil   []byte
	Map   map[string][]byte
	Any   any
	Named sql.NamedArg
}

func tokenOf[T any](expression rasql.Expr[T]) bindplan.Token {
	return rasql.Q1BindToken(expression)
}

func TestBindCopy(t *testing.T) {
	t.Run("detaches nested values", func(t *testing.T) {
		number := 7
		input := bindCopyShape{
			Array: [2]int{1, 2}, Ptr: &number, Empty: []byte{}, Map: map[string][]byte{"x": {3}},
			Any: []byte{4}, Named: sql.Named("inner", []byte{5}),
		}
		expression := rasql.Value(input)
		token := tokenOf(expression)
		input.Array[0], number, input.Map["x"][0] = 99, 88, 77
		input.Any.([]byte)[0], input.Named.Value.([]byte)[0] = 66, 55
		compiled, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?"), token))
		require.NoError(t, err)
		first, err := compiled.Copy()
		require.NoError(t, err)
		got := first.BoundArgs()[0].(bindCopyShape)
		require.Equal(t, [2]int{1, 2}, got.Array)
		require.Equal(t, 7, *got.Ptr)
		require.NotNil(t, got.Empty)
		require.Nil(t, got.Nil)
		require.Equal(t, []byte{3}, got.Map["x"])
		require.Equal(t, []byte{4}, got.Any)
		require.Equal(t, []byte{5}, got.Named.Value)
		got.Map["x"][0] = 1
		got.Array[0], *got.Ptr = 2, 3
		got.Empty = append(got.Empty, 8)
		got.Any.([]byte)[0] = 9
		got.Named.Value.([]byte)[0] = 10
		second, err := compiled.Copy()
		require.NoError(t, err)
		require.Equal(t, []byte{3}, second.BoundArgs()[0].(bindCopyShape).Map["x"])
		secondShape := second.BoundArgs()[0].(bindCopyShape)
		require.Equal(t, [2]int{1, 2}, secondShape.Array)
		require.Equal(t, 7, *secondShape.Ptr)
		require.Equal(t, []byte{4}, secondShape.Any)
		require.Equal(t, []byte{5}, secondShape.Named.Value)
		require.Equal(t, []byte{8}, got.Empty)
		require.NotNil(t, secondShape.Empty)
		require.Equal(t, []byte{}, secondShape.Empty)
		require.Nil(t, secondShape.Nil)
		compiledAgain, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?"), token))
		require.NoError(t, err)
		third, err := compiledAgain.Copy()
		require.NoError(t, err)
		require.Equal(t, secondShape, third.BoundArgs()[0])
	})

	t.Run("repeated occurrence uses independent copies", func(t *testing.T) {
		expression := rasql.Value([]byte("x"))
		token := tokenOf(expression)
		compiled, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?, ?"), token, token))
		require.NoError(t, err)
		require.Equal(t, compiled.Slots[0], compiled.Slots[1])
		statement, err := compiled.Copy()
		require.NoError(t, err)
		args := statement.BoundArgs()
		args[0].([]byte)[0] = 'z'
		require.Equal(t, []byte("x"), args[1])
		other := rasql.Value([]byte("x"))
		require.NotEqual(t, tokenOf(expression).ID, tokenOf(other).ID)
	})

	t.Run("snapshotter runs once", func(t *testing.T) {
		calls := &atomic.Int32{}
		expression := rasql.Value(struct{ Value bindCopyOpaque }{Value: bindCopyOpaque{value: []byte("x"), calls: calls}})
		token := tokenOf(expression)
		compiled, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?"), token))
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			_, err = compiled.Copy()
			require.NoError(t, err)
		}
		require.Equal(t, int32(1), calls.Load())
	})

	t.Run("nested opaque snapshotters run once per logical value", func(t *testing.T) {
		calls := &atomic.Int32{}
		values := []any{
			struct{ Value bindCopyOpaque }{Value: bindCopyOpaque{value: []byte("struct"), calls: calls}},
			[]bindCopyOpaque{{value: []byte("slice"), calls: calls}},
			map[string]bindCopyOpaque{"value": {value: []byte("map"), calls: calls}},
			sql.Named("named", bindCopyOpaque{value: []byte("named"), calls: calls}),
		}
		for index, value := range values {
			expression := rasql.Value(value)
			token := tokenOf(expression)
			compiled, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?"), token))
			require.NoError(t, err)
			for i := 0; i < 3; i++ {
				statement, copyErr := compiled.Copy()
				require.NoError(t, copyErr)
				switch index {
				case 0:
					require.Equal(t, []byte("struct"), statement.BoundArgs()[0].(struct{ Value bindCopyOpaque }).Value.value)
				case 1:
					require.Equal(t, []byte("slice"), statement.BoundArgs()[0].([]bindCopyOpaque)[0].value)
				case 2:
					require.Equal(t, []byte("map"), statement.BoundArgs()[0].(map[string]bindCopyOpaque)["value"].value)
				case 3:
					require.Equal(t, []byte("named"), statement.BoundArgs()[0].(sql.NamedArg).Value.(bindCopyOpaque).value)
				}
			}
		}
		require.Equal(t, int32(4), calls.Load())
	})

	t.Run("legacy and named arguments", func(t *testing.T) {
		legacy, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?, ?"), []byte("x"), sql.Named("name", []byte("y"))))
		require.NoError(t, err)
		first, err := legacy.Copy()
		require.NoError(t, err)
		first.BoundArgs()[0].([]byte)[0] = 'z'
		second, err := legacy.Copy()
		require.NoError(t, err)
		require.Equal(t, []byte("x"), second.BoundArgs()[0])
		require.Equal(t, "name", second.BoundArgs()[1].(sql.NamedArg).Name)
		nilBytes := []byte(nil)
		legacyGraph, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?, ?"), nilBytes, struct {
			Array [2][]byte
			Map   map[string][]byte
		}{Array: [2][]byte{{1}, nil}, Map: map[string][]byte{"x": {2}}}))
		require.NoError(t, err)
		legacyFirst, err := legacyGraph.Copy()
		require.NoError(t, err)
		legacyValue := legacyFirst.BoundArgs()[1].(struct {
			Array [2][]byte
			Map   map[string][]byte
		})
		legacyValue.Array[0][0] = 9
		legacyValue.Map["x"][0] = 8
		legacySecond, err := legacyGraph.Copy()
		require.NoError(t, err)
		require.Nil(t, legacySecond.BoundArgs()[0])
		require.Equal(t, []byte{1}, legacySecond.BoundArgs()[1].(struct {
			Array [2][]byte
			Map   map[string][]byte
		}).Array[0])
		require.Equal(t, []byte{2}, legacySecond.BoundArgs()[1].(struct {
			Array [2][]byte
			Map   map[string][]byte
		}).Map["x"])
		_, err = bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?"), bindCopyLegacy{value: []byte("x")}))
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsnapshotable_bind", planErr.Code)
	})

	t.Run("named token shapes and alignment", func(t *testing.T) {
		directExpression, err := rasql.ValueWithCodec(sql.Named("inner", []byte("value")), "bytes.codec")
		require.NoError(t, err)
		directToken := tokenOf(directExpression)
		value, copier, err := bindplan.Adopt([]byte("outer"), true)
		require.NoError(t, err)
		syntheticToken := bindplan.Token{ID: 91, Codec: "text.codec", Value: value, Copy: copier}
		compiled, err := bindplan.Unwrap(stmt.New(sqltext.Text("SELECT ?, ?"), directToken, sql.Named("outer", syntheticToken)))
		require.NoError(t, err)
		require.Equal(t, []bindplan.Slot{{ID: directToken.ID, Codec: "bytes.codec"}, {ID: 91, Codec: "text.codec"}}, compiled.Slots)
		statement, err := compiled.Copy()
		require.NoError(t, err)
		args := statement.BoundArgs()
		require.Equal(t, "inner", args[0].(sql.NamedArg).Name)
		require.Equal(t, []byte("value"), args[0].(sql.NamedArg).Value)
		require.Equal(t, "outer", args[1].(sql.NamedArg).Name)
		require.Equal(t, []byte("outer"), args[1].(sql.NamedArg).Value)
		_, nested := args[1].(sql.NamedArg).Value.(bindplan.Token)
		require.False(t, nested)
	})

	t.Run("atomic errors and alignment", func(t *testing.T) {
		sentinel := errors.New("copy failed")
		laterCalled := false
		compiled := bindplan.Compiled{
			Statement: stmt.New(sqltext.Text("SELECT ?, ?"), 1, 2),
			Slots:     []bindplan.Slot{{}, {}},
			CopyArgs: []bindplan.ValueCopy{
				func() (any, error) { return nil, sentinel },
				func() (any, error) { laterCalled = true; return 2, nil },
			},
		}
		statement, err := compiled.Copy()
		var planErr *rasql.PlanError
		require.ErrorIs(t, err, sentinel)
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "unsnapshotable_bind", planErr.Code)
		require.Equal(t, "args[0]", planErr.Path)
		require.Equal(t, stmt.Statement{}, statement)
		require.False(t, laterCalled)
		for _, invalid := range []bindplan.Compiled{
			{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: []bindplan.Slot{{}}, CopyArgs: []bindplan.ValueCopy{nil}},
			{Statement: stmt.New(sqltext.Text("SELECT ?"), 1), Slots: nil, CopyArgs: []bindplan.ValueCopy{func() (any, error) { return 1, nil }}},
		} {
			statement, err := invalid.Copy()
			require.ErrorAs(t, err, &planErr)
			require.Equal(t, stmt.Statement{}, statement)
		}
	})

	t.Run("rendered order across composition shapes", func(t *testing.T) {
		firstExpression, err := rasql.ValueWithCodec([]byte("first"), "bytes.codec")
		require.NoError(t, err)
		secondExpression, err := rasql.ValueWithCodec(sql.Named("named", []byte("second")), "named.codec")
		require.NoError(t, err)
		firstToken, secondToken := tokenOf(firstExpression), tokenOf(secondExpression)
		legacy := struct {
			Values []byte
		}{Values: []byte("legacy")}
		statement := stmt.New(sqltext.Text("SELECT ?, ?, ?, ?, ?, ?, ?"),
			firstToken, sql.Named("outer", secondToken), firstToken, nil, legacy, secondToken, sql.NamedArg{Name: "empty"},
		)
		compiled, err := bindplan.Unwrap(statement)
		require.NoError(t, err)
		require.Len(t, compiled.Slots, 7)
		require.Equal(t, []bindplan.ID{firstToken.ID, secondToken.ID, firstToken.ID, 0, 0, secondToken.ID, 0}, []bindplan.ID{
			compiled.Slots[0].ID, compiled.Slots[1].ID, compiled.Slots[2].ID, compiled.Slots[3].ID,
			compiled.Slots[4].ID, compiled.Slots[5].ID, compiled.Slots[6].ID,
		})
		require.Equal(t, []string{"bytes.codec", "named.codec", "bytes.codec", "", "", "named.codec", ""}, []string{
			compiled.Slots[0].Codec, compiled.Slots[1].Codec, compiled.Slots[2].Codec, compiled.Slots[3].Codec,
			compiled.Slots[4].Codec, compiled.Slots[5].Codec, compiled.Slots[6].Codec,
		})
		for i, copier := range compiled.CopyArgs {
			require.NotNil(t, copier, "copyArgs[%d]", i)
		}
		copy, err := compiled.Copy()
		require.NoError(t, err)
		require.Equal(t, "outer", copy.BoundArgs()[1].(sql.NamedArg).Name)
		require.Equal(t, "empty", copy.BoundArgs()[6].(sql.NamedArg).Name)
	})

	t.Run("composition alignment across CTE, compound predicate and order", func(t *testing.T) {
		base, relation := bindQueryRelation(t)
		compound, err := rasql.Combine(base, rasql.UnionAll, base)
		require.NoError(t, err)
		amount, err := rasql.BindColumn[bindFixtureRow, int64](relation, "amount", "")
		require.NoError(t, err)
		predicateQuery := base.Where(rasql.EqualValue(amount.Expr(), int64(2))).Where(rasql.EqualValue(amount.Expr(), int64(3)))
		orderQuery := base.OrderBy(rasql.AscExpr(rasql.Value(int64(3))))
		cte, err := rasql.CTEOf("bound_values", base)
		require.NoError(t, err)
		with, err := rasql.With(base, cte)
		require.NoError(t, err)
		compiler := bindCompiler(t)
		compiledBase, err := rasql.Q1CompileQuery(compiler, base)
		require.NoError(t, err)
		compiledCompound, err := rasql.Q1CompileQuery(compiler, compound)
		require.NoError(t, err)
		compiledPredicate, err := rasql.Q1CompileQuery(compiler, predicateQuery)
		require.NoError(t, err)
		compiledOrder, err := rasql.Q1CompileQuery(compiler, orderQuery)
		require.NoError(t, err)
		compiledWith, err := rasql.Q1CompileQuery(compiler, with)
		require.NoError(t, err)
		count := rasql.CountQuery(compound, true)
		compiledCount, err := rasql.Q1CompileQuery(compiler, count)
		require.NoError(t, err)
		for _, compiled := range []bindplan.Compiled{compiledBase, compiledCompound, compiledPredicate, compiledOrder, compiledWith, compiledCount} {
			require.Equal(t, len(compiled.Statement.BoundArgs()), len(compiled.Slots))
			require.Equal(t, len(compiled.Statement.BoundArgs()), len(compiled.CopyArgs))
			for index, copier := range compiled.CopyArgs {
				require.NotNil(t, copier, "copyArgs[%d]", index)
			}
			statement, copyErr := compiled.Copy()
			require.NoError(t, copyErr)
			require.Equal(t, len(statement.BoundArgs()), len(compiled.Slots))
		}
	})
}

type bindCopyOpaque struct {
	value []byte
	calls *atomic.Int32
}

func (value bindCopyOpaque) SnapshotBind() (bindCopyOpaque, error) {
	value.calls.Add(1)
	return bindCopyOpaque{value: append([]byte(nil), value.value...), calls: value.calls}, nil
}

type bindCopyLegacy struct{ value []byte }

func (bindCopyLegacy) SnapshotBind() (bindCopyLegacy, error) { panic("legacy snapshotter was called") }

type bindFixtureRow struct {
	Category rasql.Nullable[string]
	Amount   int64
}

type bindFixtureDecoder struct{ result rasql.ResultSchema }

func (d bindFixtureDecoder) ResultSchema() rasql.ResultSchema { return d.result }
func (bindFixtureDecoder) Presence() []rasql.Presence         { return nil }
func (bindFixtureDecoder) DecodeRow(source rasql.ScanSource, row *bindFixtureRow) error {
	var category sql.NullString
	if err := source.Scan(&category, &row.Amount); err != nil {
		return err
	}
	row.Category = rasql.Nullable[string]{Value: category.String, Valid: category.Valid}
	return nil
}

// bindQueryRelation returns a two-column query and the relation it reads, so a
// caller can bind another column of the same source.
func bindQueryRelation(t *testing.T) (rasql.Query[bindFixtureRow], rasql.TypedRelation[bindFixtureRow]) {
	t.Helper()
	table, err := rasql.ReadTableOf[bindFixtureRow](schema.TableDef{
		Name: "bind_items",
		Columns: []schema.ColumnDef{
			{Name: "category", Type: schema.TextType{}, Nullable: true},
			{Name: "amount", Type: schema.IntegerType{}},
		},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "i")
	require.NoError(t, err)
	category, err := rasql.BindNullColumn[bindFixtureRow, string](relation, "category", "category.codec")
	require.NoError(t, err)
	amount, err := rasql.BindColumn[bindFixtureRow, int64](relation, "amount", "amount.codec")
	require.NoError(t, err)
	result, err := rasql.NewResultSchema(
		rasql.ResultColumn{Name: "category", Type: schema.TextType{}, Nullable: true, Codec: "category.codec"},
		rasql.ResultColumn{Name: "amount", Type: schema.IntegerType{}, Codec: "amount.codec"},
	)
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{
		rasql.NullItem("category", category.NullExpr(), schema.TextType{}, "category.codec"),
		rasql.Item("amount", amount.Expr(), schema.IntegerType{}, "amount.codec"),
	}, bindFixtureDecoder{result: result})
	require.NoError(t, err)
	return rasql.Select(relation.Source(), projection), relation
}

func bindCompiler(t *testing.T) rasql.Compiler {
	t.Helper()
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	compiler, err := profile.Compiler(dialect.SQLite())
	require.NoError(t, err)
	return compiler
}
