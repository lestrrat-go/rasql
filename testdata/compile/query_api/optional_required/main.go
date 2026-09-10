package optional_required
import "github.com/lestrrat-go/rasql"
func bad() { var required rasql.Expr[int64]; var optional rasql.NullExpr[int64]; _ = rasql.EqualExpr(required, optional) }
