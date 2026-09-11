package graphkey

import (
	"bytes"
	"database/sql/driver"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
)

// Encoder turns a value into what a driver carries, for one named codec. The
// root package holds the codec registry, so it passes one of these in rather
// than this package reaching back for it.
type Encoder interface {
	EncodeGraphKey(codec string, value any) (driver.Value, error)
}

// PartSpec describes one column of a graph key, and how to read that column's
// value out of a row.
type PartSpec struct {
	Column     query.ColumnRef
	Codec      string
	Type       reflect.Type
	Nullable   bool
	ColumnType schema.ColumnType
	Source     string
	Extract    func(any) (any, bool)
}

// Spec is a whole graph key, one or more columns of a single relation.
type Spec struct{ Parts []*PartSpec }

// Component is one column's contribution to a key tuple.
type Component struct {
	Value   driver.Value
	Encoded []byte
	Codec   string
}

// Tuple is a key read out of one row. Identity is the bytes two rows compare
// on, so rows of the same parent share it exactly.
type Tuple struct {
	Components []Component
	Identity   string
}

// ColumnType reports the declared type of column, and nil when the column's
// relation does not describe it.
func ColumnType(column query.ColumnRef) schema.ColumnType {
	for _, value := range column.Source().Columns() {
		if value.Name == column.Name() {
			return value.Type
		}
	}
	return nil
}

// ValidateParts reports a key whose parts cannot stand together, because one
// is unusable on its own, because they read different relations, or because
// two of them name the same column.
func ValidateParts(parts []*PartSpec) error {
	if len(parts) == 0 {
		return planerr.New("invalid_graph_key", "parts", "must not be empty")
	}
	source := ""
	for i, part := range parts {
		if part == nil || part.Extract == nil {
			return planerr.New("invalid_graph_key", fmt.Sprintf("parts[%d]", i), "must not be zero")
		}
		if err := part.Column.Validate(); err != nil {
			return planerr.New("invalid_graph_key", fmt.Sprintf("parts[%d]", i), err.Error())
		}
		if part.Codec != "" && !bindplan.CodecPattern.MatchString(part.Codec) {
			return planerr.New("invalid_graph_key", fmt.Sprintf("parts[%d]", i), "malformed codec")
		}
		current := part.Column.Source().QualifiedName()
		if source == "" {
			source = current
		} else if source != current {
			return planerr.New("invalid_graph_key", "parts", "mixed relation sources")
		}
		for _, prior := range parts[:i] {
			if prior != nil && prior.Column.Name() == part.Column.Name() {
				return planerr.New("invalid_graph_key", "parts", "duplicate column")
			}
		}
	}
	return nil
}

// Tuple reads the key out of row. It reports false when a nullable part of the
// key is absent, because a row with no key joins to nothing.
func (s *Spec) Tuple(row any, encoder Encoder) (Tuple, bool, error) {
	if s == nil || len(s.Parts) == 0 {
		return Tuple{}, false, planerr.New("invalid_graph_key", "key", "must not be zero")
	}
	result := Tuple{Components: make([]Component, len(s.Parts))}
	var identity bytes.Buffer
	identity.WriteByte(byte(len(s.Parts)))
	for i, part := range s.Parts {
		value, present := part.Extract(row)
		if !present {
			return Tuple{}, false, nil
		}
		driverValue, err := Normalize(value)
		if err != nil {
			return Tuple{}, false, err
		}
		if part.Codec != "" {
			driverValue, err = encoder.EncodeGraphKey(part.Codec, value)
			if err != nil {
				return Tuple{}, false, err
			}
		}
		frame, err := Frame(driverValue)
		if err != nil {
			return Tuple{}, false, err
		}
		result.Components[i] = Component{Value: driverValue, Encoded: frame, Codec: part.Codec}
		identity.Write(frame)
	}
	result.Identity = identity.String()
	return result, true, nil
}
