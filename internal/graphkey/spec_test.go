package graphkey_test

import (
	"database/sql/driver"
	"errors"
	"reflect"
	"testing"

	"github.com/lestrrat-go/rasql/internal/graphkey"
	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

var errSpecCodec = errors.New("codec refused the value")

// upperEncoder stands in for a registry. It answers one codec name and
// refuses every other, which is how a missing codec is reported.
type upperEncoder struct{ known string }

func (e upperEncoder) EncodeGraphKey(codec string, value any) (driver.Value, error) {
	if codec != e.known {
		return nil, errSpecCodec
	}
	return "encoded:" + value.(string), nil
}

func specPart(column query.ColumnRef, codec string, value any, present bool) *graphkey.PartSpec {
	return &graphkey.PartSpec{
		Column:  column,
		Codec:   codec,
		Type:    reflect.TypeOf(value),
		Source:  column.Source().QualifiedName(),
		Extract: func(any) (any, bool) { return value, present },
	}
}

func specColumn(t *testing.T, table, name string) query.ColumnRef {
	t.Helper()
	ref := query.MustTableRef(schema.TableDef{
		Name: table,
		Columns: []schema.ColumnDef{
			{Name: "id", Type: schema.IntegerType{}},
			{Name: "other", Type: schema.IntegerType{}},
		},
	})
	return query.Relation(ref).Column(name)
}

func TestSpecTuple(t *testing.T) {
	t.Run("reads a key and gives equal rows one identity", func(t *testing.T) {
		column := specColumn(t, "rows", "id")
		spec := &graphkey.Spec{Parts: []*graphkey.PartSpec{specPart(column, "", int64(7), true)}}

		first, present, err := spec.Tuple(struct{}{}, upperEncoder{})
		require.NoError(t, err)
		require.True(t, present)
		second, present, err := spec.Tuple(struct{}{}, upperEncoder{})
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, first.Identity, second.Identity)
		require.Len(t, first.Components, 1)
		require.Equal(t, int64(7), first.Components[0].Value)
	})

	// A key column that is absent means the row joins to nothing, which is not
	// an error, so the caller is told there is no tuple rather than given one.
	t.Run("reports an absent key without an error", func(t *testing.T) {
		column := specColumn(t, "rows", "id")
		spec := &graphkey.Spec{Parts: []*graphkey.PartSpec{specPart(column, "", int64(7), false)}}

		_, present, err := spec.Tuple(struct{}{}, upperEncoder{})
		require.NoError(t, err)
		require.False(t, present)
	})

	t.Run("sends a codec column through the encoder", func(t *testing.T) {
		column := specColumn(t, "rows", "id")
		spec := &graphkey.Spec{Parts: []*graphkey.PartSpec{specPart(column, "upper", "value", true)}}

		tuple, present, err := spec.Tuple(struct{}{}, upperEncoder{known: "upper"})
		require.NoError(t, err)
		require.True(t, present)
		require.Equal(t, "encoded:value", tuple.Components[0].Value)
		require.Equal(t, "upper", tuple.Components[0].Codec)
	})

	t.Run("reports what the encoder refused", func(t *testing.T) {
		column := specColumn(t, "rows", "id")
		spec := &graphkey.Spec{Parts: []*graphkey.PartSpec{specPart(column, "missing", "value", true)}}

		_, _, err := spec.Tuple(struct{}{}, upperEncoder{known: "upper"})
		require.ErrorIs(t, err, errSpecCodec)
	})

	t.Run("rejects a key with no parts", func(t *testing.T) {
		var zero *graphkey.Spec
		_, _, err := zero.Tuple(struct{}{}, upperEncoder{})
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "invalid_graph_key", planErr.Code)

		_, _, err = (&graphkey.Spec{}).Tuple(struct{}{}, upperEncoder{})
		require.ErrorAs(t, err, &planErr)
	})
}

func TestValidateParts(t *testing.T) {
	column := func(table, name string) query.ColumnRef { return specColumn(t, table, name) }

	t.Run("accepts one relation's distinct columns", func(t *testing.T) {
		require.NoError(t, graphkey.ValidateParts([]*graphkey.PartSpec{
			specPart(column("rows", "id"), "", int64(1), true),
			specPart(column("rows", "other"), "", int64(2), true),
		}))
	})

	for name, parts := range map[string][]*graphkey.PartSpec{
		"no parts":    nil,
		"a zero part": {nil},
		"a duplicate column": {
			specPart(column("rows", "id"), "", int64(1), true),
			specPart(column("rows", "id"), "", int64(2), true),
		},
		"two relations": {
			specPart(column("rows", "id"), "", int64(1), true),
			specPart(column("others", "id"), "", int64(2), true),
		},
		"a malformed codec": {specPart(column("rows", "id"), "not a codec", int64(1), true)},
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			err := graphkey.ValidateParts(parts)
			var planErr *planerr.Error
			require.ErrorAs(t, err, &planErr)
			require.Equal(t, "invalid_graph_key", planErr.Code)
		})
	}
}

func TestColumnType(t *testing.T) {
	require.Equal(t, schema.IntegerType{}, graphkey.ColumnType(specColumn(t, "rows", "id")))
	require.Nil(t, graphkey.ColumnType(query.ColumnRef{}))
}
