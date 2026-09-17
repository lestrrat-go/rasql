package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"strings"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/stretchr/testify/require"
)

type registryCodec struct{}

func (registryCodec) Encode(value any) (driver.Value, error) { return value, nil }
func (registryCodec) Decode(source any, destination any) error {
	*destination.(*string) = source.(string)
	return nil
}

func TestCodecRegistry(t *testing.T) {
	registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"text": registryCodec{}})
	require.NoError(t, err)
	codec, ok := registry.Lookup("text")
	require.True(t, ok)
	require.NotNil(t, codec)
	_, err = rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"": registryCodec{}})
	require.Error(t, err)
}

// nilCodecScopedExecutor is the shape testdata/compile/runtime_api/positive/main.go
// declares legal: an executor implementing CodecProvider that returns nil
// from it. It also opens a scope, exercising the case that used to reach a
// caller with the nil intact.
type nilCodecScopedExecutor struct{ rasql.Executor }

func (nilCodecScopedExecutor) Codecs() rasql.CodecRegistry { return nil }
func (e nilCodecScopedExecutor) BeginScope(context.Context, *sql.TxOptions) (rasql.Executor, rasql.ScopeFinalizer, error) {
	return e, nilCodecFinalizer{}, nil
}
func (e nilCodecScopedExecutor) BeginSavepoint(context.Context) (rasql.Executor, rasql.ScopeFinalizer, error) {
	return e, nilCodecFinalizer{}, nil
}

type nilCodecFinalizer struct{}

func (nilCodecFinalizer) Commit(context.Context) error   { return nil }
func (nilCodecFinalizer) Rollback(context.Context) error { return nil }

// A nil registry is an error rather than an executor without codecs, so every
// caller that reads one reports the same code. PageAfter used to dereference
// the nil instead, and whether it did depended on whether the executor opened
// a scope, because only the scoped wrapper passed the nil along.
func TestNilCodecRegistryIsAnError(t *testing.T) {
	table, err := rasql.TableOf[int64](schema.TableDef{Name: "items", Columns: []schema.ColumnDef{{Name: "id", Type: schema.IntegerType{}}}})
	require.NoError(t, err)
	relation, err := table.As("i")
	require.NoError(t, err)
	id, err := rasql.BindColumn[int64, int64](relation, "id", "")
	require.NoError(t, err)
	resultSchema, err := rasql.NewResultSchema(rasql.ResultColumn{Name: "value", Type: schema.IntegerType{}})
	require.NoError(t, err)
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{rasql.Item("value", id.Expr(), schema.IntegerType{}, "")}, runtimeDecoder{schema: resultSchema})
	require.NoError(t, err)
	baseQuery := rasql.Select(relation, projection)
	orderExpr, err := rasql.ValueWithCodec(int64(7), "count.page")
	require.NoError(t, err)
	orderKey := rasql.AscKey[int64](orderExpr, func(int64) int64 { return 7 })
	idKey := rasql.AscKey[int64](id.Expr(), func(value int64) int64 { return value })
	spec, err := rasql.NewPageSpec([]rasql.PageKey[int64]{orderKey, idKey}, idKey)
	require.NoError(t, err)
	profile := rasql.SQLite335()
	raw := &runtimeFakeExecutor{rows: [][]any{{int64(1)}}, dialect: dialect.SQLite()}
	executor, err := rasql.WithEngineProfile(nilCodecScopedExecutor{raw}, profile)
	require.NoError(t, err)

	requireRegistryUnavailable := func(t *testing.T, err error) {
		t.Helper()
		require.Error(t, err)
		var planErr *rasql.PlanError
		require.ErrorAs(t, err, &planErr)
		require.Equal(t, "codec_registry_unavailable", planErr.Code)
	}

	t.Run("a paged read reports it rather than dereferencing the nil", func(t *testing.T) {
		_, err := rasql.PageAfter(t.Context(), executor, baseQuery, spec, rasql.PagePolicy{DefaultLimit: 1, MaxLimit: 3}, rasql.PageRequest{Limit: 1})
		requireRegistryUnavailable(t, err)
	})

	t.Run("a read reports it", func(t *testing.T) {
		_, err := rasql.All(t.Context(), executor, baseQuery)
		requireRegistryUnavailable(t, err)
	})

	// The decorator WithEngineProfile builds forwards Codecs to the executor
	// it wraps, which used to substitute the builtin registry and let the
	// same mistake through.
	t.Run("an executor that opens no scope reports it too", func(t *testing.T) {
		unscoped, err := rasql.WithEngineProfile(struct {
			rasql.Executor
			rasql.CodecProvider
		}{raw, nilCodecScopedExecutor{}}, profile)
		require.NoError(t, err)
		_, err = rasql.All(t.Context(), unscoped, baseQuery)
		requireRegistryUnavailable(t, err)
	})

	t.Run("an executor carrying no registry at all still reads", func(t *testing.T) {
		plain, err := rasql.WithEngineProfile(raw, profile)
		require.NoError(t, err)
		_, err = rasql.All(t.Context(), plain, baseQuery)
		require.NoError(t, err)
	})
}

func TestCodecErrors(t *testing.T) {
	// A column that no codec decoded failed inside rasql's own conversion, so its
	// message names the cause. TestCodecErrors/"codec cause text stays hidden" covers the other
	// half of the same rule, where a codec produced the error and its text stays
	// out of the message.
	t.Run("decode error names its cause without a codec", func(t *testing.T) {
		cause := errors.New(`expected int64, got []uint8`)
		err := &rasql.DecodeError{Column: "total", Err: cause}
		require.True(t, errors.Is(err, cause))
		require.Equal(t, `decode column "total" failed: expected int64, got []uint8`, err.Error())
	})

	t.Run("codec cause text stays hidden", func(t *testing.T) {
		secret := errors.New("secret value should not be printed")
		err := &rasql.DecodeError{Column: "payload", Codec: "text", Err: secret}
		require.True(t, errors.Is(err, secret))
		require.False(t, strings.Contains(err.Error(), "secret value"))
		err2 := &rasql.EncodeError{Index: 2, Codec: "text", Err: secret}
		require.True(t, errors.Is(err2, secret))
		require.False(t, strings.Contains(err2.Error(), "secret value"))
	})
}
