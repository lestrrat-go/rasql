package is_null_nonnull
import "github.com/lestrrat-go/rasql"
func bad() { _ = rasql.IsNull(rasql.Value("required")) }
