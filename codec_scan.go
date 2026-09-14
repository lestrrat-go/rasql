package rasql

import (
	"database/sql"
	"fmt"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/rowvalue"
)

type codecScanSource struct {
	source  ScanSource
	columns []ResultColumn
	codecs  []ValueCodec

	// rawValues and raw are reused across every row of one execution rather
	// than allocated per row, on the same reasoning as scanSource in
	// internal/rowvalue/scan.go: s.source.Scan writes through the *any
	// destinations in raw, which rebinds each rawValues[i] to a new
	// interface value on each call and never mutates the object the old one
	// pointed at. What Scan does with a rawValues[i] afterward -- hand it to
	// a codec, or through rowvalue.AssignValue, which clones before
	// assigning -- copies out of it synchronously within this same call, so
	// nothing outlives the next Scan's overwrite. A codecScanSource is
	// built fresh per query execution in rowsPreparedRequired and never
	// shared across executions, so this buffer never widens past the one
	// execution it belongs to.
	rawValues []any
	raw       []any
}

func (s *codecScanSource) Scan(destinations ...any) error {
	if len(destinations) != len(s.columns) {
		return fmt.Errorf("rasql: scan destination count %d does not match result column count %d", len(destinations), len(s.columns))
	}
	if s.rawValues == nil {
		s.rawValues = make([]any, len(destinations))
		s.raw = make([]any, len(destinations))
		for i := range s.raw {
			s.raw[i] = &s.rawValues[i]
		}
	}
	rawValues := s.rawValues
	if err := s.source.Scan(s.raw...); err != nil {
		return err
	}
	for i, destination := range destinations {
		value := rawValues[i]
		if value == nil {
			if nullable, ok := destination.(nullableScanDestination); ok {
				nullable.nullableClear()
				continue
			}
			if scanner, ok := destination.(sql.Scanner); ok {
				if err := scanner.Scan(nil); err == nil {
					continue
				} else {
					return &DecodeError{Column: s.columns[i].Name, Codec: CodecID(s.columns[i].Codec), Err: err}
				}
			}
			v := reflect.ValueOf(destination)
			if v.IsValid() && v.Kind() == reflect.Pointer && !v.IsNil() && v.Elem().Kind() == reflect.Pointer {
				v.Elem().SetZero()
				continue
			}
			if v.IsValid() && v.Kind() == reflect.Pointer && !v.IsNil() && v.Elem().Kind() == reflect.Interface {
				v.Elem().SetZero()
				continue
			}
			return &DecodeError{Column: s.columns[i].Name, Codec: CodecID(s.columns[i].Codec), Err: ErrUnexpectedNull}
		}
		decodeDestination := destination
		var nullable nullableScanDestination
		if candidate, ok := destination.(nullableScanDestination); ok {
			nullable, decodeDestination = candidate, candidate.nullableValue()
			nullable.nullableClear()
		}
		if s.codecs[i] != nil {
			if err := s.codecs[i].Decode(value, decodeDestination); err != nil {
				return &DecodeError{Column: s.columns[i].Name, Codec: CodecID(s.columns[i].Codec), Err: err}
			}
			if nullable != nil {
				nullable.nullableValid()
			}
			continue
		}
		if err := scanValueAny(decodeDestination, value); err != nil {
			return &DecodeError{Column: s.columns[i].Name, Codec: CodecID(s.columns[i].Codec), Err: err}
		}
		if nullable != nil {
			nullable.nullableValid()
		}
	}
	return nil
}

func scanValueAny(destination any, value any) error {
	if destination == nil {
		return fmt.Errorf("rasql: scan destination must not be nil")
	}
	v := reflect.ValueOf(destination)
	if v.Kind() != reflect.Pointer || v.IsNil() {
		return fmt.Errorf("rasql: scan destination must be a non-nil pointer")
	}
	return scanValueReflect(v, value)
}
func scanValueReflect(destination reflect.Value, value any) error {
	return rowvalue.AssignValue(destination.Elem(), value)
}
