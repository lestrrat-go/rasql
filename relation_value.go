package rasql

import (
	"database/sql/driver"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/graphkey"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// The key machinery lives in internal/graphkey alongside the value framing it
// uses. These names stay so the rest of this package reads as before.
type graphKeyPartSpec = graphkey.PartSpec
type graphKeySpec = graphkey.Spec
type keyComponent = graphkey.Component
type keyTuple = graphkey.Tuple

type GraphKeyPart[R any] struct{ part *graphKeyPartSpec }
type GraphKey[R any] struct{ key *graphKeySpec }

func KeyPart[R, T comparable](column Column[R, T], extract func(R) T) GraphKeyPart[R] {
	if extract == nil || column.ref.Name() == "" {
		return GraphKeyPart[R]{}
	}
	return GraphKeyPart[R]{part: &graphKeyPartSpec{
		Column: column.ref, Codec: column.codec, Type: reflect.TypeOf((*T)(nil)).Elem(),
		ColumnType: graphColumnType(column.ref), Source: column.ref.Source().QualifiedName(),
		Extract: func(row any) (any, bool) { return extract(row.(R)), true },
	}}
}

func NullKeyPart[R, T comparable](column NullColumn[R, T], extract func(R) Nullable[T]) GraphKeyPart[R] {
	if extract == nil || column.ref.Name() == "" {
		return GraphKeyPart[R]{}
	}
	return GraphKeyPart[R]{part: &graphKeyPartSpec{
		Column: column.ref, Codec: column.codec, Type: reflect.TypeOf((*T)(nil)).Elem(),
		Nullable: true, ColumnType: graphColumnType(column.ref), Source: column.ref.Source().QualifiedName(),
		Extract: func(row any) (any, bool) {
			value := extract(row.(R))
			return value.Value, value.Valid
		},
	}}
}

func graphColumnType(column query.ColumnRef) schema.ColumnType { return graphkey.ColumnType(column) }

func NewGraphKey[R any](parts ...GraphKeyPart[R]) (GraphKey[R], error) {
	result := make([]*graphKeyPartSpec, len(parts))
	for i, part := range parts {
		result[i] = part.part
	}
	if err := graphkey.ValidateParts(result); err != nil {
		return GraphKey[R]{}, err
	}
	return GraphKey[R]{key: &graphKeySpec{Parts: result}}, nil
}

func normalizeGraphValue(value any) (driver.Value, error) { return graphkey.Normalize(value) }

func frameGraphValue(value driver.Value) ([]byte, error) { return graphkey.Frame(value) }

// graphKeyEncoder adapts the root's codec registry to what graphkey asks for.
type graphKeyEncoder struct{ codecs CodecRegistry }

func (e graphKeyEncoder) EncodeGraphKey(codec string, value any) (driver.Value, error) {
	found, ok := e.codecs.Lookup(CodecID(codec))
	if !ok {
		return nil, planError("codec_unavailable", "graph.key", codec)
	}
	return found.Encode(value)
}
