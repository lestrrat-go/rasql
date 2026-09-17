package mismatched_projection

import "github.com/lestrrat-go/rasql"

func bad() {
	var p rasql.Projection[string]
	var q rasql.Query[int64] = rasql.Select(rasql.Table[int64]{}, p)
	_ = q
}
