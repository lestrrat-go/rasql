package rasql_test

import (
	"database/sql"
	"errors"
	"testing"
	"time"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type q1ExportedBind struct {
	Label string
	Bytes []byte
}

type q1PrivateBind struct {
	bytes []byte
}

type q1Snapshotter struct {
	Value string
	Calls *int
}

type q1FailingSnapshotter struct{}

func (q1FailingSnapshotter) SnapshotBind() (q1FailingSnapshotter, error) {
	return q1FailingSnapshotter{}, errors.New("snapshot failed")
}

func (s q1Snapshotter) SnapshotBind() (q1Snapshotter, error) {
	*s.Calls++
	return q1Snapshotter{Value: s.Value, Calls: s.Calls}, nil
}

func TestQ1ValueWithCodecSnapshotsSupportedInputs(t *testing.T) {
	now := time.Date(2026, time.September, 7, 12, 0, 0, 0, time.UTC)
	cases := []struct {
		name  string
		value any
	}{
		{name: "nil", value: nil},
		{name: "nil interface slice element", value: []any{nil}},
		{name: "map with nil interface value", value: map[string]any{"missing": nil}},
		{name: "exported struct", value: q1ExportedBind{Label: "before", Bytes: []byte("owned")}},
		{name: "pointer", value: &q1ExportedBind{Label: "before", Bytes: []byte("owned")}},
		{name: "time", value: now},
		{name: "named argument", value: sql.Named("value", []byte("owned"))},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			_, err := rasql.ValueWithCodec(test.value, "snapshot.codec")
			require.NoError(t, err)
		})
	}
}

func TestQ1ValueWithCodecRejectsUnsupportedGraphs(t *testing.T) {
	type cycleNode struct{ Next *cycleNode }
	cycle := &cycleNode{}
	cycle.Next = cycle
	_, err := rasql.ValueWithCodec(cycle, "")
	require.Error(t, err)
	var planErr *rasql.PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)

	_, err = rasql.ValueWithCodec(q1PrivateBind{bytes: []byte("private")}, "")
	require.Error(t, err)
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)

	self := make([]any, 1)
	self[0] = self
	_, err = rasql.ValueWithCodec(self, "")
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)

	_, err = rasql.ValueWithCodec(map[any]string{"key": "value"}, "")
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
	_, err = rasql.ValueWithCodec(map[[1]*int]string{{nil}: "value"}, "")
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
	_, err = rasql.ValueWithCodec(map[struct{ Pointer *int }]string{{}: "value"}, "")
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
	_, err = rasql.ValueWithCodec(map[chan int]string{make(chan int): "value"}, "")
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
}

func TestQ1EqualValueCarriesSnapshotErrorsIntoValidation(t *testing.T) {
	type cycleNode struct{ Next *cycleNode }
	cycle := &cycleNode{}
	cycle.Next = cycle

	table, err := rasql.ReadTableOf[int64](schema.TableDef{Name: "values", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "v")
	require.NoError(t, err)
	projection, err := rasql.Scalar("value", rasql.Value(int64(1)), schema.IntegerType{}, "")
	require.NoError(t, err)
	query := rasql.Select(relation.Source(), projection).Where(rasql.EqualValue(rasql.Value(cycle), cycle))
	err = query.Validate()
	require.Error(t, err)
	var planErr *rasql.PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
}

func TestQ1SnapshotterRunsRecursivelyAndWrapsErrors(t *testing.T) {
	calls := 0
	input := q1Snapshotter{Value: "nested", Calls: &calls}
	_, err := rasql.ValueWithCodec([]q1Snapshotter{input}, "")
	require.NoError(t, err)
	require.Equal(t, 1, calls)

	calls = 0
	arg := sql.Named("value", input)
	_, err = rasql.ValueWithCodec(arg, "")
	require.NoError(t, err)
	require.Equal(t, 1, calls)

	_, err = rasql.ValueWithCodec(q1FailingSnapshotter{}, "")
	require.Error(t, err)
	var planErr *rasql.PlanError
	require.ErrorAs(t, err, &planErr)
	require.Equal(t, "unsnapshotable_bind", planErr.Code)
}

func TestQ1SnapshotterAllowsOverlappingAcyclicSlices(t *testing.T) {
	base := []int{1, 2, 3}
	_, err := rasql.ValueWithCodec([][]int{base[:2], base[1:]}, "")
	require.NoError(t, err)
}

func TestQ1SnapshotterDistinguishesPointerTypesAtSameAddress(t *testing.T) {
	type pointerFirstField struct {
		Value int
		Ref   *int
	}
	value := pointerFirstField{Value: 42}
	value.Ref = &value.Value
	_, err := rasql.ValueWithCodec(value, "")
	require.NoError(t, err)
}

func TestQ1SnapshotterRunsOnceAndBuildsReusableQuery(t *testing.T) {
	calls := 0
	input := q1Snapshotter{Value: "before", Calls: &calls}
	expression, err := rasql.ValueWithCodec(input, "snapshot.codec")
	require.NoError(t, err)
	require.Equal(t, 1, calls)
	input.Value = "after"

	table, err := rasql.ReadTableOf[int64](schema.TableDef{Name: "values", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "v")
	require.NoError(t, err)
	projection, err := rasql.Scalar("value", expression, schema.JSONType{}, "snapshot.codec")
	require.NoError(t, err)
	query := rasql.Select(relation.Source(), projection)
	require.NoError(t, query.Validate())
	require.NoError(t, query.Validate())
	require.Equal(t, 1, calls)
}
