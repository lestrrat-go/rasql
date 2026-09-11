// Package pagekey builds the value a keyset cursor carries to prove it belongs
// to the query that produced it. Anything that would change which rows a page
// returns goes into the value, so a stale cursor is refused rather than paging
// the wrong rows.
package pagekey

import (
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"reflect"
	"time"

	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
)

// Field is the part of one page key the fingerprint records. Two queries
// ordering by the same columns in different directions must not share a value.
type Field struct {
	Direction byte
	Nulls     byte
	Codec     string
	Source    string
}

// Fingerprint identifies the query a cursor belongs to. A cursor carries this
// value, so a cursor handed to a different query, or to the same query after
// its shape changed, is refused rather than silently paging the wrong rows.
func Fingerprint(engine, sqlText string, columns []query.ResultColumn, statement stmt.Statement, keys []Field, indexes []int) ([32]byte, error) {
	h := sha256.New()
	writeField(h, "rasql-keyset-v1")
	writeField(h, engine)
	writeField(h, sqlText)
	writeUintField(h, "column-count", uint64(len(columns)))
	for _, column := range columns {
		writeField(h, column.Name)
		if err := writeColumnType(h, column.Type); err != nil {
			return [32]byte{}, err
		}
		writeField(h, column.Codec)
		if column.Nullable {
			writeField(h, "nullable")
		} else {
			writeField(h, "required")
		}
	}
	writeUintField(h, "key-count", uint64(len(keys)))
	for _, key := range keys {
		writeField(h, string([]byte{key.Direction, key.Nulls}))
		writeField(h, key.Codec)
		writeField(h, key.Source)
	}
	args := statement.Args()
	writeUintField(h, "argument-count", uint64(len(indexes)))
	for _, index := range indexes {
		if index < 0 || index >= len(args) {
			return [32]byte{}, planerr.New("internal_plan", "binds", "matched argument index is invalid")
		}
		if err := writeFingerprintValue(h, args[index]); err != nil {
			return [32]byte{}, err
		}
	}
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result, nil
}

func writeColumnType(h hashWriter, columnType schema.ColumnType) error {
	writeField(h, "column-type")
	writeField(h, string(columnType.Kind()))
	writeField(h, "parameters")
	switch typed := columnType.(type) {
	case schema.BooleanType, schema.FloatType, schema.BytesType, schema.TimeType, schema.JSONType, schema.UUIDType, schema.OpaqueType:
		return nil
	case schema.IntegerType:
		writeBoolField(h, "unsigned", typed.Unsigned)
		width, stated := typed.DisplayWidth.Value()
		writeBoolField(h, "display-width-stated", stated)
		if stated {
			writeIntField(h, "display-width", int64(width))
		}
		writeBoolField(h, "zerofill", typed.ZeroFill)
	case schema.TextType:
		width, stated := typed.Width.Value()
		writeBoolField(h, "width-stated", stated)
		if stated {
			writeIntField(h, "width", int64(width))
		}
		writeBoolField(h, "fixed", typed.Fixed)
	case schema.DecimalType:
		writeIntField(h, "precision", int64(typed.Precision))
		scale, stated := typed.Scale.Value()
		writeBoolField(h, "scale-stated", stated)
		if stated {
			writeIntField(h, "scale", int64(scale))
		}
		writeBoolField(h, "unsigned", typed.Unsigned)
		writeBoolField(h, "zerofill", typed.ZeroFill)
	default:
		return fmt.Errorf("unsupported column type %T", columnType)
	}
	return nil
}

func writeBoolField(h hashWriter, tag string, value bool) {
	writeField(h, tag)
	if value {
		writeField(h, "1")
	} else {
		writeField(h, "0")
	}
}

func writeIntField(h hashWriter, tag string, value int64) {
	writeField(h, tag)
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], uint64(value))
	writeField(h, string(data[:]))
}

func writeUintField(h hashWriter, tag string, value uint64) {
	writeField(h, tag)
	var data [8]byte
	binary.BigEndian.PutUint64(data[:], value)
	writeField(h, string(data[:]))
}

func writeField(h hashWriter, value string) {
	var length [4]byte
	binary.BigEndian.PutUint32(length[:], uint32(len(value)))
	_, _ = h.Write(length[:])
	_, _ = h.Write([]byte(value))
}

type hashWriter interface{ Write([]byte) (int, error) }

func writeFingerprintValue(h hashWriter, value any) error {
	if named, ok := value.(sql.NamedArg); ok {
		writeField(h, "named")
		writeField(h, named.Name)
		return writeFingerprintValue(h, named.Value)
	}
	switch value := value.(type) {
	case nil:
		writeField(h, "nil")
	case int64:
		writeField(h, "int64")
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], uint64(value))
		_, _ = h.Write(b[:])
	case float64:
		writeField(h, "float64")
		var b [8]byte
		binary.BigEndian.PutUint64(b[:], math.Float64bits(value))
		_, _ = h.Write(b[:])
	case bool:
		writeField(h, "bool")
		if value {
			_, _ = h.Write([]byte{1})
		} else {
			_, _ = h.Write([]byte{0})
		}
	case string:
		writeField(h, "string")
		writeField(h, value)
	case []byte:
		writeField(h, "bytes")
		writeField(h, string(value))
	case time.Time:
		writeField(h, "time")
		writeField(h, value.UTC().Format(time.RFC3339Nano))
	default:
		v := reflect.ValueOf(value)
		switch v.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
			writeField(h, v.Type().String())
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], uint64(v.Int()))
			_, _ = h.Write(b[:])
		case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr:
			writeField(h, v.Type().String())
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], v.Uint())
			_, _ = h.Write(b[:])
		case reflect.Float32, reflect.Float64:
			writeField(h, v.Type().String())
			var b [8]byte
			binary.BigEndian.PutUint64(b[:], math.Float64bits(v.Float()))
			_, _ = h.Write(b[:])
		default:
			return fmt.Errorf("unsupported driver value %T", value)
		}
	}
	return nil
}
