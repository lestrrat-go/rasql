package query

// TupleMembership is a private-use tuple IN predicate for relationship loads.
type TupleMembership struct {
	Columns []ColumnRef
	Values  [][]any
}

func (TupleMembership) expression() {}
func TupleIn(columns []ColumnRef, values [][]any) TupleMembership {
	return TupleMembership{Columns: columns, Values: values}
}
