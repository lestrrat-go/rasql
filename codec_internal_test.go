package rasql

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type testCodec struct{}

func (testCodec) Encode(value any) (driver.Value, error) { return value, nil }
func (testCodec) Decode(source any, destination any) error {
	*destination.(*string) = source.(string)
	return nil
}

func TestCodecRegistry(t *testing.T) {
	registry, err := NewCodecRegistry(map[CodecID]ValueCodec{"text": testCodec{}})
	require.NoError(t, err)
	codec, ok := registry.Lookup("text")
	require.True(t, ok)
	require.NotNil(t, codec)
	_, err = NewCodecRegistry(map[CodecID]ValueCodec{"": testCodec{}})
	require.Error(t, err)
	_, err = NewCodecRegistry(map[CodecID]ValueCodec{"nil": (*testCodec)(nil)})
	require.Error(t, err)
}

func TestCodecErrors(t *testing.T) {
	// A column that no codec decoded failed inside rasql's own conversion, so its
	// message names the cause. TestCodecErrors/"codec cause text stays hidden" covers the other
	// half of the same rule, where a codec produced the error and its text stays
	// out of the message.
	t.Run("decode error names its cause without a codec", func(t *testing.T) {
		cause := errors.New(`expected int64, got []uint8`)
		err := &DecodeError{Column: "total", Err: cause}
		require.True(t, errors.Is(err, cause))
		require.Equal(t, `decode column "total" failed: expected int64, got []uint8`, err.Error())
	})

	t.Run("codec cause text stays hidden", func(t *testing.T) {
		secret := errors.New("secret value should not be printed")
		err := &DecodeError{Column: "payload", Codec: "text", Err: secret}
		require.True(t, errors.Is(err, secret))
		require.False(t, strings.Contains(err.Error(), "secret value"))
		err2 := &EncodeError{Index: 2, Codec: "text", Err: secret}
		require.True(t, errors.Is(err2, secret))
		require.False(t, strings.Contains(err2.Error(), "secret value"))
	})
}

type nullableStringCodec struct{ dec *int }

func (c nullableStringCodec) Encode(value any) (driver.Value, error) { return value, nil }
func (c nullableStringCodec) Decode(source any, destination any) error {
	if c.dec != nil {
		*c.dec = *c.dec + 1
	}
	*destination.(*string) = source.(string)
	return nil
}

func TestCodecScan(t *testing.T) {
	t.Run("nullable present and null values", func(t *testing.T) {
		count := 0
		codec := nullableStringCodec{dec: &count}
		columns := []ResultColumn{{Name: "value", Type: schema.TextType{}, Nullable: true, Codec: "text"}}
		_, err := NewCodecRegistry(map[CodecID]ValueCodec{"text": codec})
		require.NoError(t, err)
		rows := &runtimeFakeRows{values: [][]any{{"present"}, {nil}}}
		source := codecScanSource{source: rows, columns: columns, codecs: []ValueCodec{codec}}
		var present Nullable[string]
		require.NoError(t, source.Scan(&present))
		require.True(t, present.Valid)
		require.Equal(t, "present", present.Value)
		var absent Nullable[string]
		require.NoError(t, source.Scan(&absent))
		require.False(t, absent.Valid)
		require.Empty(t, absent.Value)
		require.Equal(t, 1, count)
	})

	t.Run("preserves scanner null cause", func(t *testing.T) {
		codec := nullableStringCodec{}
		source := codecScanSource{source: &runtimeFakeRows{values: [][]any{{nil}}}, columns: []ResultColumn{{Name: "value", Codec: "text"}}, codecs: []ValueCodec{codec}}
		err := source.Scan(&rejectingNullScanner{})
		var decodeErr *DecodeError
		require.ErrorAs(t, err, &decodeErr)
		require.ErrorIs(t, err, errScannerNull)
		require.NotContains(t, err.Error(), "<nil>")
		var sqlNull sql.NullString
		source = codecScanSource{source: &runtimeFakeRows{values: [][]any{{nil}}}, columns: source.columns, codecs: source.codecs}
		require.NoError(t, source.Scan(&sqlNull))
		require.False(t, sqlNull.Valid)
	})

	t.Run("invalidates nullable before failed decode", func(t *testing.T) {
		failure := errors.New("decode failed")
		source := codecScanSource{source: &runtimeFakeRows{values: [][]any{{"new"}}}, columns: []ResultColumn{{Name: "value", Codec: "text"}}, codecs: []ValueCodec{failingRuntimeCodec{err: failure}}}
		destination := Nullable[string]{Value: "old", Valid: true}
		err := source.Scan(&destination)
		require.ErrorIs(t, err, failure)
		require.False(t, destination.Valid)
		require.Empty(t, destination.Value)
		builtin := codecScanSource{source: &runtimeFakeRows{values: [][]any{{int64(1)}}}, columns: []ResultColumn{{Name: "value"}}, codecs: []ValueCodec{nil}}
		destination = Nullable[string]{Value: "old", Valid: true}
		err = builtin.Scan(&destination)
		require.Error(t, err)
		require.False(t, destination.Valid)
		require.Empty(t, destination.Value)
	})
}

type rejectingNullScanner struct{}

var errScannerNull = errors.New("scanner rejected null")

func (*rejectingNullScanner) Scan(any) error { return errScannerNull }

type failingRuntimeCodec struct{ err error }

func (c failingRuntimeCodec) Encode(any) (driver.Value, error) { return nil, nil }
func (c failingRuntimeCodec) Decode(any, any) error            { return c.err }
