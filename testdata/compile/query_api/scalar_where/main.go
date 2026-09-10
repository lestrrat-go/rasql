package scalar_where
import "github.com/lestrrat-go/rasql"
func bad() { _ = rasql.Select(rasql.Source{}, rasql.Projection[int64]{}).Where(rasql.Value(true)) }
