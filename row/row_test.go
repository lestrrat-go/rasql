package row_test

import (
	"testing"
	"time"

	"github.com/lestrrat-go/rasql/row"
	"github.com/stretchr/testify/require"
)

func TestTypedColumnsDecodeValues(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	inputBytes := []byte("payload")
	result, err := row.New(
		[]string{"active", "id", "ratio", "name", "payload", "created_at"},
		[]any{true, int64(42), 1.5, []byte("Ada"), inputBytes, createdAt},
	)
	require.NoError(t, err)

	active, err := row.Bool("active")
	require.NoError(t, err)
	id, err := row.Int64("id")
	require.NoError(t, err)
	ratio, err := row.Float64("ratio")
	require.NoError(t, err)
	name, err := row.String("name")
	require.NoError(t, err)
	payload, err := row.Bytes("payload")
	require.NoError(t, err)
	when, err := row.Time("created_at")
	require.NoError(t, err)

	gotActive, err := active.Get(result)
	require.NoError(t, err)
	require.True(t, gotActive)
	gotID, err := id.Get(result)
	require.NoError(t, err)
	require.Equal(t, int64(42), gotID)
	gotRatio, err := ratio.Get(result)
	require.NoError(t, err)
	require.Equal(t, 1.5, gotRatio)
	gotName, err := name.Get(result)
	require.NoError(t, err)
	require.Equal(t, "Ada", gotName)
	gotPayload, err := payload.Get(result)
	require.NoError(t, err)
	require.Equal(t, []byte("payload"), gotPayload)
	gotWhen, err := when.Get(result)
	require.NoError(t, err)
	require.Equal(t, createdAt, gotWhen)

	inputBytes[0] = 'P'
	require.Equal(t, []byte("payload"), gotPayload)
}

func TestNullableDecoderPreservesNull(t *testing.T) {
	result, err := row.New([]string{"name"}, []any{nil})
	require.NoError(t, err)
	name, err := row.NewColumn("name", row.Nullable(row.DecoderFunc[string](func(value any) (string, error) {
		return value.(string), nil
	})))
	require.NoError(t, err)

	got, err := name.Get(result)
	require.NoError(t, err)
	require.False(t, got.Valid)
}

func TestTypedColumnsRejectWrongType(t *testing.T) {
	result, err := row.New([]string{"id"}, []any{"42"})
	require.NoError(t, err)
	id, err := row.Int64("id")
	require.NoError(t, err)

	_, err = id.Get(result)
	require.Error(t, err)
}

func TestGetAndDecodePopulateTypedValues(t *testing.T) {
	result, err := row.New(
		[]string{"id", "email", "nickname"},
		[]any{int64(42), []byte("ada@example.com"), nil},
	)
	require.NoError(t, err)

	id, err := row.Get[int64](result, "id")
	require.NoError(t, err)
	require.Equal(t, int64(42), id)

	type user struct {
		ID       int64
		Email    string
		Nickname *string
	}
	decoded, err := row.Decode[user](result)
	require.NoError(t, err)
	require.Equal(t, int64(42), decoded.ID)
	require.Equal(t, "ada@example.com", decoded.Email)
	require.Nil(t, decoded.Nickname)

	type summary struct {
		UserID int64 `rasql:"id"`
	}
	aliased, err := row.Decode[summary](result)
	require.NoError(t, err)
	require.Equal(t, int64(42), aliased.UserID)
}

func TestDecodeRejectsMissingColumnsAndUnsupportedDestinations(t *testing.T) {
	result, err := row.New([]string{"id"}, []any{int64(42)})
	require.NoError(t, err)

	type user struct {
		Email string `rasql:"email"`
	}
	_, err = row.Decode[user](result)
	require.Error(t, err)
	_, err = row.Decode[string](result)
	require.Error(t, err)
}

func TestNewRejectsInvalidShape(t *testing.T) {
	_, err := row.New([]string{"id"}, nil)
	require.Error(t, err)

	_, err = row.New([]string{"id", "id"}, []any{int64(1), int64(2)})
	require.Error(t, err)
}
