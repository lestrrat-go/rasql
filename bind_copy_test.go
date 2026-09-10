package rasql

import (
	"database/sql"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/lestrrat-go/rasql/query"
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

func tokenOf[T any](expression Expr[T]) bindToken {
	value := expression.node.(query.Value).Argument()
	return value.(bindToken)
}

func TestBindCopyDetachesNestedValues(t *testing.T) {
	number := 7
	input := bindCopyShape{
		Array: [2]int{1, 2}, Ptr: &number, Empty: []byte{}, Map: map[string][]byte{"x": {3}},
		Any: []byte{4}, Named: sql.Named("inner", []byte{5}),
	}
	expression := Value(input)
	token := tokenOf(expression)
	input.Array[0], number, input.Map["x"][0] = 99, 88, 77
	input.Any.([]byte)[0], input.Named.Value.([]byte)[0] = 66, 55
	compiled, err := unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?"), token))
	require.NoError(t, err)
	first, err := compiled.statementCopy()
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
	second, err := compiled.statementCopy()
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
	compiledAgain, err := unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?"), token))
	require.NoError(t, err)
	third, err := compiledAgain.statementCopy()
	require.NoError(t, err)
	require.Equal(t, secondShape, third.BoundArgs()[0])
}

func TestBindCopyRepeatedOccurrenceUsesIndependentCopies(t *testing.T) {
	expression := Value([]byte("x"))
	token := tokenOf(expression)
	compiled, err := unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?, ?"), token, token))
	require.NoError(t, err)
	require.Equal(t, compiled.bindSlots[0], compiled.bindSlots[1])
	statement, err := compiled.statementCopy()
	require.NoError(t, err)
	args := statement.BoundArgs()
	args[0].([]byte)[0] = 'z'
	require.Equal(t, []byte("x"), args[1])
	other := Value([]byte("x"))
	require.NotEqual(t, tokenOf(expression).id, tokenOf(other).id)
}

type bindCopyOpaque struct {
	value []byte
	calls *atomic.Int32
}

func (value bindCopyOpaque) SnapshotBind() (bindCopyOpaque, error) {
	value.calls.Add(1)
	return bindCopyOpaque{value: append([]byte(nil), value.value...), calls: value.calls}, nil
}

func TestBindCopySnapshotterRunsOnce(t *testing.T) {
	calls := &atomic.Int32{}
	expression := Value(struct{ Value bindCopyOpaque }{Value: bindCopyOpaque{value: []byte("x"), calls: calls}})
	token := tokenOf(expression)
	compiled, err := unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?"), token))
	require.NoError(t, err)
	for i := 0; i < 3; i++ {
		_, err = compiled.statementCopy()
		require.NoError(t, err)
	}
	require.Equal(t, int32(1), calls.Load())
}

func TestBindCopyNestedOpaqueSnapshottersRunOncePerLogicalValue(t *testing.T) {
	calls := &atomic.Int32{}
	values := []any{
		struct{ Value bindCopyOpaque }{Value: bindCopyOpaque{value: []byte("struct"), calls: calls}},
		[]bindCopyOpaque{{value: []byte("slice"), calls: calls}},
		map[string]bindCopyOpaque{"value": {value: []byte("map"), calls: calls}},
		sql.Named("named", bindCopyOpaque{value: []byte("named"), calls: calls}),
	}
	for index, value := range values {
		expression := Value(value)
		token := expression.node.(query.Value).Argument().(bindToken)
		compiled, err := unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?"), token))
		require.NoError(t, err)
		for i := 0; i < 3; i++ {
			statement, copyErr := compiled.statementCopy()
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
}

type bindCopyLegacy struct{ value []byte }

func (bindCopyLegacy) SnapshotBind() (bindCopyLegacy, error) { panic("legacy snapshotter was called") }

func TestBindCopyLegacyAndNamedArguments(t *testing.T) {
	legacy, err := unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?, ?"), []byte("x"), sql.Named("name", []byte("y"))))
	require.NoError(t, err)
	first, err := legacy.statementCopy()
	require.NoError(t, err)
	first.BoundArgs()[0].([]byte)[0] = 'z'
	second, err := legacy.statementCopy()
	require.NoError(t, err)
	require.Equal(t, []byte("x"), second.BoundArgs()[0])
	require.Equal(t, "name", second.BoundArgs()[1].(sql.NamedArg).Name)
	nilBytes := []byte(nil)
	legacyGraph, err := unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?, ?"), nilBytes, struct {
		Array [2][]byte
		Map   map[string][]byte
	}{Array: [2][]byte{{1}, nil}, Map: map[string][]byte{"x": {2}}}))
	require.NoError(t, err)
	legacyFirst, err := legacyGraph.statementCopy()
	require.NoError(t, err)
	legacyValue := legacyFirst.BoundArgs()[1].(struct {
		Array [2][]byte
		Map   map[string][]byte
	})
	legacyValue.Array[0][0] = 9
	legacyValue.Map["x"][0] = 8
	legacySecond, err := legacyGraph.statementCopy()
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
	_, err = unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?"), bindCopyLegacy{value: []byte("x")}))
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
}

func TestBindCopyNamedTokenShapesAndAlignment(t *testing.T) {
	directExpression, err := ValueWithCodec(sql.Named("inner", []byte("value")), "bytes.codec")
	require.NoError(t, err)
	directToken := tokenOf(directExpression)
	value, copier, err := adoptBind([]byte("outer"), true)
	require.NoError(t, err)
	syntheticToken := bindToken{id: 91, codec: "text.codec", value: value, copy: copier}
	compiled, err := unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?, ?"), directToken, sql.Named("outer", syntheticToken)))
	require.NoError(t, err)
	require.Equal(t, []bindSlot{{id: directToken.id, codec: "bytes.codec"}, {id: 91, codec: "text.codec"}}, compiled.bindSlots)
	statement, err := compiled.statementCopy()
	require.NoError(t, err)
	args := statement.BoundArgs()
	require.Equal(t, "inner", args[0].(sql.NamedArg).Name)
	require.Equal(t, []byte("value"), args[0].(sql.NamedArg).Value)
	require.Equal(t, "outer", args[1].(sql.NamedArg).Name)
	require.Equal(t, []byte("outer"), args[1].(sql.NamedArg).Value)
	_, nested := args[1].(sql.NamedArg).Value.(bindToken)
	require.False(t, nested)
}

func TestBindCopyAtomicErrorsAndAlignment(t *testing.T) {
	sentinel := errors.New("copy failed")
	laterCalled := false
	compiled := compiledQuery{
		statement: stmt.New(sqltext.Text("SELECT ?, ?"), 1, 2),
		bindSlots: []bindSlot{{}, {}},
		copyArgs: []bindValueCopy{
			func() (any, error) { return nil, sentinel },
			func() (any, error) { laterCalled = true; return 2, nil },
		},
	}
	statement, err := compiled.statementCopy()
	var planErr *PlanError
	require.ErrorIs(t, err, sentinel)
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
	require.Equal(t, "args[0]", planErr.Path)
	require.Equal(t, stmt.Statement{}, statement)
	require.False(t, laterCalled)
	for _, invalid := range []compiledQuery{
		{statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: []bindSlot{{}}, copyArgs: []bindValueCopy{nil}},
		{statement: stmt.New(sqltext.Text("SELECT ?"), 1), bindSlots: nil, copyArgs: []bindValueCopy{func() (any, error) { return 1, nil }}},
	} {
		statement, err := invalid.statementCopy()
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, stmt.Statement{}, statement)
	}
}

func TestBindCopyRenderedOrderAcrossCompositionShapes(t *testing.T) {
	firstExpression, err := ValueWithCodec([]byte("first"), "bytes.codec")
	require.NoError(t, err)
	secondExpression, err := ValueWithCodec(sql.Named("named", []byte("second")), "named.codec")
	require.NoError(t, err)
	firstToken, secondToken := tokenOf(firstExpression), tokenOf(secondExpression)
	legacy := struct {
		Values []byte
	}{Values: []byte("legacy")}
	statement := stmt.New(sqltext.Text("SELECT ?, ?, ?, ?, ?, ?, ?"),
		firstToken, sql.Named("outer", secondToken), firstToken, nil, legacy, secondToken, sql.NamedArg{Name: "empty"},
	)
	compiled, err := unwrapBindTokens(statement)
	require.NoError(t, err)
	require.Len(t, compiled.bindSlots, 7)
	require.Equal(t, []bindID{firstToken.id, secondToken.id, firstToken.id, 0, 0, secondToken.id, 0}, []bindID{
		compiled.bindSlots[0].id, compiled.bindSlots[1].id, compiled.bindSlots[2].id, compiled.bindSlots[3].id,
		compiled.bindSlots[4].id, compiled.bindSlots[5].id, compiled.bindSlots[6].id,
	})
	require.Equal(t, []string{"bytes.codec", "named.codec", "bytes.codec", "", "", "named.codec", ""}, []string{
		compiled.bindSlots[0].codec, compiled.bindSlots[1].codec, compiled.bindSlots[2].codec, compiled.bindSlots[3].codec,
		compiled.bindSlots[4].codec, compiled.bindSlots[5].codec, compiled.bindSlots[6].codec,
	})
	for i, copier := range compiled.copyArgs {
		require.NotNil(t, copier, "copyArgs[%d]", i)
	}
	copy, err := compiled.statementCopy()
	require.NoError(t, err)
	require.Equal(t, "outer", copy.BoundArgs()[1].(sql.NamedArg).Name)
	require.Equal(t, "empty", copy.BoundArgs()[6].(sql.NamedArg).Name)
}

func TestBindCopyCompositionAlignmentAcrossCTECompoundPredicateAndOrder(t *testing.T) {
	base := q2AcceptanceQuery(t)
	compound, err := Combine(base, UnionAll, base)
	require.NoError(t, err)
	relation := TypedRelation[q2AcceptanceRow]{source: base.plan.sources[0]}
	amount, err := BindColumn[q2AcceptanceRow, int64](relation, "amount", "")
	require.NoError(t, err)
	predicateQuery := base.Where(EqualValue(amount.Expr(), int64(2))).Where(EqualValue(amount.Expr(), int64(3)))
	orderQuery := base.OrderBy(AscExpr(Value(int64(3))))
	cte, err := CTEOf("bound_values", base)
	require.NoError(t, err)
	with, err := With(base, cte)
	require.NoError(t, err)
	compiler := q2AcceptanceCompiler(t)
	compiledBase, err := compileQuery(compiler, base)
	require.NoError(t, err)
	compiledCompound, err := compileQuery(compiler, compound)
	require.NoError(t, err)
	compiledPredicate, err := compileQuery(compiler, predicateQuery)
	require.NoError(t, err)
	compiledOrder, err := compileQuery(compiler, orderQuery)
	require.NoError(t, err)
	compiledWith, err := compileQuery(compiler, with)
	require.NoError(t, err)
	count := CountQuery(compound, true)
	compiledCount, err := compileQuery(compiler, count)
	require.NoError(t, err)
	for _, compiled := range []compiledQuery{compiledBase, compiledCompound, compiledPredicate, compiledOrder, compiledWith, compiledCount} {
		require.Equal(t, len(compiled.statement.BoundArgs()), len(compiled.bindSlots))
		require.Equal(t, len(compiled.statement.BoundArgs()), len(compiled.copyArgs))
		for index, copier := range compiled.copyArgs {
			require.NotNil(t, copier, "copyArgs[%d]", index)
		}
		statement, copyErr := compiled.statementCopy()
		require.NoError(t, copyErr)
		require.Equal(t, len(statement.BoundArgs()), len(compiled.bindSlots))
	}
}
