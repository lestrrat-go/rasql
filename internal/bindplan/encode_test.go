package bindplan_test

import (
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql/internal/bindplan"
	"github.com/lestrrat-go/rasql/internal/planerr"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

var errEncodeProbe = errors.New("codec refused the value")

// countingEncoder answers one codec name, counts what it encoded, and refuses
// every other name the way a registry without that codec would.
type countingEncoder struct {
	known   string
	encodes int
	fail    bool
}

func (e *countingEncoder) CheckBindCodec(codec string) error {
	if codec == "" || codec == e.known {
		return nil
	}
	return planerr.New("codec_unavailable", "codec", codec)
}

func (e *countingEncoder) EncodeBind(index int, codec string, value any) (driver.Value, error) {
	if e.fail {
		return nil, fmt.Errorf("encode bind %d: %w", index, errEncodeProbe)
	}
	e.encodes++
	return fmt.Sprintf("encoded:%v", value), nil
}

func TestEncodeStatement(t *testing.T) {
	t.Run("encodes each value through its codec", func(t *testing.T) {
		encoder := &countingEncoder{known: "text"}
		encoded, err := bindplan.EncodeStatement(
			stmt.New(sqltext.Text("SELECT ?, ?"), int64(1), int64(2)),
			[]bindplan.Slot{{Codec: "text"}, {Codec: "text"}}, encoder)
		require.NoError(t, err)
		require.Equal(t, 2, encoder.encodes, "each occurrence encodes on its own")
		require.Equal(t, "encoded:1", encoded.Args()[0])
		require.Equal(t, "encoded:2", encoded.Args()[1])
	})

	// A codec never sees a NULL, because a codec written for a value would not
	// expect one, so the argument stays nil and keeps its name.
	t.Run("a named null keeps its name and skips the codec", func(t *testing.T) {
		encoder := &countingEncoder{known: "text"}
		encoded, err := bindplan.EncodeStatement(
			stmt.New(sqltext.Text("SELECT ?"), sql.Named("value", nil)),
			[]bindplan.Slot{{Codec: "text"}}, encoder)
		require.NoError(t, err)
		require.Zero(t, encoder.encodes)
		arg, ok := encoded.Args()[0].(sql.NamedArg)
		require.True(t, ok)
		require.Equal(t, "value", arg.Name)
		require.Nil(t, arg.Value)
	})

	t.Run("a slot with no codec is left alone", func(t *testing.T) {
		encoder := &countingEncoder{known: "text"}
		encoded, err := bindplan.EncodeStatement(
			stmt.New(sqltext.Text("SELECT ?"), int64(7)),
			[]bindplan.Slot{{}}, encoder)
		require.NoError(t, err)
		require.Zero(t, encoder.encodes)
		require.Equal(t, int64(7), encoded.Args()[0])
	})

	// An already encoded value is copied rather than re-encoded, so a caller
	// writing through what it bound cannot change what the statement sends.
	t.Run("a pre-encoded value is copied, not encoded again", func(t *testing.T) {
		encoder := &countingEncoder{known: "text"}
		payload := []byte("original")
		encoded, err := bindplan.EncodeStatement(
			stmt.New(sqltext.Text("SELECT ?"), payload),
			[]bindplan.Slot{{Codec: "text", PreEncoded: true}}, encoder)
		require.NoError(t, err)
		require.Zero(t, encoder.encodes)
		require.Equal(t, []byte("original"), encoded.Args()[0])
		payload[0] = 'X'
		require.Equal(t, []byte("original"), encoded.Args()[0])
	})

	t.Run("rejects a pre-encoded value no driver carries", func(t *testing.T) {
		_, err := bindplan.EncodeStatement(
			stmt.New(sqltext.Text("SELECT ?"), struct{ Field int }{}),
			[]bindplan.Slot{{PreEncoded: true}}, &countingEncoder{})
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "internal_plan", planErr.Code)
	})

	// The codec is checked for every slot, including one whose value never
	// reaches it, so a missing codec is reported whatever the value is.
	t.Run("reports a missing codec even for a null value", func(t *testing.T) {
		_, err := bindplan.EncodeStatement(
			stmt.New(sqltext.Text("SELECT ?"), nil),
			[]bindplan.Slot{{Codec: "absent"}}, &countingEncoder{known: "text"})
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "codec_unavailable", planErr.Code)
	})

	t.Run("reports what the codec refused", func(t *testing.T) {
		_, err := bindplan.EncodeStatement(
			stmt.New(sqltext.Text("SELECT ?"), int64(1)),
			[]bindplan.Slot{{Codec: "text"}}, &countingEncoder{known: "text", fail: true})
		require.ErrorIs(t, err, errEncodeProbe)
	})

	t.Run("rejects a slot count that differs from the arguments", func(t *testing.T) {
		_, err := bindplan.EncodeStatement(
			stmt.New(sqltext.Text("SELECT ?, ?"), int64(1), int64(2)),
			[]bindplan.Slot{{}}, &countingEncoder{})
		var planErr *planerr.Error
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "bind_mismatch", planErr.Code)
	})
}
