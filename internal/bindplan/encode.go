package bindplan

import (
	"database/sql"
	"database/sql/driver"
	"fmt"

	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/stmt"
)

// Encoder runs a statement's bound values through their codecs. The codec
// registry and the errors it reports belong to the root package, so it passes
// one of these in rather than this package reaching back for them.
type Encoder interface {
	// CheckBindCodec reports a codec the registry does not hold. It is asked
	// for every slot, including ones whose value is never encoded, so a
	// missing codec is reported whatever the value turns out to be.
	CheckBindCodec(codec string) error
	// EncodeBind encodes one bound value. index identifies the argument, so a
	// failure can say which one.
	EncodeBind(index int, codec string, value any) (driver.Value, error)
}

// EncodeStatement replaces each bound value with what its codec makes of it.
// A value already encoded is left alone beyond a copy, and a nil value stays
// nil rather than reaching a codec that would not expect it.
func EncodeStatement(statement stmt.Statement, slots []Slot, encoder Encoder) (stmt.Statement, error) {
	args := statement.BoundArgs()
	if len(args) != len(slots) {
		return stmt.Statement{}, planerr.New("bind_mismatch", "", "statement arguments and bind slots differ")
	}
	for i, slot := range slots {
		if err := encoder.CheckBindCodec(slot.Codec); err != nil {
			return stmt.Statement{}, err
		}
		value := args[i]
		name := ""
		if named, ok := value.(sql.NamedArg); ok {
			name, value = named.Name, named.Value
		}
		if slot.PreEncoded {
			if err := validDriverValue(value); err != nil {
				return stmt.Statement{}, planerr.New("internal_plan", fmt.Sprintf("binds[%d]", i), err.Error())
			}
			if b, ok := value.([]byte); ok {
				value = append([]byte(nil), b...)
			}
			args[i] = rename(name, value)
			continue
		}
		// A slot with no codec keeps whatever it already holds, and a nil
		// value stays nil rather than reaching a codec that would not expect
		// it. The order matches what the root package did before this moved.
		if slot.Codec == "" {
			continue
		}
		if value == nil {
			args[i] = rename(name, nil)
			continue
		}
		encoded, err := encoder.EncodeBind(i, slot.Codec, value)
		if err != nil {
			return stmt.Statement{}, err
		}
		args[i] = rename(name, encoded)
	}
	return stmt.New(statement.Text(), args...), nil
}

// rename puts a value back under the argument name it arrived with, when it
// had one.
func rename(name string, value any) any {
	if name != "" {
		return sql.Named(name, value)
	}
	return value
}

// validDriverValue reports a value database/sql would refuse to carry.
// internal/graphkey holds the same check for its own use; importing it here
// would close a loop, because it already reads this package's codec pattern.
func validDriverValue(value driver.Value) error {
	if value == nil || driver.IsValue(value) {
		return nil
	}
	return fmt.Errorf("value %T is not a legal driver value", value)
}
