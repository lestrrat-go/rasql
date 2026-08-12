package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
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
		if _, dynamic := any(&probe).(row.DestinationScanner); !dynamic {
			decodeRows[T](row.Scan(rows))(yield)
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
		scanner := any(&result).(row.DestinationScanner)
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
		scanner, ok := any(&result).(row.Scanner)
		if !ok {
			decodeRows[T](row.Scan(rows))(yield)
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
func scanTypedRendered[T any](ctx context.Context, db DB, statement render.Statement) iter.Seq2[T, error] {
	return scanTypedRenderedWith(ctx, db, statement, scanTypedRows[T])
}

// scanTypedRenderedStatic defers the query until iteration begins and scans a
// statically-known complete generated row projection directly into its fields.
func scanTypedRenderedStatic[T any](ctx context.Context, db DB, statement render.Statement) iter.Seq2[T, error] {
	return scanTypedRenderedWith(ctx, db, statement, scanTypedRowsStatic[T])
}

func scanTypedRenderedWith[T any](ctx context.Context, db DB, statement render.Statement, scan func(*sql.Rows) iter.Seq2[T, error]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		rows, err := db.QueryRendered(ctx, statement)
		if err != nil {
			yield(zero, err)
			return
		}
		scan(rows)(yield)
	}
}

// decodeRows adapts a rangeable sequence of row.Dynamic into one that decodes each
// row as T.
func decodeRows[T any](rows iter.Seq2[row.Dynamic, error]) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		index := 0
		for result, err := range rows {
			if err != nil {
				yield(zero, err)
				return
			}
			decoded, err := row.Decode[T](result)
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

// collectAll gathers every value from a rangeable sequence.
func collectAll[T any](rows iter.Seq2[T, error]) ([]T, error) {
	decoded := make([]T, 0)
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
