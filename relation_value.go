package rasql

import (
	"bytes"
	"database/sql/driver"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

type graphKeyPartSpec struct {
	column     query.ColumnRef
	codec      string
	typ        reflect.Type
	nullable   bool
	columnType schema.ColumnType
	source     string
	extract    func(any) (any, bool)
}

type graphKeySpec struct{ parts []*graphKeyPartSpec }

type GraphKeyPart[R any] struct{ part *graphKeyPartSpec }
type GraphKey[R any] struct{ key *graphKeySpec }

func KeyPart[R, T comparable](column Column[R, T], extract func(R) T) GraphKeyPart[R] {
	if extract == nil || column.ref.Name() == "" {
		return GraphKeyPart[R]{}
	}
	return GraphKeyPart[R]{part: &graphKeyPartSpec{column: column.ref, codec: column.codec, typ: reflect.TypeOf((*T)(nil)).Elem(), extract: func(row any) (any, bool) { return extract(row.(R)), true }, columnType: graphColumnType(column.ref), source: column.ref.Source().QualifiedName()}}
}

func NullKeyPart[R, T comparable](column NullColumn[R, T], extract func(R) Nullable[T]) GraphKeyPart[R] {
	if extract == nil || column.ref.Name() == "" {
		return GraphKeyPart[R]{}
	}
	return GraphKeyPart[R]{part: &graphKeyPartSpec{column: column.ref, codec: column.codec, typ: reflect.TypeOf((*T)(nil)).Elem(), nullable: true, columnType: graphColumnType(column.ref), source: column.ref.Source().QualifiedName(), extract: func(row any) (any, bool) {
		value := extract(row.(R))
		return value.Value, value.Valid
	}}}
}

func graphColumnType(column query.ColumnRef) schema.ColumnType {
	for _, value := range column.Source().Columns() {
		if value.Name == column.Name() {
			return value.Type
		}
	}
	return nil
}

func NewGraphKey[R any](parts ...GraphKeyPart[R]) (GraphKey[R], error) {
	if len(parts) == 0 {
		return GraphKey[R]{}, planError("invalid_graph_key", "parts", "must not be empty")
	}
	result := make([]*graphKeyPartSpec, len(parts))
	source := ""
	for i, part := range parts {
		if part.part == nil || part.part.extract == nil {
			return GraphKey[R]{}, planError("invalid_graph_key", fmt.Sprintf("parts[%d]", i), "must not be zero")
		}
		if err := part.part.column.Validate(); err != nil {
			return GraphKey[R]{}, planError("invalid_graph_key", fmt.Sprintf("parts[%d]", i), err.Error())
		}
		if part.part.codec != "" && !codecPattern.MatchString(part.part.codec) {
			return GraphKey[R]{}, planError("invalid_graph_key", fmt.Sprintf("parts[%d]", i), "malformed codec")
		}
		current := part.part.column.Source().QualifiedName()
		if source == "" {
			source = current
		} else if source != current {
			return GraphKey[R]{}, planError("invalid_graph_key", "parts", "mixed relation sources")
		}
		for _, prior := range result[:i] {
			if prior != nil && prior.column.Name() == part.part.column.Name() {
				return GraphKey[R]{}, planError("invalid_graph_key", "parts", "duplicate column")
			}
		}
		result[i] = part.part
	}
	return GraphKey[R]{key: &graphKeySpec{parts: result}}, nil
}

type keyComponent struct {
	value   driver.Value
	encoded []byte
	codec   string
}
type keyTuple struct {
	components []keyComponent
	identity   string
}

func normalizeGraphValue(value any) (driver.Value, error) {
	if value == nil {
		return nil, nil
	}
	converted, err := driver.DefaultParameterConverter.ConvertValue(value)
	if err != nil {
		return nil, err
	}
	if err := validateDriverValue(converted); err != nil {
		return nil, err
	}
	return converted, nil
}

func frameGraphValue(value driver.Value) ([]byte, error) {
	var tag byte
	var payload []byte
	switch v := value.(type) {
	case nil:
		tag = 0
	case int64:
		tag = 1
		payload = make([]byte, 8)
		binary.BigEndian.PutUint64(payload, uint64(v))
	case float64:
		if math.IsNaN(v) {
			return nil, fmt.Errorf("NaN is not a graph key")
		}
		tag = 2
		payload = make([]byte, 8)
		binary.BigEndian.PutUint64(payload, math.Float64bits(v))
	case bool:
		tag = 3
		if v {
			payload = []byte{1}
		} else {
			payload = []byte{0}
		}
	case []byte:
		tag = 4
		payload = append([]byte(nil), v...)
	case string:
		tag = 5
		payload = []byte(v)
	case time.Time:
		tag = 6
		payload = []byte(v.UTC().Format(time.RFC3339Nano))
	default:
		return nil, fmt.Errorf("unsupported graph key value %T", value)
	}
	result := make([]byte, 9+len(payload))
	result[0] = tag
	binary.BigEndian.PutUint64(result[1:], uint64(len(payload)))
	copy(result[9:], payload)
	return result, nil
}

func (k *graphKeySpec) tuple(row any, codecs CodecRegistry) (keyTuple, bool, error) {
	if k == nil || len(k.parts) == 0 {
		return keyTuple{}, false, planError("invalid_graph_key", "key", "must not be zero")
	}
	result := keyTuple{components: make([]keyComponent, len(k.parts))}
	var identity bytes.Buffer
	identity.WriteByte(byte(len(k.parts)))
	for i, part := range k.parts {
		value, present := part.extract(row)
		if !present {
			return keyTuple{}, false, nil
		}
		driverValue, err := normalizeGraphValue(value)
		if err != nil {
			return keyTuple{}, false, err
		}
		if part.codec != "" {
			codec, ok := codecs.Lookup(CodecID(part.codec))
			if !ok {
				return keyTuple{}, false, planError("codec_unavailable", "graph.key", part.codec)
			}
			driverValue, err = codec.Encode(value)
			if err != nil {
				return keyTuple{}, false, err
			}
		}
		frame, err := frameGraphValue(driverValue)
		if err != nil {
			return keyTuple{}, false, err
		}
		result.components[i] = keyComponent{value: driverValue, encoded: frame, codec: part.codec}
		identity.Write(frame)
	}
	result.identity = identity.String()
	return result, true, nil
}

func graphKeyPredicate(key *graphKeySpec, tuple keyTuple) (Predicate, error) {
	if key == nil || len(key.parts) != len(tuple.components) {
		return Predicate{}, planError("internal_plan", "graph.key", "tuple width mismatch")
	}
	branches := make([]query.Expression, len(key.parts))
	_ = branches
	parts := make([]Predicate, len(key.parts))
	for i, part := range key.parts {
		expression, err := graphEncodedBind(tuple.components[i].value, part.codec)
		if err != nil {
			return Predicate{}, err
		}
		parts[i] = Predicate{node: query.Equal(part.column, expression)}
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return And(parts...), nil
}
