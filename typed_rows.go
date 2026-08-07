package rasql

import (
	"context"
	"database/sql"
	"fmt"
	"iter"
	"slices"

	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/row"
)

// scanTypedRows scans complete generated rows directly. Partial and reordered
// generated rows map result-column names to field destinations before Scan.
// Other row types use the dynamic decoder.
func scanTypedRows[T any](rows *sql.Rows) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		if rows == nil {
			return
		}

		var probe T
		fastScanner, fast := any(&probe).(row.Scanner)
		_, dynamic := any(&probe).(row.DestinationScanner)
		if !fast && !dynamic {
			decodeRows[T](row.Scan(rows))(yield)
			return
		}
		names, err := rows.Columns()
		if err != nil {
			yield(zero, fmt.Errorf("row: read result columns: %w", err))
			return
		}
		if fast && slices.Equal(names, fastScanner.ScanColumns()) {
			defer rows.Close()
			index := 0
			for rows.Next() {
				var result T
				scanner := any(&result).(row.Scanner)
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
			return
		}
		if !dynamic {
			decodeRows[T](row.Scan(rows))(yield)
			return
		}

		defer rows.Close()
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

// scanTypedRendered defers the query until iteration begins and scans each
// result directly into a generated row when its result shape matches.
func scanTypedRendered[T any](ctx context.Context, x Executor, statement render.Statement) iter.Seq2[T, error] {
	return func(yield func(T, error) bool) {
		var zero T
		rows, err := x.QueryRendered(ctx, statement)
		if err != nil {
			yield(zero, err)
			return
		}
		scanTypedRows[T](rows)(yield)
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
