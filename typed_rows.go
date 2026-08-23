package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"reflect"

	"github.com/lestrrat-go/rasql/internal/rowvalue"
	"github.com/lestrrat-go/rasql/statement"
)

// scanTypedRows maps runtime result-column names to generated fields before
// Scan. Other row types use the dynamic decoder.
func scanTypedRows[T any](rows *sql.Rows) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		if rows == nil {
			return
		}

		var probe T
		if _, dynamic := any(&probe).(DestinationScanner); !dynamic {
			decodeRows[T](rowvalue.Scan(rows))(yield)
			return
		}
		defer func() {
			_ = rows.Close()
		}()
		names, err := rows.Columns()
		if err != nil {
			yield(zero, fmt.Errorf("row: read result columns: %w", err))
			return
		}
		index := 0
		var result T
		scanner := any(&result).(DestinationScanner)
		destinations, err := scanner.ScanDestinations(names)
		if err != nil {
			yield(zero, fmt.Errorf("rasql: configure result scan: %w", err))
			return
		}
		for rows.Next() {
			if err := rows.Scan(destinations...); err != nil {
				yield(zero, fmt.Errorf("rasql: scan row %d: %w", index, err))
				return
			}
			index++
			if !yield(result, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(zero, fmt.Errorf("row: iterate result rows: %w", err))
		}
	}
}

// scanTypedRowsStatic scans a complete, statically-known generated row
// projection directly into its fields. Other row types use the dynamic decoder.
func scanTypedRowsStatic[T any](rows *sql.Rows) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		if rows == nil {
			return
		}

		var result T
		scanner, ok := any(&result).(Scanner)
		if !ok {
			decodeRows[T](rowvalue.Scan(rows))(yield)
			return
		}

		defer func() {
			_ = rows.Close()
		}()
		index := 0
		for rows.Next() {
			if err := scanner.ScanRow(rows); err != nil {
				yield(zero, fmt.Errorf("rasql: scan row %d: %w", index, err))
				return
			}
			index++
			if !yield(result, nil) {
				return
			}
		}
		if err := rows.Err(); err != nil {
			yield(zero, fmt.Errorf("row: iterate result rows: %w", err))
		}
	}
}

// scanTypedRendered defers the query until iteration begins and maps each
// result column to a generated row field at runtime.
func scanTypedRendered[T any](ctx context.Context, db DB, stmt statement.Statement) iter.Seq2[T, error] {
	return scanTypedRenderedWith(ctx, db, stmt, scanTypedRows[T])
}

// scanTypedRenderedStatic defers the query until iteration begins and scans a
// statically-known complete generated row projection directly into its fields.
func scanTypedRenderedStatic[T any](ctx context.Context, db DB, stmt statement.Statement) iter.Seq2[T, error] {
	return scanTypedRenderedWith(ctx, db, stmt, scanTypedRowsStatic[T])
}

func scanTypedRenderedWith[T any](ctx context.Context, db DB, stmt statement.Statement, scan func(*sql.Rows) iter.Seq2[T, error]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		rows, err := db.QueryRendered(ctx, stmt)
		if err != nil {
			yield(zero, err)
			return
		}
		scan(rows)(yield)
	}
}

// decodeRows adapts a rangeable sequence of rowvalue.Row into one that decodes
// each row as T.
func decodeRows[T any](rows iter.Seq2[rowvalue.Row, error]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		index := 0
		for result, err := range rows {
			if err != nil {
				yield(zero, err)
				return
			}
			decoded, err := rowvalue.Decode[T](result)
			if err != nil {
				yield(zero, fmt.Errorf("rasql: decode row %d: %w", index, err))
				return
			}
			index++
			if !yield(decoded, nil) {
				return
			}
		}
	}
}

// maxCollectPreallocBytes bounds the slice collectAll reserves before it has
// read a single row. A LIMIT is an upper bound on the result, not a count of
// it, so a statement that sets an enormous one must not be able to commit an
// enormous allocation for a result that turns out to be three rows.
const maxCollectPreallocBytes = 256 * 1024

// preallocCapacity turns an upper bound on the row count into a slice capacity.
// It returns 0 when there is no bound to work from, which leaves collectAll
// allocating nothing up front, and it clamps a bound that would reserve more
// than maxCollectPreallocBytes. A row type wider than the whole budget clamps
// to 0, which is the unhinted behavior.
func preallocCapacity[T any](hint int) int {
	if hint <= 0 {
		return 0
	}
	size := int(reflect.TypeFor[T]().Size())
	if size < 1 {
		size = 1
	}
	if maximum := maxCollectPreallocBytes / size; hint > maximum {
		return maximum
	}
	return hint
}

// collectAll gathers every value from a rangeable sequence. hint is the most
// rows the sequence can yield, or 0 when that is not known. It returns a
// non-nil empty slice for an empty sequence.
func collectAll[T any](rows iter.Seq2[T, error], hint int) ([]T, error) {
	decoded := make([]T, 0, preallocCapacity[T](hint))
	for value, err := range rows {
		if err != nil {
			return nil, err
		}
		decoded = append(decoded, value)
	}
	return decoded, nil
}

// exactlyOne requires that rows yields exactly one value. It returns
// [ErrNoRows] for an empty sequence and [ErrMultipleRows] as soon as a second
// value arrives, so every caller that expects one row reports the same
// sentinels.
func exactlyOne[T any](rows iter.Seq2[T, error]) (T, error) {
	var zero T
	var result T
	count := 0
	for value, err := range rows {
		if err != nil {
			return zero, err
		}
		result = value
		count++
		if count > 1 {
			return zero, ErrMultipleRows
		}
	}
	if count != 1 {
		return zero, ErrNoRows
	}
	return result, nil
}
