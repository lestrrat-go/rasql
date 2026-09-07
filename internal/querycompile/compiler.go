package querycompile

import (
	"fmt"
	"github.com/lestrrat-go/rasql/dialect"
	"github.com/lestrrat-go/rasql/internal/engineprofile"
	"github.com/lestrrat-go/rasql/query"
	"github.com/lestrrat-go/rasql/render"
	"github.com/lestrrat-go/rasql/sqltext"
	"github.com/lestrrat-go/rasql/stmt"
)

type Compiler struct {
	profile engineprofile.Profile
	dialect dialect.Dialect
}

func New(p engineprofile.Profile) (Compiler, error) {
	if p.ID == "" || p.Limits.MaxBindParameters <= 0 {
		return Compiler{}, fmt.Errorf("%w: invalid profile", engineprofile.ErrInvalidProfile)
	}
	d, err := dialectFor(p)
	if err != nil {
		return Compiler{}, err
	}
	return Compiler{profile: p, dialect: d}, nil
}
func (c Compiler) Select(q query.ResultQuery) (stmt.Statement, error) {
	return render.Result(c.dialect, q)
}
func (c Compiler) Write(q query.WriteStatement) (stmt.Statement, error) {
	return render.Write(c.dialect, q)
}
func (c Compiler) Native(s stmt.Statement) (stmt.Statement, error) {
	if s.SQL() == "" {
		return stmt.Statement{}, fmt.Errorf("native statement SQL must not be blank")
	}
	if len(s.Args()) > c.profile.Limits.MaxBindParameters {
		return stmt.Statement{}, fmt.Errorf("%w: %d", engineprofile.ErrBindLimit, len(s.Args()))
	}
	return stmt.New(sqltext.Text(s.SQL()), s.Args()...), nil
}
func dialectFor(p engineprofile.Profile) (dialect.Dialect, error) {
	switch p.Engine {
	case engineprofile.PostgreSQL:
		return dialect.PostgreSQL(), nil
	case engineprofile.MySQL:
		return dialect.MySQL(), nil
	case engineprofile.SQLite:
		return dialect.SQLite(), nil
	default:
		return nil, fmt.Errorf("custom dialect %q is unavailable", p.CustomName)
	}
}
