package rasql

import (
	"bytes"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/binary"
	"reflect"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

func graphPreencodeBaseOccurrences(base compiledQuery, encoded stmt.Statement, final compiledQuery) (compiledQuery, error) {
	baseArgs := base.Statement.Args()
	encodedArgs := encoded.Args()
	finalArgs := final.Statement.Args()
	if base.Statement.SQL() != encoded.SQL() {
		return compiledQuery{}, planError("internal_plan", "binds", "base and prepared SQL differ")
	}
	if len(baseArgs) != len(base.Slots) || len(baseArgs) != len(base.CopyArgs) ||
		len(encodedArgs) != len(base.Slots) || len(finalArgs) != len(final.Slots) ||
		len(finalArgs) != len(final.CopyArgs) {
		return compiledQuery{}, planError("internal_plan", "binds", "statement arguments and bind metadata differ")
	}
	occurrences, err := matchBaseOccurrences(base, final)
	if err != nil {
		return compiledQuery{}, err
	}
	result := final
	result.Slots = append([]bindSlot(nil), final.Slots...)
	result.CopyArgs = append([]bindValueCopy(nil), final.CopyArgs...)
	args := append([]any(nil), finalArgs...)
	for i, position := range occurrences {
		if position >= len(args) || i >= len(encodedArgs) {
			return compiledQuery{}, planError("internal_plan", "binds", "encoded occurrence is out of range")
		}
		value := encodedArgs[i]
		args[position] = value
		result.Slots[position].PreEncoded = true
		result.CopyArgs[position] = func() (any, error) { return graphCloneEncoded(value), nil }
	}
	result.Statement = stmt.New(final.Statement.Text(), args...)
	return result, nil
}

func graphStageCacheable(compiled compiledQuery) bool {
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

func graphCloneEncoded(value any) any {
	switch value := value.(type) {
	case []byte:
		return append([]byte(nil), value...)
	case sql.NamedArg:
		value.Value = graphCloneEncoded(value.Value)
		return value
	default:
		return value
	}
}

// graphCloneValue owns the top-level row or graph value and direct slice fields.
// It deliberately leaves nested pointers, maps, interfaces, and opaque values
// alone because their mapper owns that state.
func graphCloneValue(value any) any {
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
			graphCloneDirectSlices(clone.Elem(), original.Elem())
		}
		return clone.Interface()
	case reflect.Struct:
		clone := reflect.New(original.Type()).Elem()
		clone.Set(original)
		graphCloneDirectSlices(clone, original)
		return clone.Interface()
	case reflect.Slice:
		return graphCloneSlice(original).Interface()
	default:
		return value
	}
}

func graphCloneDirectSlices(destination, source reflect.Value) {
	for index := 0; index < source.NumField(); index++ {
		from, to := source.Field(index), destination.Field(index)
		if !to.CanSet() || !from.CanInterface() {
			continue
		}
		switch from.Kind() {
		case reflect.Slice:
			to.Set(graphCloneSlice(from))
		case reflect.Pointer:
			if from.IsNil() || from.Type().Elem().Kind() != reflect.Slice {
				continue
			}
			clone := reflect.New(from.Type().Elem())
			clone.Elem().Set(graphCloneSlice(from.Elem()))
			to.Set(clone)
		}
	}
}

func graphCloneSlice(value reflect.Value) reflect.Value {
	if value.IsNil() {
		return reflect.Zero(value.Type())
	}
	clone := reflect.MakeSlice(value.Type(), value.Len(), value.Len())
	reflect.Copy(clone, value)
	return clone
}

func executorCompilerProfile(executor Executor) engineProfileSnapshot {
	provider, ok := executor.(compilerProvider)
	if !ok || provider.queryCompiler() == nil {
		return engineProfileSnapshot{}
	}
	p := provider.queryCompiler().EngineProfile()
	dialectName := ""
	if dialect := executor.Dialect(); dialect != nil {
		dialectName = dialect.Name()
	}
	return engineProfileSnapshot{Dialect: dialectName, ID: p.ID, Engine: p.Engine, CustomName: p.CustomName, Version: p.Version, Limits: p.Limits, Capabilities: p.Capabilities, MaxBind: p.Limits.MaxBindParameters}
}

type engineProfileSnapshot struct {
	Dialect      string
	ID           string
	Engine       EngineID
	CustomName   string
	Version      EngineVersion
	Limits       EngineLimits
	Capabilities EngineCapabilities
	MaxBind      int
}

type graphCacheFingerprint struct {
	stage  string
	digest [sha256.Size]byte
}

type graphFingerprintStage struct {
	name           string
	source         string
	schema         ResultSchema
	keys           []*graphKeySpec
	compiled       compiledQuery
	perParentLimit int
	bindLimit      int
}

type graphFingerprintWriter struct{ bytes.Buffer }

func (w *graphFingerprintWriter) writeBytes(value []byte) {
	var size [8]byte
	binary.BigEndian.PutUint64(size[:], uint64(len(value)))
	_, _ = w.Write(size[:])
	_, _ = w.Write(value)
}

func (w *graphFingerprintWriter) writeString(value string) { w.writeBytes([]byte(value)) }
func (w *graphFingerprintWriter) writeBool(value bool) {
	if value {
		_ = w.WriteByte(1)
		return
	}
	_ = w.WriteByte(0)
}
func (w *graphFingerprintWriter) writeU8(value uint8) { _ = w.WriteByte(value) }
func (w *graphFingerprintWriter) writeU16(value uint16) {
	var encoded [2]byte
	binary.BigEndian.PutUint16(encoded[:], value)
	_, _ = w.Write(encoded[:])
}
func (w *graphFingerprintWriter) writeU64(value uint64) {
	var encoded [8]byte
	binary.BigEndian.PutUint64(encoded[:], value)
	_, _ = w.Write(encoded[:])
}
func (w *graphFingerprintWriter) writeInt(value int) { w.writeU64(uint64(int64(value))) }

func graphInvocationFingerprint(stage graphFingerprintStage, profile engineProfileSnapshot) (graphCacheFingerprint, error) {
	args := stage.compiled.Statement.Args()
	if len(args) != len(stage.compiled.Slots) {
		return graphCacheFingerprint{}, planError("internal_plan", "binds", "statement arguments and bind slots differ")
	}
	if len(stage.keys) == 0 {
		return graphCacheFingerprint{}, planError("internal_plan", "graph.key", "stage key is empty")
	}

	var key graphFingerprintWriter
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
	key.writeString(stage.name)
	key.writeString(stage.source)
	key.writeString(stage.compiled.Statement.SQL())
	key.writeInt(stage.perParentLimit)
	key.writeInt(stage.bindLimit)
	columns := stage.schema.Columns()
	key.writeU64(uint64(len(columns)))
	for _, column := range columns {
		key.writeString(column.Name)
		if err := writeGraphColumnType(&key, column.Type); err != nil {
			return graphCacheFingerprint{}, err
		}
		key.writeBool(column.Nullable)
		key.writeString(column.Codec)
	}
	key.writeU64(uint64(len(stage.keys)))
	for _, keySpec := range stage.keys {
		if keySpec == nil || len(keySpec.parts) == 0 {
			return graphCacheFingerprint{}, planError("internal_plan", "graph.key", "stage key is empty")
		}
		key.writeU64(uint64(len(keySpec.parts)))
		for _, part := range keySpec.parts {
			key.writeString(part.source)
			key.writeString(part.column.Name())
			if err := writeGraphColumnType(&key, part.columnType); err != nil {
				return graphCacheFingerprint{}, err
			}
			key.writeBool(part.nullable)
			writeGraphGoType(&key, part.typ)
			key.writeString(part.codec)
		}
	}
	for index, slot := range stage.compiled.Slots {
		key.writeString(slot.Codec)
		key.writeBool(slot.PreEncoded)
		if err := writeGraphFingerprintValue(&key, args[index], slot.PreEncoded || slot.Codec != ""); err != nil {
			return graphCacheFingerprint{}, err
		}
	}
	return graphCacheFingerprint{stage: stage.name, digest: sha256.Sum256(key.Bytes())}, nil
}

func writeGraphFingerprintValue(key *graphFingerprintWriter, value any, rejectConversion bool) error {
	named, ok := value.(sql.NamedArg)
	key.writeBool(ok)
	if ok {
		key.writeString(named.Name)
		value = named.Value
	}
	var normalized driver.Value
	var err error
	if rejectConversion {
		if err = validateDriverValue(value); err == nil {
			normalized = driver.Value(value)
		}
	} else {
		normalized, err = normalizeGraphValue(value)
	}
	if err != nil {
		return planError("internal_plan", "binds", err.Error())
	}
	frame, err := frameGraphFingerprintValue(normalized)
	if err != nil {
		return planError("internal_plan", "binds", err.Error())
	}
	key.writeBytes(frame)
	return nil
}

func writeGraphColumnType(key *graphFingerprintWriter, columnType schema.ColumnType) error {
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
		return planError("internal_plan", "schema.type", "unsupported logical column type")
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
		return planError("internal_plan", "schema.type", "unsupported logical column type")
	}
	return nil
}

func writeGraphGoType(key *graphFingerprintWriter, typ reflect.Type) {
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
