// Package graphfingerprint builds the key a compiled graph stage is cached
// under. Two stages share a key only when everything that could change the
// rows they return is the same, so the key covers the SQL, the binds, the
// result columns, the key columns and the engine profile.
package graphfingerprint

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
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

// Profile is what the cache key records about the engine a stage compiled
// for. Two stages that differ only by engine must not share a key.
type Profile struct {
	Dialect      string
	ID           string
	Engine       engineprofile.EngineID
	CustomName   string
	Version      engineprofile.Version
	Limits       engineprofile.Limits
	Capabilities engineprofile.Capabilities
	MaxBind      int
}

// Fingerprint identifies one compiled stage under one engine profile.
type Fingerprint struct {
	Stage  string
	Digest [sha256.Size]byte
}

// Stage is one compiled step of a graph load, described in the terms the cache
// key is built from.
type Stage struct {
	Name           string
	Source         string
	Columns        []query.ResultColumn
	Keys           []*graphkey.Spec
	Compiled       bindplan.Compiled
	PerParentLimit int
	BindLimit      int
}

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
func (w *writer) writeU8(value uint8) { _ = w.WriteByte(value) }
func (w *writer) writeU16(value uint16) {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	_, _ = w.Write(encoded[:])
}
func (w *writer) writeU64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = w.Write(encoded[:])
}
func (w *writer) writeInt(value int) { w.writeU64(uint64(int64(value))) }

// Invocation builds the key a graph stage is cached under. Any difference that
// would change the SQL, the binds, the result columns, the key columns or the
// engine's own limits gives a different key.
func Invocation(stage Stage, profile Profile) (Fingerprint, error) {
	args := stage.Compiled.Statement.Args()
	if len(args) != len(stage.Compiled.Slots) {
		return Fingerprint{}, planerr.New("internal_plan", "binds", "statement arguments and bind slots differ")
	}
	if len(stage.Keys) == 0 {
		return Fingerprint{}, planerr.New("internal_plan", "graph.key", "stage key is empty")
	}

	var key writer
	_, _ = key.Write([]byte("rasql.graph.cache\x01"))
	key.writeString(profile.Dialect)
	key.writeString(profile.ID)
	key.writeU8(uint8(profile.Engine))
	key.writeString(profile.CustomName)
	key.writeBool(profile.Version.Known)
	key.writeU16(profile.Version.Major)
	key.writeU16(profile.Version.Minor)
	key.writeU16(profile.Version.Patch)
	key.writeInt(profile.Limits.MaxBindParameters)
	capabilities := profile.Capabilities
	key.writeU8(uint8(capabilities.Returning))
	key.writeU8(uint8(capabilities.Upsert))
	for _, value := range []bool{
		capabilities.ConflictTarget, capabilities.DefaultValues, capabilities.EmptyInsert, capabilities.DefaultValuesUpsert,
		capabilities.SubqueryLimit, capabilities.WriteSubqueryTarget, capabilities.PartialIndex, capabilities.AggregateFilter,
		capabilities.QualifiedReference, capabilities.QualifiedIndexTarget, capabilities.QualifiedIndexName, capabilities.MatchOperator,
		capabilities.SelectForUpdate, capabilities.SelectForShare, capabilities.SelectLockOf, capabilities.SelectLockNoWait,
		capabilities.SelectLockSkipLocked, capabilities.UpsertConflictWhere, capabilities.UpsertUpdateWhere,
		capabilities.WindowFunctions, capabilities.LateralJoins, capabilities.Savepoints, capabilities.TransactionalDDL,
		capabilities.ExplicitNullOrdering, capabilities.TupleComparison,
	} {
		key.writeBool(value)
	}
	key.writeU8(uint8(capabilities.PerParentLimit))
	key.writeU8(uint8(capabilities.UpdateDefault))
	key.writeString(stage.Name)
	key.writeString(stage.Source)
	key.writeString(stage.Compiled.Statement.SQL())
	key.writeInt(stage.PerParentLimit)
	key.writeInt(stage.BindLimit)
	columns := stage.Columns
	key.writeU64(uint64(len(columns)))
	for _, column := range columns {
		key.writeString(column.Name)
		if err := writeColumnType(&key, column.Type); err != nil {
			return Fingerprint{}, err
		}
		key.writeBool(column.Nullable)
		key.writeString(column.Codec)
	}
	key.writeU64(uint64(len(stage.Keys)))
	for _, keySpec := range stage.Keys {
		if keySpec == nil || len(keySpec.Parts) == 0 {
			return Fingerprint{}, planerr.New("internal_plan", "graph.key", "stage key is empty")
		}
		key.writeU64(uint64(len(keySpec.Parts)))
		for _, part := range keySpec.Parts {
			key.writeString(part.Source)
			key.writeString(part.Column.Name())
			if err := writeColumnType(&key, part.ColumnType); err != nil {
				return Fingerprint{}, err
			}
			key.writeBool(part.Nullable)
			writeGoType(&key, part.Type)
			key.writeString(part.Codec)
		}
	}
	for index, slot := range stage.Compiled.Slots {
		key.writeString(slot.Codec)
		key.writeBool(slot.PreEncoded)
		if err := writeValue(&key, args[index], slot.PreEncoded || slot.Codec != ""); err != nil {
			return Fingerprint{}, err
		}
	}
	return Fingerprint{Stage: stage.Name, Digest: sha256.Sum256(key.Bytes())}, nil
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

func writeColumnType(key *writer, columnType schema.ColumnType) error {
	if columnType == nil {
		key.writeString("nil")
		return nil
	}
	value := reflect.ValueOf(columnType)
	for value.Kind() == reflect.Pointer {
		if value.IsNil() {
			key.writeString("nil")
			return nil
		}
		value = value.Elem()
	}
	key.writeString("column-type")
	typed, ok := value.Interface().(schema.ColumnType)
	if !ok {
		return planerr.New("internal_plan", "schema.type", "unsupported logical column type")
	}
	key.writeString(string(typed.Kind()))
	switch typed := typed.(type) {
	case schema.BooleanType, schema.FloatType, schema.BytesType, schema.TimeType, schema.JSONType, schema.UUIDType, schema.OpaqueType:
		return nil
	case schema.IntegerType:
		key.writeBool(typed.Unsigned)
		width, stated := typed.DisplayWidth.Value()
		key.writeBool(stated)
		if stated {
			key.writeInt(width)
		}
		key.writeBool(typed.ZeroFill)
	case schema.TextType:
		width, stated := typed.Width.Value()
		key.writeBool(stated)
		if stated {
			key.writeInt(width)
		}
		key.writeBool(typed.Fixed)
	case schema.DecimalType:
		key.writeInt(typed.Precision)
		scale, stated := typed.Scale.Value()
		key.writeBool(stated)
		if stated {
			key.writeInt(scale)
		}
		key.writeBool(typed.Unsigned)
		key.writeBool(typed.ZeroFill)
	default:
		return planerr.New("internal_plan", "schema.type", "unsupported logical column type")
	}
	return nil
}

func writeGoType(key *writer, typ reflect.Type) {
	if typ == nil {
		key.writeString("nil")
		return
	}
	key.writeU8(uint8(typ.Kind()))
	if typ.Name() != "" || typ.PkgPath() != "" {
		key.writeString("named")
		key.writeString(typ.PkgPath())
		key.writeString(typ.Name())
		return
	}
	key.writeString("unnamed")
	key.writeString(typ.String())
}
