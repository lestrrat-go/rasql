package cursorcodec

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"math"
)

// Version is the leading byte every envelope carries. A reader rejects any
// other value rather than guessing at an older layout.
const Version byte = 1

// MaxEnvelopeBytes caps an envelope before base64, so a caller cannot be made
// to hold an unbounded cursor in memory.
const MaxEnvelopeBytes = 64 * 1024

// headerBytes counts the version byte, the fingerprint and the field count.
const headerBytes = 1 + 32 + 1

// Field is the per-key metadata an envelope carries alongside each value. The
// package writes and reads these bytes without interpreting them; a caller
// compares them against its own keys to decide whether a cursor still fits.
type Field struct {
	Direction uint8
	Nullable  bool
	Nulls     uint8
	Codec     string
}

// Value is one encoded key value. An absent value carries no payload.
type Value struct {
	Present bool
	Data    []byte
}

// Envelope is a decoded cursor. Fields and Values are always the same length.
type Envelope struct {
	Fingerprint [32]byte
	Fields      []Field
	Values      []Value
}

// EncodeEnvelope frames fields and values into the base64 text a cursor
// carries. It rejects anything the format cannot represent, so a cursor this
// returns always decodes.
func EncodeEnvelope(fingerprint [32]byte, fields []Field, values []Value) (string, error) {
	if len(fields) > 255 || len(values) != len(fields) {
		return "", errors.New("cursor envelope key count is out of range")
	}
	var b bytes.Buffer
	b.WriteByte(Version)
	b.Write(fingerprint[:])
	b.WriteByte(byte(len(fields)))
	for i, field := range fields {
		if len(field.Codec) > 255 || len(values[i].Data) > math.MaxUint16 {
			return "", errors.New("cursor envelope field is too large")
		}
		if !values[i].Present && len(values[i].Data) != 0 {
			return "", errors.New("absent cursor value has a payload")
		}
		b.WriteByte(field.Direction)
		if field.Nullable {
			b.WriteByte(1)
		} else {
			b.WriteByte(0)
		}
		b.WriteByte(field.Nulls)
		b.WriteByte(byte(len(field.Codec)))
		b.WriteString(field.Codec)
		if values[i].Present {
			b.WriteByte(1)
		} else {
			b.WriteByte(0)
		}
		_ = binary.Write(&b, binary.BigEndian, uint32(len(values[i].Data)))
		b.Write(values[i].Data)
	}
	if b.Len() > MaxEnvelopeBytes {
		return "", errors.New("cursor envelope exceeds 64 KiB")
	}
	return base64.RawURLEncoding.EncodeToString(b.Bytes()), nil
}

// DecodeEnvelope parses encoded back into its fields and values. It enforces
// only what the format itself requires: the version, the size cap, canonical
// one-byte markers, and that the bytes end exactly where the last value does.
// Whether the metadata suits a given query is the caller's question.
func DecodeEnvelope(encoded string) (Envelope, error) {
	var envelope Envelope
	raw, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(raw) > MaxEnvelopeBytes {
		return envelope, errors.New("invalid base64 cursor")
	}
	if len(raw) < headerBytes || raw[0] != Version {
		return envelope, errors.New("invalid cursor version")
	}
	copy(envelope.Fingerprint[:], raw[1:33])
	count := int(raw[33])
	envelope.Fields = make([]Field, count)
	envelope.Values = make([]Value, count)
	offset := headerBytes
	for i := range count {
		if offset+4 > len(raw) {
			return Envelope{}, errors.New("truncated cursor")
		}
		nullableMarker, nulls, codecLen := raw[offset+1], raw[offset+2], int(raw[offset+3])
		if nullableMarker > 1 {
			return Envelope{}, errors.New("invalid cursor metadata marker")
		}
		field := Field{Direction: raw[offset], Nullable: nullableMarker == 1, Nulls: nulls}
		offset += 4
		if offset+codecLen+5 > len(raw) {
			return Envelope{}, errors.New("truncated cursor metadata")
		}
		field.Codec = string(raw[offset : offset+codecLen])
		offset += codecLen
		presenceMarker := raw[offset]
		if presenceMarker > 1 {
			return Envelope{}, errors.New("invalid cursor presence marker")
		}
		offset++
		length := int(binary.BigEndian.Uint32(raw[offset:]))
		offset += 4
		if length > len(raw)-offset {
			return Envelope{}, errors.New("invalid cursor length")
		}
		present := presenceMarker == 1
		if !present && length != 0 {
			return Envelope{}, errors.New("absent cursor value has a payload")
		}
		envelope.Fields[i] = field
		envelope.Values[i] = Value{Present: present, Data: append([]byte(nil), raw[offset:offset+length]...)}
		offset += length
	}
	if offset != len(raw) {
		return Envelope{}, errors.New("trailing cursor data")
	}
	return envelope, nil
}
