package rasql

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"

	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type nullableStringCodec struct{ dec *int }

func (c nullableStringCodec) Encode(value any) (driver.Value, error) { return value, nil }
func (c nullableStringCodec) Decode(source any, destination any) error {
	if c.dec != nil {
		*c.dec = *c.dec + 1
	}
	*destination.(*string) = source.(string)
	return nil
}

func TestCodecScanSupportsNullablePresentAndNullValues(t *testing.T) {
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
}

type rejectingNullScanner struct{}

var errScannerNull = errors.New("scanner rejected null")

func (*rejectingNullScanner) Scan(any) error { return errScannerNull }

func TestCodecScanPreservesScannerNullCause(t *testing.T) {
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
}
