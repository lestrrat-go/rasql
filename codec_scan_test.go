package rasql_test

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"testing"

	"github.com/lestrrat-go/rasql"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/schema"
	"github.com/lestrrat-go/rasql/stmt"
	"github.com/stretchr/testify/require"
)

// The codec scan source sits between an executor's rows and a row decoder, so
// these cases reach it the way anything else does, by running a query whose
// decoder scans one column through a registered codec.

var errCodecScannerNull = errors.New("scanner rejected null")

type codecScanCounting struct{ decodes *int }

func (codecScanCounting) Encode(value any) (driver.Value, error) { return value, nil }
func (c codecScanCounting) Decode(source any, destination any) error {
	if c.decodes != nil {
		*c.decodes++
	}
	*destination.(*string) = source.(string)
	return nil
}

type codecScanFailing struct{ err error }

func (codecScanFailing) Encode(any) (driver.Value, error) { return nil, nil }
func (c codecScanFailing) Decode(any, any) error          { return c.err }

// codecScanRejectingNull refuses a NULL, so the cause it returns is what the
// decode error has to carry back.
type codecScanRejectingNull struct{}

func (*codecScanRejectingNull) Scan(any) error { return errCodecScannerNull }

// codecScanQuery runs one query whose single column is decoded by decode, and
// returns what the row decoder produced.
func codecScanQuery[R any](t *testing.T, values [][]any, codec rasql.ValueCodec, nullable bool,
	decode func(rasql.ScanSource, *R) error) ([]R, error) {
	t.Helper()
	table, err := rasql.ReadTableOf[R](schema.TableDef{
		Name:    "codec_rows",
		Columns: []schema.ColumnDef{{Name: "value", Type: schema.TextType{}, Nullable: nullable}},
	})
	require.NoError(t, err)
	relation, err := rasql.SourceOf(table, "")
	require.NoError(t, err)
	result, err := rasql.NewResultSchema(rasql.ResultColumn{
		Name: "value", Type: schema.TextType{}, Nullable: nullable, Codec: "text",
	})
	require.NoError(t, err)

	var item rasql.ProjectionItem
	if nullable {
		column, bindErr := rasql.BindNullColumn[R, string](relation, "value", "text")
		require.NoError(t, bindErr)
		item = rasql.NullItem("value", column.NullExpr(), schema.TextType{}, "text")
	} else {
		column, bindErr := rasql.BindColumn[R, string](relation, "value", "text")
		require.NoError(t, bindErr)
		item = rasql.Item("value", column.Expr(), schema.TextType{}, "text")
	}
	projection, err := rasql.NewProjection([]rasql.ProjectionItem{item}, codecScanDecoder[R]{result: result, decode: decode})
	require.NoError(t, err)

	registry, err := rasql.NewCodecRegistry(map[rasql.CodecID]rasql.ValueCodec{"text": codec})
	require.NoError(t, err)
	profile, err := rasql.EngineProfileFromVersion("sqlite-3.35", 3, 35, 0)
	require.NoError(t, err)
	executor, err := rasql.WithEngineProfile(&codecScanExecutor{rows: values}, profile)
	require.NoError(t, err)
	executor, err = rasql.WithCodecs(executor, registry)
	require.NoError(t, err)

	return rasql.All(t.Context(), executor, rasql.Select(relation.Source(), projection))
}

func TestCodecScanSource(t *testing.T) {
	t.Run("decodes a present value and clears a null one", func(t *testing.T) {
		decodes := 0
		rows, err := codecScanQuery(t, [][]any{{"present"}, {nil}},
			codecScanCounting{decodes: &decodes}, true,
			func(source rasql.ScanSource, row *codecScanNullableRow) error { return source.Scan(&row.Value) })
		require.NoError(t, err)
		require.Len(t, rows, 2)
		require.True(t, rows[0].Value.Valid)
		require.Equal(t, "present", rows[0].Value.Value)
		require.False(t, rows[1].Value.Valid)
		require.Empty(t, rows[1].Value.Value)
		require.Equal(t, 1, decodes, "a null value never reaches the codec")
	})

	t.Run("preserves a scanner's own null cause", func(t *testing.T) {
		_, err := codecScanQuery(t, [][]any{{nil}},
			codecScanCounting{}, false,
			func(source rasql.ScanSource, row *codecScanScannerRow) error { return source.Scan(&row.Value) })
		var decodeErr *rasql.DecodeError
		require.ErrorAs(t, err, &decodeErr)
		require.ErrorIs(t, err, errCodecScannerNull)
		require.NotContains(t, err.Error(), "<nil>")
	})

	t.Run("a sql.NullString takes a null without an error", func(t *testing.T) {
		rows, err := codecScanQuery(t, [][]any{{nil}},
			codecScanCounting{}, false,
			func(source rasql.ScanSource, row *codecScanSQLNullRow) error { return source.Scan(&row.Value) })
		require.NoError(t, err)
		require.Len(t, rows, 1)
		require.False(t, rows[0].Value.Valid)
	})

	t.Run("invalidates a nullable destination before a failed decode", func(t *testing.T) {
		failure := errors.New("decode failed")
		_, err := codecScanQuery(t, [][]any{{"new"}},
			codecScanFailing{err: failure}, true,
			func(source rasql.ScanSource, row *codecScanNullableRow) error {
				row.Value = rasql.Nullable[string]{Value: "old", Valid: true}
				if scanErr := source.Scan(&row.Value); scanErr != nil {
					require.False(t, row.Value.Valid, "the destination is invalidated before the decode fails")
					require.Empty(t, row.Value.Value)
					return scanErr
				}
				return nil
			})
		require.ErrorIs(t, err, failure)
	})
}

type codecScanNullableRow struct{ Value rasql.Nullable[string] }
type codecScanScannerRow struct{ Value codecScanRejectingNull }
type codecScanSQLNullRow struct{ Value sql.NullString }

type codecScanDecoder[R any] struct {
	result rasql.ResultSchema
	decode func(rasql.ScanSource, *R) error
}

func (d codecScanDecoder[R]) ResultSchema() rasql.ResultSchema { return d.result }
func (codecScanDecoder[R]) Presence() []rasql.Presence         { return nil }
func (d codecScanDecoder[R]) DecodeRow(source rasql.ScanSource, row *R) error {
	return d.decode(source, row)
}

// codecScanExecutor answers one query from a fixed set of raw driver values.
type codecScanExecutor struct{ rows [][]any }

func (*codecScanExecutor) Dialect() dialect.Dialect { return dialect.SQLite() }

func (e *codecScanExecutor) Query(context.Context, stmt.Statement) (rasql.ResultRows, error) {
	return &codecScanRows{values: e.rows}, nil
}

func (*codecScanExecutor) Exec(context.Context, stmt.Statement) (sql.Result, error) {
	return driver.RowsAffected(0), nil
}

type codecScanRows struct {
	values [][]any
	index  int
	closed bool
}

func (*codecScanRows) Columns() ([]string, error) { return []string{"value"}, nil }
func (r *codecScanRows) Next() bool               { return r.index < len(r.values) }
func (r *codecScanRows) Scan(destinations ...any) error {
	if r.index >= len(r.values) {
		return sql.ErrNoRows
	}
	values := r.values[r.index]
	if len(values) != len(destinations) {
		return fmt.Errorf("scan destinations: got %d, want %d", len(destinations), len(values))
	}
	for i, destination := range destinations {
		target, ok := destination.(*any)
		if !ok {
			return fmt.Errorf("codec scan source reads into *any, got %T", destination)
		}
		*target = values[i]
	}
	r.index++
	return nil
}
func (r *codecScanRows) Close() error {
	r.closed = true
	return nil
}
func (*codecScanRows) Err() error { return nil }
func (*codecScanRows) RecordRow() {}
func (r *codecScanRows) Finish(err error, _ bool) error {
	_ = r.Close()
	return err
}
