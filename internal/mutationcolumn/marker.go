package mutationcolumn

// NonNull marks a typed non-null mutation column without exposing its carrier.
type NonNull[Row, Value any] struct{}

// Nullable marks a typed nullable mutation column without exposing its carrier.
type Nullable[Row, Value any] struct{}
