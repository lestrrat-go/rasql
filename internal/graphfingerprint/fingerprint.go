// Package graphfingerprint holds what a graph load's row cache needs: which
// stage invocations may share rows, and how a cached value or an encoded bind
// is copied on the way out. Two invocations share rows only when everything
// that could change the rows they return is the same, so a stage's identity
// covers the SQL, the binds, the result columns, the key columns and the
// decoder.
//
// The package name is older than what it holds. Nothing here digests anything
// any more; renaming it to graphcache is a separate change.
package graphfingerprint

import (
	"bytes"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/graphkey"
	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

// PreencodeBaseOccurrences replaces each bind the base stage already encoded
// with the encoded value, so a cached stage does not encode it a second time.
func PreencodeBaseOccurrences(base bindplan.Compiled, encoded stmt.Statement, final bindplan.Compiled) (bindplan.Compiled, error) {
	baseArgs := base.Statement.Args()
	encodedArgs := encoded.Args()
	finalArgs := final.Statement.Args()
	if base.Statement.SQL() != encoded.SQL() {
		return bindplan.Compiled{}, planerr.New("internal_plan", "binds", "base and prepared SQL differ")
	}
	if len(baseArgs) != len(base.Slots) || len(baseArgs) != len(base.CopyArgs) ||
		len(encodedArgs) != len(base.Slots) || len(finalArgs) != len(final.Slots) ||
		len(finalArgs) != len(final.CopyArgs) {
		return bindplan.Compiled{}, planerr.New("internal_plan", "binds", "statement arguments and bind metadata differ")
	}
	occurrences, err := bindplan.MatchBaseOccurrences(base, final)
	if err != nil {
		return bindplan.Compiled{}, err
	}
	result := final
	result.Slots = append([]bindplan.Slot(nil), final.Slots...)
	result.CopyArgs = append([]bindplan.ValueCopy(nil), final.CopyArgs...)
	args := append([]any(nil), finalArgs...)
	for i, position := range occurrences {
		if position >= len(args) || i >= len(encodedArgs) {
			return bindplan.Compiled{}, planerr.New("internal_plan", "binds", "encoded occurrence is out of range")
		}
		value := encodedArgs[i]
		args[position] = value
		result.Slots[position].PreEncoded = true
		result.CopyArgs[position] = func() (any, error) { return CloneEncoded(value), nil }
	}
	result.Statement = stmt.New(final.Statement.Text(), args...)
	return result, nil
}

// StageCacheable reports whether a compiled stage carries the bind metadata a
// cache key needs.
func StageCacheable(compiled bindplan.Compiled) bool {
	if len(compiled.Slots) != len(compiled.Statement.Args()) || len(compiled.Slots) != len(compiled.CopyArgs) {
		return false
	}
	for _, slot := range compiled.Slots {
		if slot.ID == 0 {
			return false
		}
	}
	return true
}

// CloneEncoded copies an already encoded bind value, so handing it out again
// cannot let a caller change what a later render sends.
func CloneEncoded(value any) any {
	switch value := value.(type) {
	case []byte:
		return append([]byte(nil), value...)
	case sql.NamedArg:
		value.Value = CloneEncoded(value.Value)
		return value
	default:
		return value
	}
}

// cloneValue owns the top-level row or graph value and direct slice fields.
// It deliberately leaves nested pointers, maps, interfaces, and opaque values
// alone because their mapper owns that state.
// CloneValue copies a value taken from the cache, so the caller that receives
// it cannot change what the cache still holds.
func CloneValue(value any) any {
	if value == nil {
		return nil
	}
	original := reflect.ValueOf(value)
	switch original.Kind() {
	case reflect.Pointer:
		if original.IsNil() {
			return value
		}
		clone := reflect.New(original.Type().Elem())
		clone.Elem().Set(original.Elem())
		if clone.Elem().Kind() == reflect.Struct {
			cloneDirectSlices(clone.Elem(), original.Elem())
		}
		return clone.Interface()
	case reflect.Struct:
		clone := reflect.New(original.Type()).Elem()
		clone.Set(original)
		cloneDirectSlices(clone, original)
		return clone.Interface()
	case reflect.Slice:
		return cloneSlice(original).Interface()
	default:
		return value
	}
}

func cloneDirectSlices(destination, source reflect.Value) {
	for index := 0; index < source.NumField(); index++ {
		from, to := source.Field(index), destination.Field(index)
		if !to.CanSet() || !from.CanInterface() {
			continue
		}
		switch from.Kind() {
		case reflect.Slice:
			to.Set(cloneSlice(from))
		case reflect.Pointer:
			if from.IsNil() || from.Type().Elem().Kind() != reflect.Slice {
				continue
			}
			clone := reflect.New(from.Type().Elem())
			clone.Elem().Set(cloneSlice(from.Elem()))
			to.Set(clone)
		}
	}
}

func cloneSlice(value reflect.Value) reflect.Value {
	if value.IsNil() {
		return reflect.Zero(value.Type())
	}
	clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
	reflect.Copy(clone, value)
	return clone
}

// Stage is one compiled step of a graph load, described in the terms its cache
// identity is built from.
type Stage struct {
	Name           string
	Source         string
	Columns        []query.ResultColumn
	Keys           []*graphkey.Spec
	Compiled       bindplan.Compiled
	PerParentLimit int
	BindLimit      int
	// Node is the plan object the stage reads its decoder out of. Two
	// invocations that agree on everything else and name the same Node hold the
	// same decoder value by construction, which is how a decoder carrying a
	// func still shares rows with itself. Node must be comparable; a caller
	// passes a pointer.
	Node any
	// Decoder is the stage's RowDecoder value. One decoder type with two
	// configurations decodes two different rows, so two stages share rows only
	// when their decoders are equal.
	Decoder any
}

// writer frames the pieces of a stage's fixed binds into one string. Each
// piece carries its own length, so two different bind lists can never frame to
// the same bytes.
type writer struct{ bytes.Buffer }

func (w *writer) writeBytes(value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write(value)
}

func (w *writer) writeString(value string) { w.writeBytes([]byte(value)) }
func (w *writer) writeBool(value bool) {
	if value {
		_ = w.WriteByte(1)
		return
	}
	_ = w.WriteByte(0)
}

// stageRecord is everything that decides whether two stage invocations may
// share cached rows. Stages interns one record per distinct invocation and the
// row cache keys its entries on a record's index, which is how a decoder value
// that no map key could carry is still part of a stage's identity.
type stageRecord struct {
	node           any
	name           string
	source         string
	sql            string
	perParentLimit int
	bindLimit      int
	columns        []query.ResultColumn
	keyParts       []keyPartRecord
	binds          string
	decoder        any
}

// keyPartRecord is one column of a stage's key, reduced to the fields that
// decide whether two key parts read the same value. graphkey.PartSpec carries
// an extractor func, so a spec is not comparable; these fields are. key is the
// position of the key the part belongs to, so a stage with one two-column key
// never matches a stage with two one-column keys.
type keyPartRecord struct {
	key        int
	source     string
	column     string
	codec      string
	nullable   bool
	columnType schema.ColumnType
}

// equal reports whether two stage records may share cached rows.
//
// Everything but the decoder is compared by value. schema.ColumnType is closed
// by an unexported method and every one of its ten implementations is a value
// struct of bool and int fields, so == on a query.ResultColumn and on a
// keyPartRecord is well defined and needs no enumeration of the ten types.
//
// The decoder is compared last, and only when it has to be. Two records built
// from the same plan node hold the same decoder value by construction, because
// a node hands back the one RowDecoder its projection holds, so matching on the
// node skips reflect.DeepEqual entirely. That matters because DeepEqual reports
// two non-nil func values as unequal even when they are the same function, so a
// decoder carrying a func, a channel or a touched mutex would otherwise never
// match itself. Across two different nodes DeepEqual decides, where it can only
// err toward "unequal" and so cost a cache miss rather than hand back the wrong
// rows.
func (r stageRecord) equal(other stageRecord) bool {
	if r.name != other.name || r.source != other.source || r.sql != other.sql ||
		r.perParentLimit != other.perParentLimit || r.bindLimit != other.bindLimit ||
		r.binds != other.binds {
		return false
	}
	if len(r.columns) != len(other.columns) || len(r.keyParts) != len(other.keyParts) {
		return false
	}
	for index, column := range r.columns {
		if column != other.columns[index] {
			return false
		}
	}
	for index, part := range r.keyParts {
		if part != other.keyParts[index] {
			return false
		}
	}
	if r.node != nil && r.node == other.node {
		return true
	}
	return reflect.DeepEqual(r.decoder, other.decoder)
}

// Stages interns the stage records of one graph load. The load's row cache keys
// its entries on a record index and a key tuple, so a stage's identity costs
// one int in every entry and is compared once per stage invocation rather than
// once per lookup.
//
// A Stages holds one record per distinct stage invocation, which a plan bounds:
// LoadGraph refuses a cyclic plan, and an invocation happens once per edge per
// depth the walk reaches. Row count never enters it. A Stages is created per
// LoadGraph call and dropped with it.
type Stages struct{ records []stageRecord }

// Index returns the position of the interned record equal to stage, appending a
// new record when none matches. Two stage invocations that are handed the same
// index may share cached rows.
func (s *Stages) Index(stage Stage) (int, error) {
	record, err := recordOf(stage)
	if err != nil {
		return 0, err
	}
	for index := range s.records {
		if s.records[index].equal(record) {
			return index, nil
		}
	}
	s.records = append(s.records, record)
	return len(s.records) - 1, nil
}

func recordOf(stage Stage) (stageRecord, error) {
	args := stage.Compiled.Statement.Args()
	if len(args) != len(stage.Compiled.Slots) {
		return stageRecord{}, planerr.New("internal_plan", "binds", "statement arguments and bind slots differ")
	}
	if len(stage.Keys) == 0 {
		return stageRecord{}, planerr.New("internal_plan", "graph.key", "stage key is empty")
	}
	record := stageRecord{
		node:           stage.Node,
		name:           stage.Name,
		source:         stage.Source,
		sql:            stage.Compiled.Statement.SQL(),
		perParentLimit: stage.PerParentLimit,
		bindLimit:      stage.BindLimit,
		columns:        append([]query.ResultColumn(nil), stage.Columns...),
		decoder:        stage.Decoder,
	}
	for keyIndex, keySpec := range stage.Keys {
		if keySpec == nil || len(keySpec.Parts) == 0 {
			return stageRecord{}, planerr.New("internal_plan", "graph.key", "stage key is empty")
		}
		for _, part := range keySpec.Parts {
			record.keyParts = append(record.keyParts, keyPartRecord{
				key:        keyIndex,
				source:     part.Source,
				column:     part.Column.Name(),
				codec:      part.Codec,
				nullable:   part.Nullable,
				columnType: part.ColumnType,
			})
		}
	}
	binds, err := frameBinds(stage.Compiled, args)
	if err != nil {
		return stageRecord{}, err
	}
	record.binds = binds
	return record, nil
}

// frameBinds renders a stage's fixed binds as one string. A value is
// canonicalised before it is framed, so a driver that hands back an int32 seven
// and one that hands back an int64 seven frame alike, and a negative zero does
// not frame as a positive one.
func frameBinds(compiled bindplan.Compiled, args []any) (string, error) {
	var key writer
	for index, slot := range compiled.Slots {
		key.writeString(slot.Codec)
		key.writeBool(slot.PreEncoded)
		if err := writeValue(&key, args[index], slot.PreEncoded || slot.Codec != ""); err != nil {
			return "", err
		}
	}
	return key.String(), nil
}

func writeValue(key *writer, value any, rejectConversion bool) error {
	named, ok := value.(sql.NamedArg)
	key.writeBool(ok)
	if ok {
		key.writeString(named.Name)
		value = named.Value
	}
	var normalized driver.Value
	var err error
	if rejectConversion {
		if err = graphkey.ValidateDriverValue(value); err == nil {
			normalized = driver.Value(value)
		}
	} else {
		normalized, err = graphkey.Normalize(value)
	}
	if err != nil {
		return planerr.New("internal_plan", "binds", err.Error())
	}
	frame, err := graphkey.FrameAllowingNaN(normalized)
	if err != nil {
		return planerr.New("internal_plan", "binds", err.Error())
	}
	key.writeBytes(frame)
	return nil
}
