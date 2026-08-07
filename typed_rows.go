package rasql

import (
	"fmt"
	"iter"

	"github.com/lestrrat-go/rasql/row"
)

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
