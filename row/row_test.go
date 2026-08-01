package row_test

import (
	"errors"
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

func TestTypedColumnsDecodeSQLiteValues(t *testing.T) {
	createdAt := time.Date(2026, time.August, 1, 12, 30, 0, 0, time.UTC)
	result, err := row.New(
		[]string{"active", "created_at"},
		[]any{int64(1), createdAt.String()},
	)
	require.NoError(t, err)

	active, err := row.Bool("active")
	require.NoError(t, err)
	when, err := row.Time("created_at")
	require.NoError(t, err)

	gotActive, err := active.Get(result)
	require.NoError(t, err)
	require.True(t, gotActive)
	gotWhen, err := when.Get(result)
	require.NoError(t, err)
	require.Equal(t, createdAt, gotWhen)
}

func TestNullableDecoderPreservesNull(t *testing.T) {
	result, err := row.New([]string{"name"}, []any{nil})
	require.NoError(t, err)
	name, err := row.NewColumn("name", row.Nullable(row.ColumnDecoderFunc[string](func(value any) (string, error) {
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

func TestAssignDecodesIntoDestination(t *testing.T) {
	result, err := row.New(
		[]string{"id", "email", "nickname"},
		[]any{int64(42), []byte("ada@example.com"), nil},
	)
	require.NoError(t, err)

	var id int64
	require.NoError(t, row.Assign(result, "id", &id))
	require.Equal(t, int64(42), id)

	var email string
	require.NoError(t, row.Assign(result, "email", &email))
	require.Equal(t, "ada@example.com", email)

	// A nullable column decodes into a nil pointer rather than failing.
	nickname := new(string)
	require.NoError(t, row.Assign(result, "nickname", &nickname))
	require.Nil(t, nickname)

	t.Run("missing column", func(t *testing.T) {
		var missing int64
		err := row.Assign(result, "nope", &missing)
		require.ErrorContains(t, err, `row: column "nope" is not present`)
	})

	t.Run("nil destination", func(t *testing.T) {
		var destination *int64
		err := row.Assign(result, "id", destination)
		require.ErrorContains(t, err, `row: destination for column "id" must not be nil`)
	})

	t.Run("conversion failure", func(t *testing.T) {
		var wrong bool
		err := row.Assign(result, "email", &wrong)
		require.ErrorContains(t, err, `row: decode column "email"`)
	})
}

// selfDecodedUser maps its own columns and carries a tag that names a different
// column, so a decode through the tag is distinguishable from one through the method.
type selfDecodedUser struct {
	ID    int64 `rasql:"nickname"`
	Email string
	Extra string
}

func (u *selfDecodedUser) DecodeRow(r row.Row) error {
	if err := row.Assign(r, "id", &u.ID); err != nil {
		return err
	}
	if err := row.Assign(r, "email", &u.Email); err != nil {
		return err
	}
	u.Extra = "derived"
	return nil
}

// failingDecoder reports an error from DecodeRow so Decode's wrapping is visible.
type failingDecoder struct{}

func (failingDecoder) DecodeRow(row.Row) error {
	return errors.New("mapping failed")
}

// selfDecodedCount is not a struct, which the reflection path rejects.
type selfDecodedCount int64

func (c *selfDecodedCount) DecodeRow(r row.Row) error {
	return row.Assign(r, "id", (*int64)(c))
}

func TestDecodePrefersRowDecoder(t *testing.T) {
	result, err := row.New(
		[]string{"id", "email", "nickname"},
		[]any{int64(42), []byte("ada@example.com"), int64(7)},
	)
	require.NoError(t, err)

	decoded, err := row.Decode[selfDecodedUser](result)
	require.NoError(t, err)
	// The tag names "nickname", so 42 proves DecodeRow ran instead of the tag path.
	require.Equal(t, int64(42), decoded.ID)
	require.Equal(t, "ada@example.com", decoded.Email)
	require.Equal(t, "derived", decoded.Extra)

	t.Run("propagates the error", func(t *testing.T) {
		_, err := row.Decode[failingDecoder](result)
		require.ErrorContains(t, err, "mapping failed")
		require.ErrorContains(t, err, "row: decode")
	})

	t.Run("accepts a non-struct destination", func(t *testing.T) {
		count, err := row.Decode[selfDecodedCount](result)
		require.NoError(t, err)
		require.Equal(t, selfDecodedCount(42), count)
	})
}

func TestNewRejectsInvalidShape(t *testing.T) {
	_, err := row.New([]string{"id"}, nil)
	require.Error(t, err)

	_, err = row.New([]string{"id", "id"}, []any{int64(1), int64(2)})
	require.Error(t, err)
}
