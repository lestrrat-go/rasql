package wrong_value
import "github.com/lestrrat-go/rasql"
func bad() { _ = rasql.EqualValue(rasql.Value(int64(1)), "wrong") }
