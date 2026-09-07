package rasql

import (
	"database/sql"
	"reflect"
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
	second, err := compiled.statementCopy()
	require.NoError(t, err)
	require.Equal(t, []byte{3}, second.BoundArgs()[0].(bindCopyShape).Map["x"])
	secondShape := second.BoundArgs()[0].(bindCopyShape)
	require.True(t, reflect.DeepEqual(got.Empty, []byte{}))
	require.Nil(t, secondShape.Nil)
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
	_, err = unwrapBindTokens(stmt.New(sqltext.Text("SELECT ?"), bindCopyLegacy{value: []byte("x")}))
	var planErr *PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
}
