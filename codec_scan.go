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
}

func (s codecScanSource) Scan(destinations ...any) error {
	if len(destinations) != len(s.columns) {
		return fmt.Errorf("rasql: scan destination count %d does not match result column count %d", len(destinations), len(s.columns))
	}
	rawValues := make([]any, len(destinations))
	raw := make([]any, len(destinations))
	for i := range raw {
		raw[i] = &rawValues[i]
	}
	if err := s.source.Scan(raw...); err != nil {
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
	row, err := rowvalue.NewRow([]string{scanValueColumn}, []any{value})
	if err != nil {
		return err
	}
	return rowvalue.AssignReflect(row, scanValueColumn, destination.Elem())
}
