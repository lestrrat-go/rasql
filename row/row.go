// Package row provides typed access to database result values.
package row

import "github.com/lestrrat-go/rasql/internal/rowvalue"

// Dynamic contains one database result row whose columns are known only at run
// time. Every row of one query shares the same columnHeader, so decoding a row
// costs one slice of values plus a lookup through the header's index, rather
// than a map[string]any allocated fresh for every row.
type Dynamic = rowvalue.Row

// NewDynamic validates column names and values and returns an independent row value.
func NewDynamic(names []string, values []any) (Dynamic, error) {
	return rowvalue.NewRow(names, values)
}

// Get decodes the named value in r as T.
func Get[T any](r Dynamic, name string) (T, error) {
	return rowvalue.Get[T](r, name)
}

// Assign decodes the named value in r into destination.
func Assign[T any](r Dynamic, name string, destination *T) error {
	return rowvalue.Assign(r, name, destination)
}

// Decode populates T from rasql-tagged fields and snake-cased exported field
// names. It is the only read mapping; a row type that carries generated scan
// methods is filled by those instead, through the typed builders rather than
// through Decode.
//
// The per-type facts this needs -- which fields map to which columns -- are
// resolved once per type by planFor and cached, rather than recomputed on
// every call. A type whose plan holds an error (a bad tag or an unexported
// tagged field) reports the same error on every call, resolved before any row
// is read: an error found while planning always wins over a per-row error,
// such as a missing column, that a field reached earlier in declaration order
// would have reported first under the old, per-row walk.
func Decode[T any](r Dynamic) (T, error) {
	return rowvalue.Decode[T](r)
}
