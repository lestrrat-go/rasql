package scalar_where

import "github.com/lestrrat-go/rasql"

func bad() {
	_ = rasql.Select(rasql.Table[int64]{}, rasql.Projection[int64]{}).Where(rasql.Value(true))
}
